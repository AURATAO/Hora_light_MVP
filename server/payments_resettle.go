package main

// A completed task whose settlement did not finish.
//
// completeTask answers the browser as soon as the task is `completed` and
// runs the money — capture, balance charge, payouts, receipt — on its own
// goroutine. If the process dies in those seconds (a deploy lands, the
// container is replaced), the task is `completed` and nothing in-request is
// left to notice. Before the chain was detached the same restart orphaned
// it just the same; the only difference is that a browser then saw an
// error. This sweep is what makes the detached version safe. It knows two
// shapes, and treats them differently because only one can be redone:
//
//	hold still live    status completed, completed_at old enough that a
//	                   healthy chain would long be done, the task payment
//	                   still requires_auth/authorized. The chain died BEFORE
//	                   the capture. Nothing has moved, so the same
//	                   settlement is run again from the task's own row
//	                   (settleCompletedTask, under its per-task lock).
//
//	captured, not      the task payment is captured but tasks.settled_total_cents
//	settled            is NULL — recordTaskSettlement, the last step of a
//	                   settlement, never ran. The chain died AFTER the
//	                   capture: a payout, the balance charge or the receipt
//	                   may be missing, and settleTaskPayment cannot resume
//	                   from the middle (it starts from the live hold, and
//	                   there is none). Re-running would be guesswork with
//	                   money, so this shape is RECORDED and ESCALATED, once:
//	                   an audit row (SETTLEMENT_INCOMPLETE) and an ops email
//	                   naming what to check. Ops reconcile against Stripe by
//	                   hand (payments runbook, entry 7).
//
// Why re-running the first shape is safe: settleCompletedTask takes the
// per-task advisory lock (advisoryLockTaskSettle), so a chain still running
// — however slowly — and the sweep never overlap, which is what would turn
// Stripe's idempotent capture replay into two balance charges. A hold that
// Stripe already captured while the row says otherwise cannot happen
// without an overlap; if it ever did, the second capture is refused into
// `capture_failed` plus the ops email — honest, not silent.
//
// Bounded: tasks completed within the last 7 days. Older than that the hold
// has expired at Stripe anyway, and a backlog from before this sweep
// existed must not be charged by a cron on its first tick; those are
// counted in a log line and left for a person. (Production had none when
// this shipped: 14 completed tasks, all captured and settled.)
//
// S-11: the actor on the rows the sweep writes is the system actor
// (orSystemActor("")), never a client value and never the supporter — a
// background job acted, not a person.

import (
	"context"
	"fmt"
	"log"
	"time"
)

// resettleAfter: how old a completed task must be before its unfinished
// settlement is read as abandoned rather than in flight. The chain takes
// ~15 s on staging; Stripe's own retries can stretch it. Ten minutes is far
// past both.
const resettleAfter = 10 * time.Minute

// resettleWindow: how far back the sweep looks. Stripe authorizations live
// about seven days; past that there is nothing to capture, only to report.
const resettleWindow = 7 * 24 * time.Hour

// sweepUnsettledCompletions runs one pass (watchUnsettledCompletions).
// Bounded per pass; the next tick takes the rest.
func sweepUnsettledCompletions(ctx context.Context) {
	if !paymentsEnabled() {
		return
	}
	// No deadline on the pass: a settlement must not be cut mid-capture by
	// a clock of ours (the same rule as completeTask's chain). Each query
	// and provider call bounds itself; a hit is settled under the per-task
	// lock, so a slow pass and the next tick cannot overlap on one task.
	resettleLiveHolds(ctx)
	flagIncompleteSettlements(ctx)
	countStaleOrphans(ctx)
}

type resettleHit struct {
	taskID                    string
	receiptCents, budgetCents int
}

// resettleLiveHolds: shape one. Settlement is run again from the row.
func resettleLiveHolds(ctx context.Context) {
	rows, err := db.Query(ctx, `
		select t.id::text, coalesce(t.receipt_amount_cents, 0), coalesce(t.shopping_budget_approved_cents, 0)
		  from public.tasks t
		 where t.status = 'completed'
		   and t.completed_at < now() - ($1::int * interval '1 second')
		   and t.completed_at > now() - ($2::int * interval '1 second')
		   and exists (
		         select 1 from public.payments p
		          where p.task_id = t.id and p.kind = $3
		            and p.status in ('requires_auth', 'authorized'))
		 order by t.completed_at asc
		 limit 20
	`, int(resettleAfter.Seconds()), int(resettleWindow.Seconds()), paymentKindTaskPayment)
	if err != nil {
		log.Printf("[payments][resettle] query: %v", err)
		return
	}
	var hits []resettleHit
	for rows.Next() {
		var h resettleHit
		if err := rows.Scan(&h.taskID, &h.receiptCents, &h.budgetCents); err != nil {
			log.Printf("[payments][resettle] scan: %v", err)
			rows.Close()
			return
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("[payments][resettle] rows: %v", err)
		return
	}
	for _, h := range hits {
		resettleOne(ctx, h)
	}
}

// resettleOne settles one orphan, with its own recover so one bad task
// cannot stop the pass or starve the tasks behind it — and so a panic here
// is mailed, not just logged (a settlement that vanished quietly is the one
// failure the payments code promises never happens).
func resettleOne(ctx context.Context, h resettleHit) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[payments][resettle][PANIC] task=%s: %T", h.taskID, r)
			emailOpsAdmins("[HO:RA] Settlement crashed — check task "+h.taskID,
				fmt.Sprintf("<p>The unsettled-completion sweep panicked while settling task <code>%s</code>. "+
					"Check its payment and payout rows against Stripe (payments runbook, entry 7).</p>", h.taskID))
		}
	}()
	totalMin, err := totalClosedMinutes(ctx, h.taskID)
	if err != nil {
		log.Printf("[payments][resettle] task=%s minutes: %v", h.taskID, err)
		return
	}
	// The same figures completeTask computed, from the same inputs. They are
	// read now rather than then: an admin time adjustment in the window
	// between would be reflected here and not in a chain that was already
	// running — the adjustment's own path refuses a captured task, so the
	// two cannot both land (alreadyCaptured).
	timeCostCents := calcTaskCostCents(ctx, h.taskID, totalMin)
	reimbursed := reimbursableReceiptCents(h.receiptCents, h.budgetCents)
	log.Printf("[payments][RESETTLE] task=%s completed with a live hold — settling now (time=%d receipt=%d)",
		h.taskID, timeCostCents, reimbursed)
	out := settleCompletedTask(ctx, h.taskID, orSystemActor(""), timeCostCents, reimbursed)
	if out.Busy {
		log.Printf("[payments][resettle] task=%s: a settlement is already running — left to it", h.taskID)
	}
}

// flagIncompleteSettlements: shape two. Recorded and escalated once.
func flagIncompleteSettlements(ctx context.Context) {
	rows, err := db.Query(ctx, `
		select t.id::text
		  from public.tasks t
		 where t.status = 'completed'
		   and t.completed_at < now() - ($1::int * interval '1 second')
		   and t.completed_at > now() - ($2::int * interval '1 second')
		   and t.settled_total_cents is null
		   and exists (
		         select 1 from public.payments p
		          where p.task_id = t.id and p.kind = $3 and p.status = $4)
		   and not exists (
		         select 1 from public.audit_logs a
		          where a.job_id = t.id and a.action = 'SETTLEMENT_INCOMPLETE')
		 order by t.completed_at asc
		 limit 20
	`, int(resettleAfter.Seconds()), int(resettleWindow.Seconds()), paymentKindTaskPayment, paymentStatusCaptured)
	if err != nil {
		log.Printf("[payments][resettle] incomplete query: %v", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("[payments][resettle] incomplete scan: %v", err)
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("[payments][resettle] incomplete rows: %v", err)
		return
	}
	for _, id := range ids {
		// Slow is not dead: a chain that captured and is still running past
		// ten minutes holds the per-task lock. Leave it to finish.
		running, err := isTaskSettlementRunning(ctx, id)
		if err != nil {
			log.Printf("[payments][resettle] task=%s: cannot tell whether a settlement is running: %v", id, err)
			continue
		}
		if running {
			continue
		}
		// The audit row FIRST, and only if it landed: it is the once-only
		// marker, so a row that did not insert must not be followed by an
		// email that would then repeat every five minutes.
		if err := writeAuditErr(ctx, id, orSystemActor(""), "SETTLEMENT_INCOMPLETE", "", map[string]any{
			"detail": "task payment captured; settlement did not finish (no settled_total_cents). Check payouts, balance charge, receipt.",
		}); err != nil {
			log.Printf("[payments][resettle] task=%s: could not record SETTLEMENT_INCOMPLETE: %v", id, err)
			continue
		}
		log.Printf("[payments][INCOMPLETE SETTLEMENT] task=%s: hold captured, settlement never finished — ops emailed", id)
		emailOpsAdmins("[HO:RA] Settlement incomplete — check task "+id,
			fmt.Sprintf("<p>Task <code>%s</code> is completed and its hold was captured, but the settlement never finished: "+
				"the payout to the supporter, a balance charge or the requester's receipt may be missing. "+
				"Compare the payment and payout rows with Stripe and finish by hand (payments runbook, entry 7). "+
				"This email is sent once per task.</p>", id))
	}
}

// countStaleOrphans: completed tasks with a live hold OLDER than the
// window. Never acted on by the sweep — a cron must not charge a backlog
// it did not create — but not invisible either.
func countStaleOrphans(ctx context.Context) {
	var n int
	if err := db.QueryRow(ctx, `
		select count(*)
		  from public.tasks t
		 where t.status = 'completed'
		   and t.completed_at <= now() - ($1::int * interval '1 second')
		   and exists (
		         select 1 from public.payments p
		          where p.task_id = t.id and p.kind = $2
		            and p.status in ('requires_auth', 'authorized'))
	`, int(resettleWindow.Seconds()), paymentKindTaskPayment).Scan(&n); err != nil {
		log.Printf("[payments][resettle] stale count: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[payments][resettle] %d completed task(s) older than %s still carry a live hold — outside the sweep's window, needs a person", n, resettleWindow)
	}
}

// watchUnsettledCompletions runs the sweep on a timer (main.go).
func watchUnsettledCompletions(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[payments][resettle] PANIC: %T", r)
					}
				}()
				sweepUnsettledCompletions(ctx)
			}()
		}
	}
}
