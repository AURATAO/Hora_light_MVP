package main

// A completed task whose hold is still live is a settlement that did not
// finish.
//
// completeTask answers the browser as soon as the task is `completed` and
// runs the money — capture, balance charge, payouts, receipt — on its own
// goroutine. If the process dies in those seconds (a deploy lands, the
// container is replaced), the task is `completed`, the payments row is
// still `authorized`, and nothing in-request is left to notice. Before the
// chain was detached the same restart orphaned it just the same; the only
// difference is that a browser then saw an error. This sweep is what makes
// the detached version safe: the shape is unambiguous — status completed,
// completed_at old enough that a healthy chain would long be done, a task
// payment still in a live state — and the fix is to run the same settlement
// again from the task's own record.
//
// Idempotency, branch by branch (payments_settlement.go):
//   - hold never captured → captured now, receipt sent, payout created.
//   - hold already captured at Stripe but the row never updated (died
//     between the two) → Stripe refuses the second capture → the row goes
//     to `capture_failed` with the ops email. Ops reconcile by hand; the
//     runbook's "Capture failed" entry covers it. Honest, not silent.
//   - payout already exists → payouts are unique per payment; the balance
//     charge carries an idempotency key.
//
// Not re-sent: the completion emails to requester and supporter. They are
// courtesy; the money and the receipt are the record.
//
// S-11: the actor on the audit row is the task's own supporter (the person
// whose completion this finishes), read from the row, never a client value.

import (
	"context"
	"log"
	"time"
)

// resettleAfter: how old a completed task must be before its live hold is
// read as abandoned rather than in flight. The chain takes ~15 s on
// staging; Stripe's own retries can stretch it. Ten minutes is far past
// both and still inside the authorization's life (days).
const resettleAfter = 10 * time.Minute

// sweepUnsettledCompletions settles completed tasks whose task payment is
// still live. Bounded per pass; the next tick takes the rest.
func sweepUnsettledCompletions(ctx context.Context) {
	if !paymentsEnabled() {
		return
	}
	rows, err := db.Query(ctx, `
		select t.id::text, coalesce(t.assigned_to_id::text, ''),
		       coalesce(t.receipt_amount_cents, 0), coalesce(t.shopping_budget_approved_cents, 0)
		  from public.tasks t
		 where t.status = 'completed'
		   and t.completed_at < now() - ($1::int * interval '1 second')
		   and exists (
		         select 1 from public.payments p
		          where p.task_id = t.id and p.kind = $2
		            and p.status in ('requires_auth', 'authorized'))
		 order by t.completed_at asc
		 limit 20
	`, int(resettleAfter.Seconds()), paymentKindTaskPayment)
	if err != nil {
		log.Printf("[payments][resettle] query: %v", err)
		return
	}
	type hit struct {
		taskID, supporterUID      string
		receiptCents, budgetCents int
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.taskID, &h.supporterUID, &h.receiptCents, &h.budgetCents); err != nil {
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
		totalMin, err := totalClosedMinutes(ctx, h.taskID)
		if err != nil {
			log.Printf("[payments][resettle] task=%s minutes: %v", h.taskID, err)
			continue
		}
		timeCostCents := calcTaskCostCents(ctx, h.taskID, totalMin)
		reimbursed := reimbursableReceiptCents(h.receiptCents, h.budgetCents)
		log.Printf("[payments][RESETTLE] task=%s completed with a live hold — settling now (time=%d receipt=%d)",
			h.taskID, timeCostCents, reimbursed)
		settleCompletedTask(ctx, h.taskID, h.supporterUID, timeCostCents, reimbursed)
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
