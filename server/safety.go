package main

// User safety: Report and Block (App Store Guideline 1.2 — the app has chat).
//
// REPORT. Either party on a task can report the other, from the chat screen or
// the task detail, under a preset reason. A report writes three things: a
// user_reports row (what ops works from — GET /admin/reports), an audit_logs
// row on the task (the same trail every admin action leaves), and an email to
// the ops allowlist, so a report is seen the day it is filed. The reason is a
// CLOSED SET owned here, served to both clients (GET /safety/report-reasons),
// for the same reason cancel reasons are (cancel_reasons.go): a vocabulary in
// two hardcoded client copies drifts, and ops has to be able to sort by it.
// The optional free text is for ops only and never reaches the reported user.
//
// BLOCK. Stored directionally (who blocked whom), ENFORCED MUTUALLY: every
// check asks "is there a row in either direction" (usersBlockedSQL), so
//
//   - neither sees the other's open tasks in the Available feed,
//   - neither can accept the other's task, even holding its id,
//   - ops cannot reassign one onto the other's task,
//   - an open task of one is unreadable to the other,
//   - and the chat between them goes read-only: both TalkJS participants are
//     set to access "Read" server-side, the clients render the thread with no
//     composer and an explanatory line (task.chat_blocked), and the TalkJS
//     webhook stops pushing messages across the pair.
//
// A block does not unwind a task already in progress between the two. The
// requester can still cancel under the normal policy, and ops is told in the
// block's email when a live task exists between them, so a person is never
// left alone on a job with somebody they have blocked.
//
// Everything is task-scoped: both report surfaces live on a task, and the
// target must be the OTHER party on it. That is also the authorization — you
// can only report or block somebody you were actually matched with (S-11:
// the acting principal is the session uid, never the body).

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type reportReason struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Ordered as they render. "Other" last; its free text goes to ops only.
var reportReasons = []reportReason{
	{Value: "harassment", Label: "Harassment or threats"},
	{Value: "inappropriate_messages", Label: "Inappropriate or offensive messages"},
	{Value: "unsafe_behavior", Label: "Unsafe behavior"},
	{Value: "no_show", Label: "Didn't show up"},
	{Value: "scam_or_payment", Label: "Scam or off-app payment request"},
	{Value: "discrimination", Label: "Discrimination"},
	{Value: "spam", Label: "Spam"},
	{Value: "other", Label: "Something else"},
}

func reportReasonLabel(code string) string {
	for _, r := range reportReasons {
		if r.Value == code {
			return r.Label
		}
	}
	return ""
}

// reportDetailsMaxRunes bounds the free text. The clients cap the field at
// the same length; this is the backstop.
const reportDetailsMaxRunes = 1000

// usersBlockedSQL is the mutual block predicate for SQL: true when either of
// the two uuid expressions has blocked the other.
func usersBlockedSQL(a, b string) string {
	return fmt.Sprintf(`exists (select 1 from public.user_blocks ub
	  where (ub.blocker_id = %[1]s and ub.blocked_id = %[2]s)
	     or (ub.blocker_id = %[2]s and ub.blocked_id = %[1]s))`, a, b)
}

// usersBlocked is the same predicate as a Go check.
func usersBlocked(ctx context.Context, a, b string) (bool, error) {
	if a == "" || b == "" || a == b {
		return false, nil
	}
	var blocked bool
	err := db.QueryRow(ctx, `select `+usersBlockedSQL("$1::uuid", "$2::uuid"), a, b).Scan(&blocked)
	if err != nil {
		return false, fmt.Errorf("read block between %s and %s: %w", a, b, err)
	}
	return blocked, nil
}

// taskParties is who was matched on a task: the requester, and the supporter
// — the current one, or the one who was on it when it was cancelled.
type taskParties struct {
	RequesterID string
	SupporterID string
	Title       string
	Status      string
}

func readTaskParties(ctx context.Context, taskID string) (taskParties, error) {
	var p taskParties
	err := db.QueryRow(ctx, `
		select requester_id::text,
		       coalesce(assigned_to_id::text, cancelled_assignee_id::text, ''),
		       coalesce(title, ''), coalesce(status, '')
		  from public.tasks where id = $1::uuid
	`, taskID).Scan(&p.RequesterID, &p.SupporterID, &p.Title, &p.Status)
	return p, err
}

// counterpartOn is the other party on the task for `me`, or "" when `me` is
// not a party at all. For the review sandbox's self-accepted task the other
// party is `me` again — which the callers refuse as a self-target.
func (p taskParties) counterpartOn(me string) string {
	switch me {
	case p.RequesterID:
		return p.SupporterID
	case p.SupporterID:
		return p.RequesterID
	}
	return ""
}

var (
	errSafetyNotAParty  = errors.New("not_a_party")
	errSafetySelfTarget = errors.New("cannot_target_self")
)

// resolveSafetyTarget checks that `target` is the other party to `me` on the
// task. It is the whole authorization for both report and block.
func resolveSafetyTarget(ctx context.Context, me, target, taskID string) (taskParties, error) {
	if target == me {
		return taskParties{}, errSafetySelfTarget
	}
	p, err := readTaskParties(ctx, taskID)
	if err != nil {
		return p, err
	}
	if other := p.counterpartOn(me); other == "" || other != target {
		return p, errSafetyNotAParty
	}
	return p, nil
}

func writeSafetyTargetError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errSafetySelfTarget):
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_target_self",
			"message": "You can't report or block yourself."})
	case errors.Is(err, errSafetyNotAParty), errors.Is(err, pgx.ErrNoRows):
		// 404 rather than 403: whether the task or the pairing exists is not
		// something a non-party gets to learn.
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found",
			"message": "You can only report or block someone you've been matched with on a task."})
	default:
		log.Printf("[safety] resolve target: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
	}
}

// RegisterSafetyRoutes mounts the user-facing half and the ops half.
func RegisterSafetyRoutes(r *gin.Engine, authMiddleware gin.HandlerFunc) {
	s := r.Group("/safety")
	s.Use(authMiddleware)
	{
		s.GET("/report-reasons", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"reasons": reportReasons})
		})
		s.POST("/report", reportUserHandler)
		s.POST("/block", blockUserHandler)
	}
	r.GET("/admin/reports", authMiddleware, requireOpsAdmin(), adminListReports)
	r.POST("/admin/reports/:id/resolve", authMiddleware, requireOpsAdmin(), adminResolveReport)
}

// ── POST /safety/report ────────────────────────────────────────────────────

type reportInput struct {
	UserID     string `json:"user_id"`
	TaskID     string `json:"task_id"`
	ReasonCode string `json:"reason_code"`
	Details    string `json:"details"`
}

func reportUserHandler(c *gin.Context) {
	me := c.GetString("uid")
	if me == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var in reportInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	in.UserID, in.TaskID = strings.TrimSpace(in.UserID), strings.TrimSpace(in.TaskID)
	in.ReasonCode = strings.TrimSpace(in.ReasonCode)
	in.Details = truncateRunes(strings.TrimSpace(in.Details), reportDetailsMaxRunes)
	if in.UserID == "" || in.TaskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id and task_id are required"})
		return
	}
	if reportReasonLabel(in.ReasonCode) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_reason",
			"message": "Pick a reason for the report."})
		return
	}
	ctx := c.Request.Context()
	parties, err := resolveSafetyTarget(ctx, me, in.UserID, in.TaskID)
	if err != nil {
		writeSafetyTargetError(c, err)
		return
	}

	reportID, created, err := insertUserReport(ctx, me, in)
	if err != nil {
		log.Printf("[safety][report] insert reporter=%s reported=%s task=%s: %v", me, in.UserID, in.TaskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if created {
		writeAudit(ctx, in.TaskID, me, "USER_REPORTED", in.ReasonCode, map[string]any{
			"report_id":   reportID,
			"reported_id": in.UserID,
			"has_details": in.Details != "",
		})
		notifyOpsOfReport(ctx, reportID, me, in, parties)
		log.Printf("[safety][report] report=%s reporter=%s reported=%s task=%s reason=%s",
			reportID, me, in.UserID, in.TaskID, in.ReasonCode)
	}
	c.JSON(http.StatusCreated, gin.H{
		"ok":        true,
		"report_id": reportID,
		"message":   "Thanks — our team reviews every report within 24 hours.",
	})
}

// insertUserReport writes the report, or returns the reporter's existing OPEN
// report against the same person on the same task — a double tap files one
// report, not two emails.
func insertUserReport(ctx context.Context, reporter string, in reportInput) (id string, created bool, err error) {
	err = db.QueryRow(ctx, `
		select id::text from public.user_reports
		 where reporter_id = $1::uuid and reported_id = $2::uuid and task_id = $3::uuid
		   and resolved_at is null
		 limit 1
	`, reporter, in.UserID, in.TaskID).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	err = db.QueryRow(ctx, `
		insert into public.user_reports (reporter_id, reported_id, task_id, reason_code, details)
		values ($1::uuid, $2::uuid, $3::uuid, $4, nullif($5, ''))
		returning id::text
	`, reporter, in.UserID, in.TaskID, in.ReasonCode, in.Details).Scan(&id)
	return id, err == nil, err
}

// safetyEmailOps is emailOpsAdmins behind a variable, so a test can see the
// ops notification a report or block sends. Production never reassigns it.
var safetyEmailOps = emailOpsAdmins

// notifyOpsOfReport emails the ops allowlist. Names, not emails or phone
// numbers (S-12); the ops panel has the rest.
func notifyOpsOfReport(ctx context.Context, reportID, reporter string, in reportInput, p taskParties) {
	reporterName := html.EscapeString(displayNameForUser(ctx, reporter))
	reportedName := html.EscapeString(displayNameForUser(ctx, in.UserID))
	details := "—"
	if in.Details != "" {
		details = html.EscapeString(in.Details)
	}
	safetyEmailOps(
		fmt.Sprintf("[HO:RA] User report: %s", reportReasonLabel(in.ReasonCode)),
		fmt.Sprintf(`<p><strong>%s</strong> reported <strong>%s</strong>.</p>
<ul>
  <li>Reason: %s</li>
  <li>Details: %s</li>
  <li>Task: %s (%s, status %s)</li>
  <li>Report id: %s</li>
</ul>
<p>Review it in the ops panel &rarr; Reports. Apple expects action on reports within 24 hours.</p>`,
			reporterName, reportedName, html.EscapeString(reportReasonLabel(in.ReasonCode)), details,
			html.EscapeString(p.Title), in.TaskID, html.EscapeString(p.Status), reportID))
}

// displayNameForUser resolves a users.id to the name the app shows for them.
func displayNameForUser(ctx context.Context, uid string) string {
	var email string
	_ = db.QueryRow(ctx, `select coalesce(email,'') from public.users where id = $1::uuid`, uid).Scan(&email)
	if email == "" {
		return "A user"
	}
	return resolveDisplayName(ctx, email)
}

// ── POST /safety/block ─────────────────────────────────────────────────────

type blockInput struct {
	UserID string `json:"user_id"`
	TaskID string `json:"task_id"`
}

func blockUserHandler(c *gin.Context) {
	me := c.GetString("uid")
	if me == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var in blockInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	in.UserID, in.TaskID = strings.TrimSpace(in.UserID), strings.TrimSpace(in.TaskID)
	if in.UserID == "" || in.TaskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id and task_id are required"})
		return
	}
	ctx := c.Request.Context()
	parties, err := resolveSafetyTarget(ctx, me, in.UserID, in.TaskID)
	if err != nil {
		writeSafetyTargetError(c, err)
		return
	}

	tag, err := db.Exec(ctx, `
		insert into public.user_blocks (blocker_id, blocked_id, task_id)
		values ($1::uuid, $2::uuid, $3::uuid)
		on conflict (blocker_id, blocked_id) do nothing
	`, me, in.UserID, in.TaskID)
	if err != nil {
		log.Printf("[safety][block] insert blocker=%s blocked=%s: %v", me, in.UserID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if tag.RowsAffected() > 0 {
		writeAudit(ctx, in.TaskID, me, "USER_BLOCKED", "", map[string]any{"blocked_id": in.UserID})
		live := liveTasksBetween(ctx, me, in.UserID)
		notifyOpsOfBlock(ctx, me, in, parties, live)
		log.Printf("[safety][block] blocker=%s blocked=%s task=%s live_tasks=%d", me, in.UserID, in.TaskID, len(live))
		// Read-only chat, server-side. After the response: TalkJS latency
		// must not hold the request, and a TalkJS failure must not undo a
		// block that is already committed — the clients also render the
		// thread read-only from chat_blocked, and the webhook stops pushing.
		go lockChatsBetween(me, in.UserID)
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"message": "Blocked. You won't be matched with each other again, and you can't message each other.",
	})
}

// liveTasksBetween lists open tasks (posted or in progress) on which the two
// are requester and supporter, either way round.
func liveTasksBetween(ctx context.Context, a, b string) []string {
	rows, err := db.Query(ctx, `
		select id::text from public.tasks
		 where status = 'open'
		   and ((requester_id = $1::uuid and assigned_to_id = $2::uuid)
		     or (requester_id = $2::uuid and assigned_to_id = $1::uuid))
	`, a, b)
	if err != nil {
		log.Printf("[safety] live tasks between %s and %s: %v", a, b, err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func notifyOpsOfBlock(ctx context.Context, blocker string, in blockInput, p taskParties, live []string) {
	liveLine := "No task is in progress between them."
	if len(live) > 0 {
		liveLine = fmt.Sprintf("<strong>%d task(s) still open between them</strong>: %s — check on the people involved.",
			len(live), html.EscapeString(strings.Join(live, ", ")))
	}
	safetyEmailOps("[HO:RA] User blocked another user",
		fmt.Sprintf(`<p><strong>%s</strong> blocked <strong>%s</strong> from task %s (%s).</p><p>%s</p>`,
			html.EscapeString(displayNameForUser(ctx, blocker)),
			html.EscapeString(displayNameForUser(ctx, in.UserID)),
			html.EscapeString(p.Title), in.TaskID, liveLine))
}

// lockChatsBetween sets both people to read-only access in every task
// conversation they share. Best-effort, background, logged — see the caller.
func lockChatsBetween(a, b string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, _, ok := talkjsConfigured(); !ok {
		log.Printf("[safety][talkjs] not configured — chat between %s and %s is read-only client-side only", a, b)
		return
	}
	rows, err := db.Query(ctx, `
		select t.id::text, coalesce(ru.email,''), coalesce(su.email,'')
		  from public.tasks t
		  join public.users ru on ru.id = t.requester_id
		  join public.users su on su.id = coalesce(t.assigned_to_id, t.cancelled_assignee_id)
		 where (t.requester_id = $1::uuid and coalesce(t.assigned_to_id, t.cancelled_assignee_id) = $2::uuid)
		    or (t.requester_id = $2::uuid and coalesce(t.assigned_to_id, t.cancelled_assignee_id) = $1::uuid)
	`, a, b)
	if err != nil {
		log.Printf("[safety][talkjs] list shared tasks %s/%s: %v", a, b, err)
		return
	}
	type conv struct{ taskID, requester, supporter string }
	var convs []conv
	for rows.Next() {
		var cv conv
		if rows.Scan(&cv.taskID, &cv.requester, &cv.supporter) == nil {
			convs = append(convs, cv)
		}
	}
	rows.Close()
	for _, cv := range convs {
		exists, err := talkjsConversationExists(ctx, cv.taskID)
		if err != nil || !exists {
			continue // never opened: the clients create it read-only from chat_blocked
		}
		for _, email := range []string{cv.requester, cv.supporter} {
			if email == "" {
				continue
			}
			if err := talkjsSetParticipantAccess(ctx, cv.taskID, email, "Read"); err != nil {
				log.Printf("[safety][talkjs] task=%s read-only failed: %v", cv.taskID, err)
			}
		}
	}
}

// ── Ops: GET /admin/reports, POST /admin/reports/:id/resolve ───────────────

type adminReport struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	ReasonCode   string    `json:"reason_code"`
	ReasonLabel  string    `json:"reason_label"`
	Details      string    `json:"details"`
	TaskID       string    `json:"task_id"`
	TaskTitle    string    `json:"task_title"`
	ReporterID   string    `json:"reporter_id"`
	ReporterName string    `json:"reporter_name"`
	ReporterMail string    `json:"reporter_email"`
	ReportedID   string    `json:"reported_id"`
	ReportedName string    `json:"reported_name"`
	ReportedMail string    `json:"reported_email"`
	// How many reports, from anyone, name this person — the first thing ops
	// asks when deciding whether one report is a pattern.
	ReportedCount int `json:"reported_count"`
	// Whether the reporter has also blocked them.
	Blocked    bool       `json:"blocked"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Resolution string     `json:"resolution,omitempty"`
}

func adminListReports(c *gin.Context) {
	ctx := c.Request.Context()
	onlyOpen := c.DefaultQuery("status", "open") != "all"
	rows, err := db.Query(ctx, `
		select r.id::text, r.created_at, r.reason_code, coalesce(r.details,''),
		       r.task_id::text, coalesce(t.title,''),
		       r.reporter_id::text, coalesce(ru.email,''),
		       r.reported_id::text, coalesce(du.email,''),
		       (select count(*) from public.user_reports x where x.reported_id = r.reported_id),
		       exists (select 1 from public.user_blocks b
		                where b.blocker_id = r.reporter_id and b.blocked_id = r.reported_id),
		       r.resolved_at, coalesce(r.resolution,'')
		  from public.user_reports r
		  join public.tasks t on t.id = r.task_id
		  join public.users ru on ru.id = r.reporter_id
		  join public.users du on du.id = r.reported_id
		 where ($1::boolean = false or r.resolved_at is null)
		 order by r.created_at desc
		 limit 200
	`, onlyOpen)
	if err != nil {
		log.Printf("[admin.reports] list: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()
	out := []adminReport{}
	for rows.Next() {
		var r adminReport
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.ReasonCode, &r.Details, &r.TaskID, &r.TaskTitle,
			&r.ReporterID, &r.ReporterMail, &r.ReportedID, &r.ReportedMail,
			&r.ReportedCount, &r.Blocked, &r.ResolvedAt, &r.Resolution); err != nil {
			log.Printf("[admin.reports] scan: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "scan error"})
			return
		}
		r.ReasonLabel = reportReasonLabel(r.ReasonCode)
		out = append(out, r)
	}
	emails := make([]string, 0, 2*len(out))
	for _, r := range out {
		emails = append(emails, r.ReporterMail, r.ReportedMail)
	}
	names := displayNamesByEmail(ctx, emails)
	for i := range out {
		out[i].ReporterName = names[strings.ToLower(out[i].ReporterMail)]
		out[i].ReportedName = names[strings.ToLower(out[i].ReportedMail)]
	}
	c.JSON(http.StatusOK, gin.H{"reports": out})
}

func adminResolveReport(c *gin.Context) {
	ctx := c.Request.Context()
	id := strings.TrimSpace(c.Param("id"))
	actor := c.GetString("uid")
	var body struct {
		Resolution string `json:"resolution"`
	}
	_ = c.ShouldBindJSON(&body)
	resolution := truncateRunes(strings.TrimSpace(body.Resolution), 500)
	var taskID string
	err := db.QueryRow(ctx, `
		update public.user_reports
		   set resolved_at = now(), resolved_by = $2::uuid, resolution = nullif($3,'')
		 where id = $1::uuid and resolved_at is null
		returning task_id::text
	`, id, actor, resolution).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "message": "No open report with that id."})
		return
	}
	if err != nil {
		log.Printf("[admin.reports] resolve %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	writeAudit(ctx, taskID, actor, "USER_REPORT_RESOLVED", resolution, map[string]any{
		"report_id":   id,
		"admin_email": c.GetString("email"),
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
