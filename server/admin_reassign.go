package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

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
	}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&in)
	}
	wantEmail := strings.TrimSpace(strings.ToLower(in.SupporterEmail))
	wantID := strings.TrimSpace(in.SupporterID)
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
	if hasWorklog {
		c.JSON(http.StatusConflict, gin.H{
			"error": "task_in_progress",
			"message": "Work has already started on this task, and its logged time and " +
				"location belong to the current supporter. Remove the task and ask the " +
				"requester to post it again instead.",
		})
		return
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

	affected, err := applyReassign(ctx, taskID, target, oldID)
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
	metaJSON, _ := json.Marshal(meta)
	reason := "swap"
	if oldEmail == "" {
		reason = "direct_assign"
	}
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
	notifyUser(ctx, target.UID, target.Email, notify.CreateNotificationInput{
		TaskID: taskID,
		Type:   "TASK_REASSIGNED",
		Title:  "You've been assigned a task",
		Body: fmt.Sprintf("You've been assigned %q by the HO:RA team. "+
			"Open the task to see the details and message the requester.", title),
		SupporterName: target.Name,
		TaskTitle:     title,
	})

	if oldID != nil && *oldID != "" {
		notifyUser(ctx, *oldID, oldEmail, notify.CreateNotificationInput{
			TaskID: taskID,
			Type:   "TASK_REASSIGNED",
			Title:  "You've been unassigned from a task",
			Body: fmt.Sprintf("You've been unassigned from %q — the task has been "+
				"reassigned. Thanks for your flexibility!", title),
			TaskTitle: title,
		})
	}

	notifyUser(ctx, requesterID, requesterEmail, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "TASK_REASSIGNED",
		Title:         "Your task has a new supporter",
		Body:          fmt.Sprintf("Your task %q has a new supporter: %s.", title, target.Name),
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
	tag, err := db.Exec(ctx, `
		update public.tasks
		set assigned_to_id = $2::uuid,
		    assigned_to    = $3
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
		select u.id::text, coalesce(u.email,''), coalesce(p.name, u.name, ''),
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

	t.Name = name
	if t.Name == "" {
		t.Name = displayName(t.Email)
	}
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
		select u.id::text, coalesce(u.email,''), coalesce(p.name, u.name, ''), coalesce(p.city,'')
		from public.profiles p
		join public.users u on lower(u.email) = lower(p.email)
		where coalesce(p.is_verified_supporter, false) = true
		  and coalesce(u.email,'') <> ''
		order by lower(coalesce(p.name, u.name, u.email))
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
		if s.Name == "" {
			s.Name = displayName(s.Email)
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[admin.supporters][rows] err=%v", err)
	}

	c.JSON(http.StatusOK, items)
}
