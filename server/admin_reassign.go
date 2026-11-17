package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"hora-auth/helpers"
	notify "hora-auth/internal/notify"
)

// Admin reassignment of a task's supporter.
//
// Live test rounds produce two situations the normal accept flow has no answer
// for: the assigned supporter becomes unavailable and someone else has to take
// the task, and a task that has been coordinated over WhatsApp needs to land on
// a specific supporter without waiting for them to find and accept it. Both
// were being handled by editing assigned_to_id in the database by hand, which
// silently skips everything that makes an assignment real — the TalkJS chat
// seat, the three notifications, the audit trail.
//
// Deliberately narrower than "edit the assignee":
//
//   - Only an 'open' task. completed/cancelled/removed are history.
//   - Only before any clock-in. worklogs are keyed by the supporter's email
//     and task_gps_pings by their user id, so a swap after work has started
//     would attribute one person's tracked time and location to another. That
//     is a data-integrity problem, not a UX one, so it is refused outright
//     (task_in_progress) and the admin is pointed at remove + repost.
//   - Only to an approved supporter, and not to whoever already holds it.
//
// THE ONE WAY PAST THE CLOCK-IN RULE: void_prior_work. A supporter who starts
// a task and then leaves it used to leave ops with two bad options — remove the
// task and make the requester post it again, or force-complete it and charge
// the requester for work nobody finished. With void_prior_work the outgoing
// supporter's sessions are taken OFF the task before the swap (recorded in the
// audit row, deleted from worklogs), so the data-integrity problem above does
// not arise: there is no tracked time left to misattribute. The task then
// looks exactly like one that was accepted and never started — same hold,
// clock at zero — and the incoming supporter does the whole job. The requester
// pays for one supporter's time, once. See voidAndReassign.
//
// Assigning an unassigned task is the same operation with no outgoing
// supporter, so it shares this path rather than getting its own.

// reassignTarget is the resolved incoming supporter.
type reassignTarget struct {
	UID   string
	Email string
	Name  string
}

// POST /admin/tasks/:id/reassign  { "supporter_email": "..." } | { "supporter_id": "uuid" }
//
// Not idempotent in the way remove is — a second call with the same supporter
// is a same_supporter error rather than a no-op, because "already assigned to
// this person" is exactly the state the caller asked for and re-running the
// notification fan-out for it would be noise.
func adminReassignTask(c *gin.Context) {
	taskID := c.Param("id")
	actorUID := c.GetString("uid")
	actorEmail := c.GetString("email")
	if actorUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var in struct {
		SupporterEmail string `json:"supporter_email"`
		SupporterID    string `json:"supporter_id"`
		// Take the current supporter's logged time off the task and hand it
		// over anyway. Requires a reason: it is the decision not to pay
		// someone for time they logged, and the audit row has to say why.
		VoidPriorWork bool   `json:"void_prior_work"`
		Reason        string `json:"reason"`
	}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&in)
	}
	wantEmail := strings.TrimSpace(strings.ToLower(in.SupporterEmail))
	wantID := strings.TrimSpace(in.SupporterID)
	voidReason := strings.TrimSpace(in.Reason)
	if wantEmail == "" && wantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "supporter_required",
			"message": "Provide supporter_email or supporter_id.",
		})
		return
	}

	ctx := c.Request.Context()

	var status, title string
	var requesterID, requesterEmail, oldEmail string
	var oldID *string
	if err := db.QueryRow(ctx, `
		select status, coalesce(title,''), requester_id::text, coalesce(requester,''),
		       assigned_to_id::text, coalesce(assigned_to,'')
		from public.tasks
		where id = $1::uuid
	`, taskID).Scan(&status, &title, &requesterID, &requesterEmail, &oldID, &oldEmail); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	if status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "task_not_reassignable",
			"status":  status,
			"message": fmt.Sprintf("A %s task cannot be reassigned.", status),
		})
		return
	}

	// Any worklog at all, open or closed, means the current supporter has
	// already been on the clock for this task. See the file comment.
	var hasWorklog bool
	if err := db.QueryRow(ctx, `
		select exists(select 1 from public.worklogs where task_id = $1::uuid)
	`, taskID).Scan(&hasWorklog); err != nil {
		log.Printf("[admin.reassign][worklog] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if hasWorklog && !in.VoidPriorWork {
		c.JSON(http.StatusConflict, gin.H{
			"error": "task_in_progress",
			"message": "Work has already started on this task, and its logged time and " +
				"location belong to the current supporter. To hand it over anyway, " +
				"reassign with the logged time voided (the current supporter is not " +
				"paid for it), or cancel the task.",
		})
		return
	}
	if in.VoidPriorWork {
		if oldID == nil || *oldID == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "nothing_to_void",
				"message": "Nobody holds this task, so there is no logged time to void.",
			})
			return
		}
		if voidReason == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "reason_required",
				"message": "Say why the logged time is being voided — it is recorded in the audit log.",
			})
			return
		}
	}

	target, errCode, errMsg := resolveApprovedSupporter(c, wantID, wantEmail)
	if errCode != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCode, "message": errMsg})
		return
	}
	if oldID != nil && *oldID == target.UID {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "same_supporter",
			"message": fmt.Sprintf("%s is already assigned to this task.", target.Email),
		})
		return
	}
	if target.UID == requesterID {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "supporter_is_requester",
			"message": "A requester cannot be assigned to their own task.",
		})
		return
	}
	// A blocked pair cannot be matched by ops either (safety.go), and the
	// App Review sandbox never meets a real account (sandbox.go).
	blocked, err := usersBlocked(ctx, target.UID, requesterID)
	if err != nil {
		log.Printf("[admin.reassign][block] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if blocked {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "users_blocked",
			"message": fmt.Sprintf("%s and the requester have blocked each other, so they can't be matched.", target.Email),
		})
		return
	}
	if allowed, err := sandboxPartitionAllows(ctx, requesterID, target.UID); err != nil || !allowed {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "sandbox_mismatch",
			"message": "The App Review account and real accounts can't be matched with each other.",
		})
		return
	}

	var voided voidedWork
	var affected int64
	if in.VoidPriorWork && hasWorklog {
		voided, affected, err = voidAndReassign(ctx, taskID, target, *oldID)
	} else {
		// void_prior_work on a task with no sessions is just a swap: there
		// is nothing to take off it.
		affected, err = applyReassign(ctx, taskID, target, oldID)
	}
	if err != nil {
		log.Printf("[admin.reassign][update] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if affected == 0 {
		var nowStatus, nowAssigned string
		_ = db.QueryRow(ctx, `
			select status, coalesce(assigned_to,'') from public.tasks where id = $1::uuid
		`, taskID).Scan(&nowStatus, &nowAssigned)
		c.JSON(http.StatusConflict, gin.H{
			"error":       "task_changed",
			"message":     "This task changed while you were reassigning it. Reload the feed and try again.",
			"status":      nowStatus,
			"assigned_to": nowAssigned,
		})
		return
	}

	// Everything below this line is after-the-fact: the swap is committed, and
	// no failure here may undo it.

	chatResult := talkjsHandoverChat(taskID, oldEmail, target.Email)

	meta := map[string]any{
		"admin_email":         actorEmail,
		"old_supporter_id":    derefOrEmpty(oldID),
		"old_supporter_email": oldEmail,
		"new_supporter_id":    target.UID,
		"new_supporter_email": target.Email,
		"talkjs":              chatResult,
	}
	reason := "swap"
	if oldEmail == "" {
		reason = "direct_assign"
	}
	if in.VoidPriorWork {
		// The only record of the sessions that were taken off the task: who
		// logged them, when, and how long. Not paid, not billed, not lost.
		reason = "swap_voided"
		meta["void_reason"] = voidReason
		meta["voided_sessions"] = voided.Sessions
		meta["voided_minutes"] = voided.Minutes
		meta["voided_gps_pings"] = voided.GPSPings
		meta["expired_extension_requests"] = voided.ExpiredRequests
	}
	metaJSON, _ := json.Marshal(meta)
	if _, err := db.Exec(ctx, `
		insert into public.audit_logs (job_id, actor_id, action, reason, meta)
		values ($1::uuid, $2::uuid, 'TASK_REASSIGNED', $3, $4::jsonb)
	`, taskID, actorUID, reason, string(metaJSON)); err != nil {
		log.Printf("[admin.reassign][audit] task=%s err=%v", taskID, err)
	}

	// Three recipients, one notification type, wording composed per person —
	// see the migration and notify.taskReassignedEmail for why ORDER_ACCEPTED
	// was not reused. notifyUser is used throughout rather than
	// notifyAssignee/notifyRequesterRich because those re-read the task to find
	// their recipient, which cannot reach the supporter who was just replaced.
	newBody := fmt.Sprintf("You've been assigned %q by the HO:RA team. "+
		"Open the task to see the details and message the requester.", title)
	oldTitle := "You've been unassigned from a task"
	oldBody := fmt.Sprintf("You've been unassigned from %q — the task has been "+
		"reassigned. Thanks for your flexibility!", title)
	reqBody := fmt.Sprintf("Your task %q has a new supporter: %s.", title, target.Name)
	if in.VoidPriorWork {
		// A supporter left the task. Nobody is thanked for their flexibility,
		// and the requester is told the one thing they will worry about:
		// that they are not paying twice.
		newBody = fmt.Sprintf("You've been assigned %q by the HO:RA team. The previous supporter "+
			"couldn't finish it, so you're starting it fresh: open the task to see the details "+
			"and message the requester.", title)
		oldTitle = "You've been taken off a task"
		oldBody = fmt.Sprintf("You've been taken off %q because it wasn't finished, and someone else "+
			"is taking over. Time logged on it isn't paid. If something went wrong on your side, "+
			"message HO:RA so we know.", title)
		reqBody = fmt.Sprintf("Your supporter wasn't able to finish %q, so %s is taking over. "+
			"You only pay for the time %s spends on it — nothing for the first supporter.",
			title, target.Name, target.Name)
	}

	notifyUser(ctx, target.UID, target.Email, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "TASK_REASSIGNED",
		Title:         "You've been assigned a task",
		Body:          newBody,
		SupporterName: target.Name,
		TaskTitle:     title,
	})

	if oldID != nil && *oldID != "" {
		notifyUser(ctx, *oldID, oldEmail, notify.CreateNotificationInput{
			TaskID:    taskID,
			Type:      "TASK_REASSIGNED",
			Title:     oldTitle,
			Body:      oldBody,
			TaskTitle: title,
		})
	}

	notifyUser(ctx, requesterID, requesterEmail, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "TASK_REASSIGNED",
		Title:         "Your task has a new supporter",
		Body:          reqBody,
		SupporterName: target.Name,
		TaskTitle:     title,
	})

	log.Printf("[admin.reassign] task=%s by=%s %q → %q talkjs=%s",
		taskID, actorEmail, oldEmail, target.Email, chatResult)

	c.JSON(http.StatusOK, gin.H{
		"ok":                     true,
		"task_id":                taskID,
		"previous_supporter":     oldEmail,
		"new_supporter":          target.Email,
		"new_supporter_name":     target.Name,
		"talkjs":                 chatResult,
		"old_supporter_notified": oldID != nil && *oldID != "",
		"voided_minutes":         voided.Minutes,
		"voided_sessions":        len(voided.Sessions),
	})
}

// applyReassign is the swap itself: assigned_to_id and the assigned_to email
// cache together, exactly the pair acceptTask writes.
//
// Same guard-in-the-UPDATE pattern as adminRemoveTask, widened to the two other
// things that can change under us between the handler's checks and this
// statement — a concurrent accept (assigned_to_id moved) and a clock-in (a
// worklog appeared). expectedOldID is the assignee the caller read, nil for an
// unassigned task; "is not distinct from" so nil matches NULL rather than
// matching nothing. Losing any of those races updates 0 rows, and the caller
// reports the current state rather than overwriting it.
//
// Split out from the handler so the race can be tested against the real
// statement instead of a copy of it.
func applyReassign(ctx context.Context, taskID string, target reassignTarget, expectedOldID *string) (int64, error) {
	// accepted_at moves with the assignment: the new supporter's commitment
	// starts now, and the free-cancellation window (billing.go
	// cancelIsWithinGrace) is measured from when the person who is actually on
	// the task took it — not from when somebody else did, possibly days ago.
	tag, err := db.Exec(ctx, `
		update public.tasks
		set assigned_to_id = $2::uuid,
		    assigned_to    = $3,
		    accepted_at    = now()
		where id = $1::uuid
		  and status = 'open'
		  and assigned_to_id is not distinct from $4::uuid
		  and not exists (select 1 from public.worklogs w where w.task_id = public.tasks.id)
	`, taskID, target.UID, target.Email, expectedOldID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// voidedSession is one work session taken off a task, as the audit row keeps it.
type voidedSession struct {
	User    string     `json:"user"`
	StartAt time.Time  `json:"start_at"`
	EndAt   *time.Time `json:"end_at"` // nil: the timer was still running
	Minutes int        `json:"minutes"`
}

type voidedWork struct {
	Sessions        []voidedSession
	Minutes         int
	GPSPings        int64
	ExpiredRequests int64
}

// voidAndReassign takes the outgoing supporter's work off a started task and
// hands the task to target, in one transaction — either the task ends up with
// the new supporter and a clean clock, or nothing changed.
//
// What "clean" means, and why each piece goes:
//
//   - worklogs: DELETED (after being copied into the returned voidedWork, which
//     the caller writes to the audit log). Every reader of billable time sums
//     this table by task — billing, the time cap, the stage label, the feed —
//     so a row left behind under any flag would be billed to the requester and
//     paid to whoever completes the task. Removing the rows is the only form of
//     "void" that all of those readers already understand.
//   - task_gps_pings: deleted for the same reason the original rule gave — two
//     of the three "where is my supporter" reads take the latest ping for the
//     TASK, so the requester would be shown the departed supporter's last
//     position as the new one's.
//   - pending extension_requests: expired. They are the outgoing supporter's
//     questions; nobody is left to act on the answer. APPROVED ones stand: the
//     requester agreed to that time or budget for the task, not for a person.
//   - the en-route, arrival and time-cap latches: reset, so the new supporter
//     gets their own "on my way", their own arrival notice and their own
//     warning as the ceiling approaches.
//
// The row lock plus the assigned_to_id guard is what makes a concurrent
// reassign, cancel or completion lose cleanly: 0 rows, and the caller reports
// the current state.
func voidAndReassign(ctx context.Context, taskID string, target reassignTarget, expectedOldID string) (voidedWork, int64, error) {
	var out voidedWork
	tx, err := db.Begin(ctx)
	if err != nil {
		return out, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked bool
	if err := tx.QueryRow(ctx, `
		select true from public.tasks
		 where id = $1::uuid and status = 'open' and assigned_to_id = $2::uuid
		 for update
	`, taskID, expectedOldID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, 0, nil
		}
		return out, 0, err
	}

	rows, err := tx.Query(ctx, `
		delete from public.worklogs where task_id = $1::uuid
		returning coalesce("user",''), start_at, end_at,
		          greatest(0, ceil(extract(epoch from (coalesce(end_at, now()) - start_at)) / 60.0))::int
	`, taskID)
	if err != nil {
		return out, 0, err
	}
	for rows.Next() {
		var v voidedSession
		if err := rows.Scan(&v.User, &v.StartAt, &v.EndAt, &v.Minutes); err != nil {
			rows.Close()
			return out, 0, err
		}
		out.Sessions = append(out.Sessions, v)
		out.Minutes += v.Minutes
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, 0, err
	}

	tag, err := tx.Exec(ctx, `delete from public.task_gps_pings where task_id = $1::uuid`, taskID)
	if err != nil {
		return out, 0, err
	}
	out.GPSPings = tag.RowsAffected()

	tag, err = tx.Exec(ctx, `
		update public.extension_requests
		   set status = 'expired', resolved_at = now()
		 where task_id = $1::uuid and status = 'pending'
	`, taskID)
	if err != nil {
		return out, 0, err
	}
	out.ExpiredRequests = tag.RowsAffected()

	tag, err = tx.Exec(ctx, `
		update public.tasks
		   set assigned_to_id = $2::uuid,
		       assigned_to    = $3,
		       accepted_at    = now(),
		       enroute_at = null,
		       arrival_notified_at = null,
		       time_cap_warned_at = null,
		       time_cap_reached_at = null,
		       time_cap_ops_alerted_at = null
		 where id = $1::uuid
	`, taskID, target.UID, target.Email)
	if err != nil {
		return out, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, 0, err
	}
	return out, tag.RowsAffected(), nil
}

// resolveApprovedSupporter turns the request's supporter_id/supporter_email
// into a user, refusing anyone who is not an approved supporter. Returns an
// error code + message on failure rather than writing a response itself, so
// the caller keeps the ordering of its own checks.
//
// users and profiles are joined on email: profiles.id is documented as kept in
// sync with users.id, but email is the column both tables are actually looked
// up by elsewhere (supporterDecision, applySupporterHandler), so it is the
// safer key here too.
func resolveApprovedSupporter(c *gin.Context, wantID, wantEmail string) (reassignTarget, string, string) {
	ctx := c.Request.Context()

	var (
		t        reassignTarget
		name     string
		approved bool
	)

	query := `
		select u.id::text, coalesce(u.email,''), coalesce(p.name, ''),
		       coalesce(p.is_verified_supporter, false)
		from public.users u
		left join public.profiles p on lower(p.email) = lower(u.email)
		where %s
	`
	var err error
	if wantID != "" {
		err = db.QueryRow(ctx, fmt.Sprintf(query, "u.id = $1::uuid"), wantID).
			Scan(&t.UID, &t.Email, &name, &approved)
	} else {
		err = db.QueryRow(ctx, fmt.Sprintf(query, "lower(u.email) = $1"), wantEmail).
			Scan(&t.UID, &t.Email, &name, &approved)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return t, "supporter_not_found", "No HO:RA account matches that supporter."
	}
	if err != nil {
		log.Printf("[admin.reassign][supporter] id=%q email=%q err=%v", wantID, wantEmail, err)
		return t, "supporter_not_found", "Could not look up that supporter."
	}
	if t.Email == "" {
		// Every notification path here needs an email to send to.
		return t, "supporter_not_found", "That account has no email address on file."
	}
	if !approved {
		return t, "supporter_not_approved",
			fmt.Sprintf("%s is not an approved supporter. Approve them first, then reassign.", t.Email)
	}

	t.Name = helpers.DisplayName(name, t.Email)
	return t, "", ""
}

// GET /admin/supporters — the approved supporters the reassign picker offers.
//
// Deliberately not /ops/supporter-applications: that endpoint lists everyone
// who has ever *applied*, which is both wider than this (rejected and pending
// applicants) and narrower (an approved supporter whose supporter_applied_at
// is null — approved before the apply flow existed, or by hand — never appears
// in it). The picker needs exactly the set that passes the reassign
// eligibility check, so it is queried on the same condition.
func adminListSupporters(c *gin.Context) {
	ctx := c.Request.Context()

	rows, err := db.Query(ctx, `
		select u.id::text, coalesce(u.email,''), coalesce(p.name, ''), coalesce(p.city,'')
		from public.profiles p
		join public.users u on lower(u.email) = lower(p.email)
		where coalesce(p.is_verified_supporter, false) = true
		  and coalesce(u.email,'') <> ''
		order by lower(coalesce(nullif(p.name,''), u.email))
	`)
	if err != nil {
		log.Printf("[admin.supporters][query] err=%v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	type supporter struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		City  string `json:"city"`
	}
	items := []supporter{}
	for rows.Next() {
		var s supporter
		if err := rows.Scan(&s.ID, &s.Email, &s.Name, &s.City); err != nil {
			log.Printf("[admin.supporters][scan] err=%v", err)
			continue
		}
		s.Name = helpers.DisplayName(s.Name, s.Email)
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[admin.supporters][rows] err=%v", err)
	}

	c.JSON(http.StatusOK, items)
}
