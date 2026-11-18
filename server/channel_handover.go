package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"hora-auth/internal/channel"
)

// chatHandOvers finds the supporter drop-off notices a texting customer
// received (internal/channel/handover.go), so the chat can tell a reply to
// one from anything else.
//
// Nothing new is stored: a notice is the ops action's own audit row (an
// admin cancel with cause supporter_dropped, or a reassign) together with
// the message the chat sent for that task to this thread within minutes of
// it (status.go stamps it with the task id). No row means no notice — one
// that never reached the thread (WhatsApp's 24-hour window) is never
// answered as if it had. The copy is never matched: it belongs to
// admin_task_ops.go and admin_reassign.go and may change.
type chatHandOvers struct{ sqlDB *sql.DB }

// handOverSendWindow: how soon after the ops action the chat's message
// must follow to be its notice (it is sent from the notification hook, off
// the request, within seconds).
const handOverSendWindow = "5 minutes"

func (h *chatHandOvers) HandOverNotices(ctx context.Context, userID string, ch channel.Channel, address string, since time.Time) ([]channel.HandOverNotice, error) {
	rows, err := h.sqlDB.QueryContext(ctx, `
		SELECT a.action, COALESCE(a.reason, ''), t.id::text, COALESCE(t.title, ''), t.status,
		       m.body, m.created_at,
		       COALESCE(p.name, ''),
		       COALESCE(t.category, ''), COALESCE(t.description, ''), COALESCE(t.location_text, ''),
		       COALESCE(t.estimated_minutes, 0), COALESCE(t.prepay_amount_cents, 0), t.scheduled_at,
		       -- answered: a later message of ours tagged with the task (the
		       -- decline's), or any newer task (booked again, or something else)
		       EXISTS (SELECT 1 FROM public.channel_messages m2
		                WHERE m2.channel = $2 AND m2.address = $3 AND m2.direction = 'outbound'
		                  AND m2.task_id = t.id AND m2.created_at > m.created_at AND m2.id <> m.id)
		       OR EXISTS (SELECT 1 FROM public.tasks t2
		                   WHERE t2.requester_id = t.requester_id AND t2.created_at > m.created_at)
		  FROM public.audit_logs a
		  JOIN public.tasks t ON t.id = a.job_id AND t.requester_id = $1::uuid
		  JOIN LATERAL (
		        SELECT mm.id, COALESCE(mm.body, '') AS body, mm.created_at
		          FROM public.channel_messages mm
		         WHERE mm.channel = $2 AND mm.address = $3 AND mm.direction = 'outbound'
		           AND NOT COALESCE(mm.relay, false)
		           AND mm.task_id = a.job_id
		           AND mm.created_at >= a.created_at
		           AND mm.created_at < a.created_at + $5::interval
		         ORDER BY mm.created_at
		         LIMIT 1) m ON true
		  LEFT JOIN public.profiles p ON p.id = t.assigned_to_id
		 WHERE a.created_at >= $4
		   AND ((a.action = 'CANCELLED' AND a.meta->>'cause' = 'supporter_dropped')
		        OR a.action = 'TASK_REASSIGNED')
		 ORDER BY m.created_at DESC
		 LIMIT 5`, userID, string(ch), address, since, handOverSendWindow)
	if err != nil {
		return nil, fmt.Errorf("hand-over notices: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("[channel] hand-over notices: close rows: %v", cerr)
		}
	}()
	var out []channel.HandOverNotice
	for rows.Next() {
		var n channel.HandOverNotice
		var action, reason, status, loc string
		var sched sql.NullTime
		var answered bool
		if err := rows.Scan(&action, &reason, &n.TaskID, &n.Title, &status, &n.Text, &n.SentAt, &n.Supporter,
			&n.Category, &n.Description, &loc, &n.Minutes, &n.BudgetCents, &sched, &answered); err != nil {
			return nil, fmt.Errorf("hand-over notices: %w", err)
		}
		switch action {
		case "CANCELLED":
			n.Kind = channel.HandOverCancelled
			n.Closed = answered
			n.Supporter = "" // the one who dropped out: never named
			if stops := taskStops(loc); len(stops) > 0 {
				n.Location1 = stops[0]
				if len(stops) > 1 {
					n.Location2 = stops[len(stops)-1]
				}
			}
			if sched.Valid {
				t := sched.Time
				n.ScheduledAt = &t
			}
		default:
			n.Kind = channel.HandOverReassigned
			// "swap_voided": the supporter before had started, and their
			// time was taken off the task (admin_reassign.go).
			n.Started = reason == "swap_voided"
			n.Closed = status != "open"
			n.Category, n.Description, n.Minutes, n.BudgetCents = "", "", 0, 0
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
