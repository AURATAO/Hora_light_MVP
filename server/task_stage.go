package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Where an accepted task stands, in the server's words, and the one rule
// about taking two at once (Aura 2026-11-17).
//
// STAGE LABEL. "My jobs" showed every active task as "Assigned", whether it
// was just accepted, on the way or being worked. Task.stage_label says which,
// and the app shows it as sent, so a stage can be reworded or added without
// an app release.
//
// OVERLAP. A supporter may hold several accepted tasks (one now, one at
// 5 PM). What they may not do is accept a task whose time overlaps one they
// already hold: the second customer would wait for someone who is busy. The
// dispatch push has skipped busy supporters since D-24; accepting from the
// list did not, so the two disagreed. Same window, same rule, in one place
// (overlappingTaskTitle). Never a cap on how many tasks someone may hold.

const (
	stageAccepted = "Accepted"
	stageOnTheWay = "On my way"
	stageWorking  = "Working"
)

// taskStageLabels words the stage of each task this person is assigned to.
// Keyed by task id; a task not in the map has no label. Errors yield an
// empty map: the label is an extra, and the list must not fail over it.
func taskStageLabels(ctx context.Context, meUID, meEmail string, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	rows, err := db.Query(ctx, `
		select t.id::text, t.enroute_at is not null,
		       exists (select 1 from public.worklogs w
		                where w.task_id = t.id and w."user" = $3 and w.end_at is null),
		       coalesce(t.is_immediate, false), t.scheduled_at
		  from public.tasks t
		 where t.id::text = any($1::text[]) and t.assigned_to_id = $2::uuid and t.status = 'open'`,
		ids, meUID, meEmail)
	if err != nil {
		log.Printf("[stage] labels unreadable: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var enroute, working, immediate bool
		var scheduled *time.Time
		if err := rows.Scan(&id, &enroute, &working, &immediate, &scheduled); err != nil {
			return out
		}
		out[id] = stageLabel(enroute, working, immediate, scheduled, time.Now())
	}
	return out
}

// stageLabel: working beats on-the-way beats accepted. A task booked for
// later says when, so it is not mistaken for the one to start now.
func stageLabel(enroute, working, immediate bool, scheduled *time.Time, now time.Time) string {
	switch {
	case working:
		return stageWorking
	case enroute:
		return stageOnTheWay
	case !immediate && scheduled != nil && scheduled.After(now):
		return fmt.Sprintf("%s · starts %s", stageAccepted, formatStageTime(*scheduled, now))
	}
	return stageAccepted
}

// formatStageTime: "5:30 PM" today, "Thu 5:30 PM" otherwise, New York time
// (the service area's clock, like every other time the product words).
func formatStageTime(t, now time.Time) string {
	loc, err := time.LoadLocation(Billing.SurgeTimezone)
	if err != nil {
		loc = time.UTC
	}
	t, now = t.In(loc), now.In(loc)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("3:04 PM")
	}
	return t.Format("Mon 3:04 PM")
}

// overlappingTaskTitle is the title of a task this supporter already holds
// whose time overlaps the one they are about to accept; "" when none does.
// The window and the held-task window are dispatch's (dispatchWindow and the
// "no overlapping task" clause of eligibleSupporters), so a supporter the
// push would have skipped is exactly one the accept refuses.
func overlappingTaskTitle(ctx context.Context, meUID, taskID string) (string, error) {
	t, err := loadDispatchTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	winStart, winEnd := dispatchWindow(t, time.Now())
	var title string
	err = db.QueryRow(ctx, `
		select coalesce(o.title, '')
		  from public.tasks o
		 where o.assigned_to_id = $1::uuid and o.status = 'open' and o.id <> $2::uuid
		   and (not (coalesce(o.is_immediate, false) or o.scheduled_at is null)
		        or coalesce(o.accepted_at, o.created_at) > now() - interval '1 day')
		   and tstzrange(
		         case when coalesce(o.is_immediate, false) or o.scheduled_at is null then now() else o.scheduled_at end,
		         (case when coalesce(o.is_immediate, false) or o.scheduled_at is null then now() else o.scheduled_at end)
		           + make_interval(mins => greatest(coalesce(o.estimated_minutes, 0), 30) + 30))
		       && tstzrange($3::timestamptz, $4::timestamptz)
		 order by o.accepted_at desc nulls last
		 limit 1`, meUID, taskID, winStart, winEnd).Scan(&title)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if title == "" {
		title = "another task"
	}
	return title, nil
}

// acceptOverlapStep runs inside acceptTask before the claim (false = the
// response is written). The App Review sandbox is exempt: its walkthrough
// accepts several demo tasks in a row with one login. An unreadable check
// lets the accept through: this is a courtesy to the second customer, not a
// safety wall, and it must not take accepting down with it.
func acceptOverlapStep(c *gin.Context, taskID, meUID string) bool {
	ctx := c.Request.Context()
	if sandbox, err := isSandboxUser(ctx, meUID); err == nil && sandbox {
		return true
	}
	title, err := overlappingTaskTitle(ctx, meUID, taskID)
	if err != nil {
		log.Printf("[accept-overlap] task=%s supporter=%s: %v", taskID, meUID, err)
		return true
	}
	if title == "" {
		return true
	}
	c.JSON(http.StatusConflict, gin.H{
		"error":   "task_overlap",
		"message": fmt.Sprintf("This overlaps with “%s”, which you've already accepted. Finish that one first, or message HO:RA if you can't do it.", title),
	})
	return false
}
