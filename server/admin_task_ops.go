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

// The three ops-panel task actions: force-complete, admin cancel, adjust time.
//
// These existed as SECURITY DEFINER Postgres functions (force_complete,
// cancel_task, adjust_time) that the /ops/* routes invoked with
// `select public.force_complete($1)`. They never once worked. Each opens with
// `perform public.assert_ops_admin()`, which reads auth.jwt() — and Go connects
// as the `postgres` role over pgx, where there is no JWT at all. The guard
// raised "not authorized" every time, the handler mapped it to a 500 "db error",
// and the ops panel's Adjust / Force / Cancel buttons have been dead for the
// project's entire life. Production proves it: audit_logs holds nine
// TASK_REMOVED rows and not a single FORCE_COMPLETED, CANCELLED or
// TIME_ADJUSTED.
//
// It stayed invisible because a failed write is silent — nothing is written, so
// nothing looks wrong. Which is why the audit row is the acceptance test for
// each of these handlers, not an afterthought: it is the only artifact that
// distinguishes "it worked" from "it did nothing".
//
// Ported here to the shape adminRemoveTask and adminReassignTask already use:
// requireOpsAdmin() for authorization, the acting admin from the session rather
// than auth.uid(), the audit row written by the handler, and the status re-test
// inside the UPDATE so a concurrent completion or cancellation can't be
// overwritten. The SQL functions are dropped in the accompanying migration.

// adminTask is the slice of a task these handlers need, read once up front.
type adminTask struct {
	Status         string
	Title          string
	RequesterID    string
	RequesterEmail string
	AssigneeID     *string
	AssigneeEmail  string
}

func loadAdminTask(ctx context.Context, taskID string) (adminTask, error) {
	var t adminTask
	err := db.QueryRow(ctx, `
		select status, coalesce(title,''), requester_id::text, coalesce(requester,''),
		       assigned_to_id::text, coalesce(assigned_to,'')
		from public.tasks
		where id = $1::uuid
	`, taskID).Scan(&t.Status, &t.Title, &t.RequesterID, &t.RequesterEmail,
		&t.AssigneeID, &t.AssigneeEmail)
	return t, err
}

// closeOpenWorklogs stops any running timer. Both force-complete and cancel end
// the task underneath a supporter who may still be on the clock; leaving the
// session open would keep it accruing against a task that is finished.
func closeOpenWorklogs(ctx context.Context, taskID string) int64 {
	tag, err := db.Exec(ctx, `
		update public.worklogs set end_at = now(), updated_at = now()
		where task_id = $1::uuid and end_at is null
	`, taskID)
	if err != nil {
		log.Printf("[admin.ops] closing open worklogs failed task=%s err=%v", taskID, err)
		return 0
	}
	return tag.RowsAffected()
}

// writeAudit records the action. Best-effort by design — a failed audit insert
// must not undo work that already happened — but it is logged loudly, because
// a missing audit row is exactly the signal that went unnoticed for months.
func writeAudit(ctx context.Context, taskID, actorUID, action, reason string, meta map[string]any) {
	metaJSON, _ := json.Marshal(meta)
	if _, err := db.Exec(ctx, `
		insert into public.audit_logs (job_id, actor_id, action, reason, meta)
		values ($1::uuid, $2::uuid, $3, nullif($4,''), $5::jsonb)
	`, taskID, actorUID, action, reason, string(metaJSON)); err != nil {
		log.Printf("[admin.ops][audit][ERROR] action=%s task=%s actor=%s err=%v",
			action, taskID, actorUID, err)
	}
}

// POST /admin/tasks/:id/force-complete
//
// The escape hatch for a task that is finished in reality but stuck open —
// the supporter never clocked out, or never uploaded the completion photo
// completeTask insists on. It deliberately skips that photo requirement, which
// is the whole reason it exists as a separate action.
func adminForceCompleteTask(c *gin.Context) {
	taskID := c.Param("id")
	actorUID := c.GetString("uid")
	actorEmail := c.GetString("email")
	if actorUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if t.Status == "completed" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "status": "completed", "already_completed": true})
		return
	}
	if t.Status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "task_not_completable",
			"status":  t.Status,
			"message": fmt.Sprintf("A %s task cannot be force-completed.", t.Status),
		})
		return
	}

	closed := closeOpenWorklogs(ctx, taskID)

	// completed_at is set here but was not by the old SQL function — one more
	// reason nothing downstream could tell these apart from a normal completion.
	tag, err := db.Exec(ctx, `
		update public.tasks
		set status = 'completed', completed_at = now()
		where id = $1::uuid and status = 'open'
	`, taskID)
	if err != nil {
		log.Printf("[admin.force-complete][update] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "task_changed",
			"message": "This task changed while you were completing it. Reload the feed and try again.",
		})
		return
	}

	totalMin, _ := totalClosedMinutes(ctx, taskID)
	totalCents := calcTaskCostCents(ctx, taskID, totalMin)

	writeAudit(ctx, taskID, actorUID, "FORCE_COMPLETED", "", map[string]any{
		"admin_email":             actorEmail,
		"closed_worklog_sessions": closed,
		"total_minutes":           totalMin,
		"final_cost_cents":        totalCents,
		"supporter_email":         t.AssigneeEmail,
	})

	// Both parties are told, same as a normal completion. An admin closing
	// someone's task in silence is how a requester discovers it from a receipt.
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:        taskID,
		Type:          "COMPLETED",
		Title:         "Your task has been completed",
		Body:          "The HO:RA team marked this task complete. Please leave a rating when you have a moment.",
		SupporterName: displayName(t.AssigneeEmail),
		TaskTitle:     t.Title,
		TotalLogged:   formatMinutes(totalMin),
		FinalCost:     fmt.Sprintf("$%.2f", float64(totalCents)/100.0),
	})
	if t.AssigneeID != nil && *t.AssigneeID != "" {
		notifyUser(ctx, *t.AssigneeID, t.AssigneeEmail, notify.CreateNotificationInput{
			TaskID:      taskID,
			Type:        "COMPLETED_SUPPORTER",
			Title:       "Task marked complete",
			Body:        "The HO:RA team marked this task complete. Thanks for your work!",
			TaskTitle:   t.Title,
			TotalLogged: formatMinutes(totalMin),
		})
	}

	log.Printf("[admin.force-complete] task=%s by=%s closedSessions=%d totalMin=%d",
		taskID, actorEmail, closed, totalMin)

	c.JSON(http.StatusOK, gin.H{
		"ok":                      true,
		"status":                  "completed",
		"closed_worklog_sessions": closed,
		"total_minutes":           totalMin,
	})
}

// POST /admin/tasks/:id/cancel  { "reason": "..." }
//
// An admin cancelling on the requester's behalf — they phoned in, or the task
// is stale. Distinct from adminRemoveTask (a platform takedown, which the
// requester is told was our decision) and from the requester's own cancelTask.
// The resulting status is 'cancelled' either way, so the requester's history
// reads the same as if they had cancelled it themselves.
func adminCancelTaskHandler(c *gin.Context) {
	taskID := c.Param("id")
	actorUID := c.GetString("uid")
	actorEmail := c.GetString("email")
	if actorUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var in struct {
		Reason string `json:"reason"`
	}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&in)
	}
	reason := strings.TrimSpace(in.Reason)

	ctx := c.Request.Context()

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if t.Status == "cancelled" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "status": "cancelled", "already_cancelled": true})
		return
	}
	if t.Status != "open" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "task_not_cancellable",
			"status":  t.Status,
			"message": fmt.Sprintf("A %s task cannot be cancelled.", t.Status),
		})
		return
	}

	closed := closeOpenWorklogs(ctx, taskID)

	tag, err := db.Exec(ctx, `
		update public.tasks
		set status = 'cancelled', cancelled_at = now(), cancel_reason = nullif($2,'')
		where id = $1::uuid and status = 'open'
	`, taskID, reason)
	if err != nil {
		log.Printf("[admin.cancel][update] task=%s err=%v", taskID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "task_changed",
			"message": "This task changed while you were cancelling it. Reload the feed and try again.",
		})
		return
	}

	writeAudit(ctx, taskID, actorUID, "CANCELLED", reason, map[string]any{
		"admin_email":             actorEmail,
		"closed_worklog_sessions": closed,
		"supporter_email":         t.AssigneeEmail,
	})

	body := fmt.Sprintf("Your task %q has been cancelled by the HO:RA team.", t.Title)
	if reason != "" {
		body = fmt.Sprintf("Your task %q has been cancelled by the HO:RA team: %s", t.Title, reason)
	}
	notifyUser(ctx, t.RequesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID:    taskID,
		Type:      "CANCELLED",
		Title:     "Task cancelled",
		Body:      body,
		TaskTitle: t.Title,
	})
	if t.AssigneeID != nil && *t.AssigneeID != "" {
		notifyUser(ctx, *t.AssigneeID, t.AssigneeEmail, notify.CreateNotificationInput{
			TaskID:    taskID,
			Type:      "CANCELLED",
			Title:     "Task cancelled",
			Body:      fmt.Sprintf("%q has been cancelled. Thanks for your time.", t.Title),
			TaskTitle: t.Title,
		})
	}

	log.Printf("[admin.cancel] task=%s by=%s reason=%q closedSessions=%d",
		taskID, actorEmail, reason, closed)

	c.JSON(http.StatusOK, gin.H{
		"ok":                      true,
		"status":                  "cancelled",
		"closed_worklog_sessions": closed,
	})
}

// POST /admin/tasks/:id/adjust-time  { "delta": 5 }
//
// Corrects the most recent work session by a number of minutes, positive or
// negative — a supporter who forgot to clock out, or clocked in early.
//
// Allowed on completed tasks as well as open ones: a billing correction is
// usually noticed *after* the fact. Not allowed on cancelled or removed tasks,
// where logged time no longer means anything.
func adminAdjustTime(c *gin.Context) {
	taskID := c.Param("id")
	actorUID := c.GetString("uid")
	actorEmail := c.GetString("email")
	if actorUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var in struct {
		Delta int `json:"delta"`
	}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&in)
	}
	if in.Delta == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_delta",
			"message": "Provide a non-zero number of minutes.",
		})
		return
	}

	ctx := c.Request.Context()

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if t.Status != "open" && t.Status != "completed" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "task_not_adjustable",
			"status":  t.Status,
			"message": fmt.Sprintf("Logged time on a %s task cannot be adjusted.", t.Status),
		})
		return
	}

	// One statement rather than select-then-update: picking the session and
	// moving it in the same UPDATE closes the window in which the supporter
	// could clock out (or start a new session) between the two.
	//
	// The end_at > start_at test is a guard the SQL function lacked — a negative
	// delta larger than the session would otherwise push end_at behind start_at
	// and produce negative billable minutes. Failing it matches zero rows,
	// which is reported below rather than silently applied.
	var worklogID string
	var newEnd, startAt string
	err = db.QueryRow(ctx, `
		update public.worklogs w
		set end_at = coalesce(w.end_at, now()) + make_interval(mins => $2),
		    updated_at = now()
		where w.id = (
		        select id from public.worklogs
		        where task_id = $1::uuid
		        order by coalesce(end_at, start_at) desc, id desc
		        limit 1
		      )
		  and coalesce(w.end_at, now()) + make_interval(mins => $2) > w.start_at
		returning w.id::text, w.start_at::text, w.end_at::text
	`, taskID, in.Delta).Scan(&worklogID, &startAt, &newEnd)
	if err != nil {
		// No row matched: either the task has no worklog at all, or the guard
		// rejected the adjustment. Distinguish them so the admin knows which.
		var hasWorklog bool
		_ = db.QueryRow(ctx,
			`select exists(select 1 from public.worklogs where task_id=$1::uuid)`, taskID).Scan(&hasWorklog)
		if !hasWorklog {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "no_worklog",
				"message": "This task has no work session to adjust — nobody has clocked in.",
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "adjustment_out_of_range",
			"message": fmt.Sprintf(
				"Subtracting %d minutes would end the session before it started.", -in.Delta),
		})
		return
	}

	totalMin, _ := totalClosedMinutes(ctx, taskID)

	writeAudit(ctx, taskID, actorUID, "TIME_ADJUSTED", "", map[string]any{
		"admin_email":         actorEmail,
		"delta_minutes":       in.Delta,
		"worklog_id":          worklogID,
		"total_minutes_after": totalMin,
	})

	// No notification: this is an internal correction to logged time, and the
	// requester sees the resulting total on the task itself.

	log.Printf("[admin.adjust-time] task=%s by=%s delta=%d worklog=%s totalMin=%d",
		taskID, actorEmail, in.Delta, worklogID, totalMin)

	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"delta_minutes": in.Delta,
		"worklog_id":    worklogID,
		"total_minutes": totalMin,
	})
}

// formatMinutes renders a duration the way the completion email does.
func formatMinutes(total int) string {
	if h := total / 60; h > 0 {
		return fmt.Sprintf("%dh %dmin", h, total%60)
	}
	return fmt.Sprintf("%d min", total)
}
