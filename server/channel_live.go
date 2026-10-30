package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"hora-auth/internal/channel"
)

// chatLive answers the chat's "where is he?" with the same facts the app's
// live map gets from GET /tasks/:id/live (live_tracking.go): the latest
// GPS ping, its freshness, the distance to the task's place, and the clock.
// Requester only, open and assigned tasks only — the same boundary as the
// endpoint; anything else is an error and the chat does not answer.
type chatLive struct{ sqlDB *sql.DB }

func (l *chatLive) LiveStatus(ctx context.Context, userID, taskID string) (channel.LiveStatus, error) {
	var requesterID, status string
	var assignedToID *string
	var jobLat, jobLng *float64
	var enrouteAt *time.Time
	if err := l.sqlDB.QueryRowContext(ctx, `
		select requester_id::text, status, assigned_to_id::text, job_lat, job_lng, enroute_at
		  from public.tasks where id = $1::uuid`, taskID).Scan(&requesterID, &status, &assignedToID, &jobLat, &jobLng, &enrouteAt); err != nil {
		return channel.LiveStatus{}, fmt.Errorf("live status: %w", err)
	}
	if requesterID != userID || status != "open" || assignedToID == nil {
		return channel.LiveStatus{}, fmt.Errorf("live status: not this person's active task")
	}
	var clockedIn bool
	if err := l.sqlDB.QueryRowContext(ctx, `
		select exists (select 1 from public.worklogs where task_id = $1::uuid and end_at is null)`, taskID).Scan(&clockedIn); err != nil {
		return channel.LiveStatus{}, fmt.Errorf("live status: clock: %w", err)
	}
	var lat, lng *float64
	var seen *time.Time
	err := l.sqlDB.QueryRowContext(ctx, `
		select lat, lng, created_at from public.task_gps_pings
		 where task_id = $1::uuid and user_id = $2::uuid
		 order by created_at desc limit 1`, taskID, *assignedToID).Scan(&lat, &lng, &seen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return channel.LiveStatus{}, fmt.Errorf("live status: ping: %w", err)
	}
	fresh := seen != nil && time.Since(*seen) <= livePingStaleAfter
	var distance *float64
	if lat != nil && lng != nil && jobLat != nil && jobLng != nil {
		d := haversineMeters(*lat, *lng, *jobLat, *jobLng)
		distance = &d
	}
	state := liveState(distance, clockedIn, fresh)
	out := channel.LiveStatus{State: channel.LiveState(state)}
	if state != liveStateUnavailable && fresh {
		// Clocked in, the state is "working" whatever the GPS says; a
		// distance goes with it only from a fresh fix — a stale one is not
		// a position (Arbiter 2026-10-30: while working the supporter is
		// locatable, as on the app's map, but never from an old dot).
		out.DistanceMeters = distance
	}
	if seen != nil {
		out.LastSeen = *seen
	}
	if fresh && lat != nil && lng != nil {
		// The dot the requester's live map shows; never a stale one — and
		// never in the minutes after a clock-out, when the last fix is
		// still "fresh" but the supporter has finished (review r3).
		clockedOut := false
		if !clockedIn {
			if err := l.sqlDB.QueryRowContext(ctx, `select exists (select 1 from public.worklogs where task_id = $1::uuid)`, taskID).Scan(&clockedOut); err != nil {
				return channel.LiveStatus{}, fmt.Errorf("live status: worklogs: %w", err)
			}
		}
		if !clockedOut {
			out.Lat, out.Lng = lat, lng
		}
	}
	if clockedIn {
		out.WorkedMinutes = liveLoggedMinutes(ctx, taskID)
	}
	if state == liveStateUnavailable && seen == nil {
		if enrouteAt != nil {
			out.SetOffNoFix = true
		} else {
			// Nothing has happened yet: no "On my way", no ping, and never
			// clocked in (a closed session means they were there).
			var worked bool
			if err := l.sqlDB.QueryRowContext(ctx, `select exists (select 1 from public.worklogs where task_id = $1::uuid)`, taskID).Scan(&worked); err != nil {
				return channel.LiveStatus{}, fmt.Errorf("live status: worklogs: %w", err)
			}
			out.NotYetOnTheWay = !worked
		}
	}
	return out, nil
}
