package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	notify "hora-auth/internal/notify"
)

// Platform takedown of a task.
//
// A requester cancels their own task (cancelTask); the HO:RA team *removes*
// one that should never have been posted — during the beta, overwhelmingly a
// job at a private residence, which is out of scope. The two are deliberately
// separate statuses: "cancelled" is the requester's decision and shows up in
// their own history as such, "removed" is ours.
//
// Authorization is the same allowlist that guards /ops/* (isOpsAdminEmail),
// checked here in the backend — the webapp's copy of the list only decides
// whether to draw a button.

// removalReasons is the closed set the admin panel's dropdown offers. It is
// mirrored by the tasks_removal_reason_check constraint; keep both in step.
var removalReasons = map[string]struct{}{
	"out_of_scope_private_residence": {},
	"out_of_scope_other":             {},
	"inappropriate":                  {},
	"other":                          {},
}

// requireOpsAdmin gates a route on the admin allowlist. It runs after an auth
// middleware has populated "email" — an unauthenticated caller has no email
// and therefore fails the check as well.
func requireOpsAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isOpsAdminEmail(c.GetString("email")) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin_only"})
			return
		}
		c.Next()
	}
}

// removalNotice returns the requester-facing copy for a removal reason. The
// admin's free-text note is deliberately absent: it is written for the audit
// log, not for the person whose task just disappeared.
func removalNotice(reason, taskTitle string) string {
	switch reason {
	case "out_of_scope_private_residence":
		return fmt.Sprintf(
			"Your task %q was removed because it falls outside this beta's scope "+
				"(public locations only — no private residences). "+
				"Feel free to post it again at a public location!", taskTitle)
	case "out_of_scope_other":
		return fmt.Sprintf(
			"Your task %q was removed because it falls outside this beta's scope "+
				"(short, in-person tasks at public locations). "+
				"Feel free to post it again within scope!", taskTitle)
	case "inappropriate":
		return fmt.Sprintf(
			"Your task %q was removed because it doesn't meet our community guidelines. "+
				"Get in touch if you think this was a mistake.", taskTitle)
	default:
		return fmt.Sprintf(
			"Your task %q was removed by the HO:RA team. "+
				"Get in touch if you think this was a mistake — you're welcome to post again.", taskTitle)
	}
}

// POST /admin/tasks/:id/remove
//
// Idempotent: removing an already-removed task is a 200 no-op, so a double
// click or a retried request never produces a second round of notifications.
func adminRemoveTask(c *gin.Context) {
	taskID := c.Param("id")
	actorUID := c.GetString("uid")
	actorEmail := c.GetString("email")
	if actorUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	// Body is optional — a removal with no stated reason is recorded as "other".
	var in struct {
		Reason string `json:"reason"`
		Note   string `json:"note"`
	}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&in)
	}
	reason := strings.TrimSpace(strings.ToLower(in.Reason))
	if reason == "" {
		reason = "other"
	}
	if _, ok := removalReasons[reason]; !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_reason"})
		return
	}
	note := strings.TrimSpace(in.Note)

	ctx := c.Request.Context()

	var status, title string
	var requesterID, requesterEmail, assignedTo string
	var assignedToID *string
	if err := db.QueryRow(ctx, `
		select status, coalesce(title,''), requester_id::text, coalesce(requester,''),
		       assigned_to_id::text, coalesce(assigned_to,'')
		from public.tasks
		where id = $1::uuid
	`, taskID).Scan(&status, &title, &requesterID, &requesterEmail, &assignedToID, &assignedTo); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	if status == "removed" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "status": "removed", "already_removed": true})
		return
	}
	if status != "open" {
		// completed and cancelled tasks are history — taking one down would
		// rewrite a record both parties have already acted on.
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "task_not_removable",
			"message": fmt.Sprintf("A %s task cannot be removed.", status),
		})
		return
	}

	// A supporter mid-shift would otherwise keep a running timer on a task that
	// no longer exists, and cancelTask refuses outright rather than closing one.
	// An admin takedown can't be blocked by the supporter's clock, so close it.
	var closedSessions int64
	if tag, err := db.Exec(ctx, `
		update public.worklogs set end_at = now()
		where task_id = $1::uuid and end_at is null
	`, taskID); err == nil {
		closedSessions = tag.RowsAffected()
	} else {
		log.Printf("[admin.remove] closing open worklogs failed task=%s err=%v", taskID, err)
	}

	// The WHERE re-tests status, so two admins clicking at once (or a race with
	// a completion) serialize on the row and only one of them notifies.
	tag, err := db.Exec(ctx, `
		update public.tasks
		set status = 'removed',
		    removed_at = now(),
		    removal_reason = $2,
		    removal_note = nullif($3, ''),
		    assigned_to_id = null,
		    assigned_to = ''
		where id = $1::uuid and status = 'open'
	`, taskID, reason, note)
	if err != nil {
		log.Printf("[admin.remove][update] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if tag.RowsAffected() == 0 {
		// Somebody else moved the task between the read and the update.
		var now string
		_ = db.QueryRow(ctx, `select status from public.tasks where id=$1::uuid`, taskID).Scan(&now)
		if now == "removed" {
			c.JSON(http.StatusOK, gin.H{"ok": true, "status": "removed", "already_removed": true})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_not_removable", "status": now})
		return
	}

	// audit_logs is the existing table for exactly this (job_id is the task id);
	// it had no writer until now. Best-effort: a failed audit insert must not
	// undo a takedown that already happened.
	meta := map[string]any{
		"admin_email":             actorEmail,
		"note":                    note,
		"previous_assignee_id":    derefOrEmpty(assignedToID),
		"previous_assignee_email": assignedTo,
		"closed_worklog_sessions": closedSessions,
	}
	metaJSON, _ := json.Marshal(meta)
	if _, err := db.Exec(ctx, `
		insert into public.audit_logs (job_id, actor_id, action, reason, meta)
		values ($1::uuid, $2::uuid, 'TASK_REMOVED', $3, $4::jsonb)
	`, taskID, actorUID, reason, string(metaJSON)); err != nil {
		log.Printf("[admin.remove][audit] task=%s err=%v", taskID, err)
	}

	// Notifications are read from the values captured *before* the update:
	// notifyAssignee resolves the supporter from assigned_to_id, which this
	// handler has just cleared, so the supporter has to be notified directly.
	notifyUser(ctx, requesterID, requesterEmail, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "TASK_REMOVED",
		Title:     "Task removed",
		Body:      removalNotice(reason, title),
		TaskTitle: title,
	})
	if assignedToID != nil && *assignedToID != "" {
		notifyUser(ctx, *assignedToID, assignedTo, notify.CreateNotificationInput{
			TaskID:    taskID,
			Type:      "TASK_REMOVED",
			Title:     "Task no longer available",
			Body:      "This task is no longer available — thanks for your interest, more tasks are coming.",
			TaskTitle: title,
		})
	}

	log.Printf("[admin.remove] task=%s by=%s reason=%s assignee=%q closedSessions=%d",
		taskID, actorEmail, reason, assignedTo, closedSessions)

	c.JSON(http.StatusOK, gin.H{
		"ok":                 true,
		"status":             "removed",
		"removal_reason":     reason,
		"supporter_notified": assignedToID != nil && *assignedToID != "",
	})
}

// notifyUser writes the in-app row and fires email + push for one explicit
// recipient. The notifyRequester*/notifyAssignee helpers re-read the task to
// find their recipient, which does not work once the assignment is cleared.
func notifyUser(ctx context.Context, uid, email string, in notify.CreateNotificationInput) {
	if uid == "" {
		return
	}
	in.DB = sqldb
	in.UserID = uid
	in.SendEmail = email != ""
	in.EmailTo = email
	if err := notify.Create(ctx, in); err != nil {
		log.Printf("[notify][ERROR] %s user=%s: %v", in.Type, uid, err)
	}
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
