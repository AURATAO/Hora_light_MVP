package main

// Phase 2b: taking the money.
//
// Phase 1 wrote Capture and called it from nowhere; Phase 2a placed the hold.
// This file closes the loop — the settlement arithmetic, the multi-hold
// capture, the growth of a hold when a mid-task approval outgrows it, and the
// one rule everything here bends around:
//
//	A MONEY PROBLEM IS NEVER A SUPPORTER'S PROBLEM.
//
// Completion is not blocked by a failed capture, an expired hold, or a Stripe
// outage. The task completes, the payment row lands in 'capture_failed', an
// audit row is written and ops are emailed. The alternative — a supporter
// standing in someone's kitchen unable to close a finished task because a card
// network is having a bad afternoon — is not a trade worth making for a
// bookkeeping guarantee we can recover by hand in the dashboard.
//
// WHAT SETTLEMENT COSTS
//
//	time     base fee + billable minutes x the task's RESOLVED rate, clamped
//	         to the consented ceiling (billing.go, taskTimeCapMinutes)
//	receipt  the verified receipt, reimbursed up to approved budget + $5
//	total    the sum
//
// THE HOLD NO LONGER GUARANTEES THE CAPTURE FITS. It is exactly the estimate
// plus the budget (preAuthAmountCents) — what the requester was shown, not a
// padded multiple of it — so a task that runs over or comes back with a
// receipt above the approved budget settles for more than was reserved. The
// difference is charged at completion as a separate immediate-capture intent
// (kind = 'completion_balance'), and if THAT fails the requester carries an
// outstanding balance that blocks their next post.
//
// That trade is the point. The alternative — over-hold so the capture always
// fits — pays for a rare collection failure with a permanent, invisible tax on
// every honest requester's available balance.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/paymentintent"

	notify "hora-auth/internal/notify"
)

const paymentKindBudgetIncrease = "budget_increase"

// paymentStatusCaptureFailed is the hold that landed, on a task that finished,
// where the money did not move. Deliberately not 'failed', which means the
// hold never landed at all — the two need opposite ops responses. See the
// 20260914150000 migration.
const paymentStatusCaptureFailed = "capture_failed"

// ── What a task will settle at ─────────────────────────────────────────────

// settlementInputs is everything the money side of a completion needs, read in
// one place so the completion path, the force-complete path and the read-only
// breakdown cannot disagree about any of it.
type settlementInputs struct {
	Category            string
	LoggedMinutes       int
	CapMinutes          int
	Cap                 TimeCap
	ApprovedBudgetCents int
	ReceiptCents        int
	ReceiptPhotoURL     string
	// The rate this task was POSTED at. Never re-resolved — see
	// resolveRateCentsPerMin.
	RateCents int
}

func readSettlementInputs(ctx context.Context, taskID string) settlementInputs {
	in := settlementInputs{Category: taskCategory(ctx, taskID)}
	in.LoggedMinutes, _ = totalClosedMinutes(ctx, taskID)
	in.CapMinutes, in.Cap = taskTimeCapMinutes(ctx, taskID)
	in.RateCents = taskRateCentsPerMin(ctx, taskID)
	_ = db.QueryRow(ctx, `
		select coalesce(shopping_budget_approved_cents, 0),
		       coalesce(receipt_amount_cents, 0),
		       coalesce(receipt_photo_url, '')
		  from public.tasks where id = $1::uuid
	`, taskID).Scan(&in.ApprovedBudgetCents, &in.ReceiptCents, &in.ReceiptPhotoURL)
	return in
}

// quote is the itemized settlement these inputs produce.
func (in settlementInputs) quote() TaskQuote {
	return quoteSettlement(in.Category, in.LoggedMinutes, in.CapMinutes, in.ApprovedBudgetCents,
		in.ReceiptCents, in.RateCents)
}

// ── Collecting what the hold did not cover ─────────────────────────────────

const paymentKindCompletionBalance = "completion_balance"

// paymentStatusBalanceDue is a completion that could not be collected: the
// task is finished, the hold was taken in full, and a second charge for the
// difference failed. The requester owes money and cannot post again until it
// is settled. Distinct from 'capture_failed', where NOTHING was collected.
const paymentStatusBalanceDue = "balance_due"

// collectCompletionBalance charges the part of a settlement the hold could not
// cover, off-session against the saved card.
//
// This exists because the hold is now exactly the estimate plus the budget
// (see preAuthAmountCents). A task that runs over, or comes back with a
// receipt above the approved budget, settles for more than was reserved — that
// is the deliberate trade for not over-holding, and this is where it is paid
// for.
//
// A failure is NOT an error the caller should act on by refusing the
// completion: the task is finished, the supporter is owed, and a declined card
// is the requester's problem to fix rather than the supporter's to wait on.
// The row lands in 'balance_due' and blocks that requester's next post.
func collectCompletionBalance(ctx context.Context, taskID, requesterID string, amountCents int) *Payment {
	if amountCents <= 0 {
		return nil
	}
	p, err := insertPayment(ctx, taskID, requesterID, paymentKindCompletionBalance,
		paymentStatusRequiresAuth, amountCents)
	if err != nil {
		log.Printf("[payments][balance][ERROR] task=%s could not record a %s balance: %v",
			taskID, formatCentsUSD(amountCents), err)
		return nil
	}

	pi, err := chargeBalanceOffSession(ctx, p, taskID, requesterID, amountCents)
	if err != nil {
		markBalanceDue(ctx, p.ID, taskID, requesterID, amountCents, err)
		p.Status = paymentStatusBalanceDue
		return p
	}

	if err := recordCapture(ctx, p.ID, int(pi.AmountReceived), 0, 0); err != nil {
		log.Printf("[payments][balance][ERROR] charged intent=%s but did not record payment=%s: %v",
			pi.ID, p.ID, err)
	}
	log.Printf("[payments][balance] task=%s payment=%s intent=%s collected %s",
		taskID, p.ID, pi.ID, formatCentsUSD(amountCents))
	p.Status = paymentStatusCaptured
	return p
}

// chargeBalanceOffSession creates and confirms an immediate-capture intent for
// the outstanding amount.
//
// CaptureMethod is automatic here, unlike every other intent in this codebase:
// there is nothing to hold and release, because the task is over and the
// amount is already known exactly.
func chargeBalanceOffSession(ctx context.Context, p *Payment, taskID, requesterID string, amountCents int) (*stripe.PaymentIntent, error) {
	var email string
	if err := db.QueryRow(ctx,
		`select coalesce(email,'') from public.users where id = $1::uuid`, requesterID).Scan(&email); err != nil {
		return nil, fmt.Errorf("requester %s: %w", requesterID, err)
	}
	payCtx, err := resolvePreAuthContext(ctx, requesterID, email)
	if err != nil {
		return nil, fmt.Errorf("resolve card: %w", err)
	}

	params := &stripe.PaymentIntentParams{
		Amount:        stripe.Int64(int64(amountCents)),
		Currency:      stripe.String(Billing.Currency),
		Customer:      stripe.String(payCtx.CustomerID),
		PaymentMethod: stripe.String(payCtx.PaymentMethodID),
		Confirm:       stripe.Bool(true),
		OffSession:    stripe.Bool(true),
		Metadata: map[string]string{
			"task_id":      taskID,
			"requester_id": requesterID,
			"payment_id":   p.ID,
			"kind":         paymentKindCompletionBalance,
		},
	}
	// Keyed on the payments row, so a retried settle cannot charge twice.
	params.SetIdempotencyKey("balance_" + p.ID)
	params.AddExpand("latest_charge")

	pi, err := paymentintent.New(params)
	if err != nil {
		pe := classifyPreAuthError(err)
		if pe.PaymentIntentID != "" {
			// Kept on the row so a later settle can resume this same intent —
			// an off-session 3DS decline is recoverable with the cardholder
			// present, which is exactly what /payments/settle-balance does.
			_ = attachIntent(ctx, p.ID, pe.PaymentIntentID, paymentStatusRequiresAuth, nil)
		}
		return nil, pe
	}
	if pi.Status != stripe.PaymentIntentStatusSucceeded {
		_ = attachIntent(ctx, p.ID, pi.ID, paymentStatusRequiresAuth, nil)
		return nil, fmt.Errorf("balance charge came back %q, not succeeded", pi.Status)
	}
	brand, last4 := cardFromIntent(pi)
	if err := attachIntent(ctx, p.ID, pi.ID, paymentStatusCaptured, &amountCents); err != nil {
		log.Printf("[payments][balance][ERROR] charged %s but did not attach to payment=%s: %v",
			pi.ID, p.ID, err)
	}
	recordPaymentCard(ctx, p.ID, brand, last4)
	return pi, nil
}

// markBalanceDue records a collection failure and tells everyone who needs to
// know: the requester because they have to act, ops because the platform is
// now carrying the float.
func markBalanceDue(ctx context.Context, paymentID, taskID, requesterID string, amountCents int, cause error) {
	if err := updatePaymentStatus(ctx, paymentID, paymentStatusBalanceDue, nil); err != nil {
		log.Printf("[payments][balance][ERROR] could not mark payment=%s balance_due: %v", paymentID, err)
	}
	log.Printf("[payments][BALANCE DUE] task=%s requester=%s amount=%s: %v",
		taskID, requesterID, formatCentsUSD(amountCents), cause)
	writeAudit(ctx, taskID, systemActorUID, "PAYMENT_BALANCE_DUE", "", map[string]any{
		"payment_id":   paymentID,
		"amount_cents": amountCents,
		"error":        cause.Error(),
	})

	t, err := loadAdminTask(ctx, taskID)
	if err != nil {
		return
	}
	notifyUser(ctx, requesterID, t.RequesterEmail, notify.CreateNotificationInput{
		TaskID: taskID,
		Type:   "BALANCE_DUE",
		Title:  "Payment didn't go through",
		Body: fmt.Sprintf(
			"%s from %q couldn't be charged to your card. Settle it in the app to keep posting tasks — your supporter has been paid either way.",
			formatCentsUSD(amountCents), t.Title),
		TaskTitle: t.Title,
	})
	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Balance due — %s uncollected", formatCentsUSD(amountCents)),
		fmt.Sprintf(`<p><strong>A completed task settled for more than its hold and the difference could not be charged.</strong></p>
<p>The task completed and the supporter is unaffected — payouts are deliberately NOT gated on this, so
the platform carries the float until the requester settles.</p>
<ul>
  <li>Task: %s (%s)</li>
  <li>Requester: %s</li>
  <li>Outstanding: %s</li>
  <li>Error: %s</li>
</ul>
<p>The requester is blocked from posting until they settle in-app. Nothing to do here unless it stays
outstanding.</p>`,
			t.Title, taskID, t.RequesterEmail, formatCentsUSD(amountCents), cause.Error()))
}

// ── Settling ───────────────────────────────────────────────────────────────

// settlementOutcome is what happened to the money when a task closed.
type settlementOutcome struct {
	// Attempted is false when the task had no hold at all — every task posted
	// with PAYMENTS_ENFORCED off. Nothing failed; there was nothing to do.
	Attempted bool
	// CapturedCents is what actually moved: the hold, plus any balance charge
	// that succeeded.
	CapturedCents int
	TimeCostCents int
	ReceiptCents  int
	// BalanceDueCents is what could not be collected. Non-zero means the
	// requester owes it and is blocked from posting until they settle.
	BalanceDueCents int
	// Err is set when the CAPTURE itself failed — nothing was collected at
	// all. The task is still completed; the payment row is in capture_failed.
	Err error
}

// settleTaskPayment collects what a finished task owes.
//
//	total <= hold   capture total; Stripe releases the rest by itself
//	total >  hold   capture the whole hold, then charge the difference to the
//	                saved card as a separate 'completion_balance' intent
//
// NEVER returns an error the caller should act on by refusing the completion.
// The task is over either way; a money problem is never a supporter's problem.
func settleTaskPayment(ctx context.Context, taskID string, timeCostCents, receiptCents int) settlementOutcome {
	out := settlementOutcome{TimeCostCents: timeCostCents, ReceiptCents: receiptCents}
	if !paymentsEnabled() {
		return out
	}

	main, err := livePaymentForTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No live hold: the task predates enforcement, or the hold was already
		// released or captured. Nothing owed here.
		return out
	}
	if err != nil {
		out.Attempted = true
		out.Err = fmt.Errorf("read payment: %w", err)
		return out
	}
	out.Attempted = true

	total := timeCostCents + receiptCents

	// Capture reuses the Phase 1 service unchanged: it clamps to the hold,
	// logs UNDERCAPTURE when the clamp bites, and records the split.
	captured, err := Capture(ctx, taskID, timeCostCents, receiptCents)
	if err != nil {
		out.Err = err
		markCaptureFailed(ctx, main.ID, err)
		return out
	}
	out.CapturedCents = derefIntOr(captured.CapturedCents, 0)

	// The overage. This is the branch the whole restructure buys: holding
	// exactly the estimate means a task that ran over settles above its hold,
	// and the difference is charged now rather than pre-emptively frozen on
	// everybody's card for weeks.
	if shortfall := total - out.CapturedCents; shortfall > 0 {
		balance := collectCompletionBalance(ctx, taskID, main.RequesterID, shortfall)
		switch {
		case balance == nil:
			out.BalanceDueCents = shortfall
		case balance.Status == paymentStatusCaptured:
			out.CapturedCents += shortfall
		default:
			out.BalanceDueCents = shortfall
		}
	}
	return out
}

// markCaptureFailed is the whole difference between "we know this task was
// worked and nobody paid" and finding out from a bank statement three weeks
// later.
func markCaptureFailed(ctx context.Context, paymentID string, cause error) {
	if err := updatePaymentStatus(ctx, paymentID, paymentStatusCaptureFailed, nil); err != nil {
		log.Printf("[payments][settle][ERROR] could not mark payment=%s capture_failed: %v", paymentID, err)
	}
	log.Printf("[payments][CAPTURE FAILED] payment=%s: %v", paymentID, cause)
}

// recordTaskSettlement writes the read model the settlement view renders.
// Best-effort: the money has already moved by the time this runs, and a failed
// write here costs a display, not a charge.
func recordTaskSettlement(ctx context.Context, taskID string, timeCostCents, totalCents int) {
	if _, err := db.Exec(ctx, `
		update public.tasks
		   set settled_time_cost_cents = $2, settled_total_cents = $3, settled_at = now()
		 where id = $1::uuid
	`, taskID, timeCostCents, totalCents); err != nil {
		log.Printf("[payments][settle][ERROR] could not record settlement task=%s: %v", taskID, err)
	}
}

// reportCaptureFailure is what happens instead of blocking the completion.
func reportCaptureFailure(ctx context.Context, taskID string, out settlementOutcome, actorUID string) {
	writeAudit(ctx, taskID, orSystemActor(actorUID), "PAYMENT_CAPTURE_FAILED", "", map[string]any{
		"time_cost_cents": out.TimeCostCents,
		"receipt_cents":   out.ReceiptCents,
		"owed_cents":      out.TimeCostCents + out.ReceiptCents,
		"error":           out.Err.Error(),
	})

	owed := formatCentsUSD(out.TimeCostCents + out.ReceiptCents)
	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Capture failed — %s uncollected", owed),
		fmt.Sprintf(`<p><strong>A completed task could not be charged.</strong></p>
<p>The task is completed and the supporter has been told it is done — that is deliberate, a payment
problem never blocks a completion. Nobody has been charged, and this needs settling by hand in the
Stripe dashboard.</p>
<ul>
  <li>Task: %s</li>
  <li>Owed: %s (time %s + receipt %s)</li>
  <li>Error: %s</li>
</ul>
<p>Stripe dashboard &rarr; Payments &rarr; search the task id in metadata.</p>`,
			taskID, owed,
			formatCentsUSD(out.TimeCostCents), formatCentsUSD(out.ReceiptCents),
			out.Err.Error()))
}

// emailOpsAdmins sends one message to the ops allowlist.
//
// Fired in a goroutine on a background context (S-32): every caller is on a
// request path a user is waiting on, and a slow mail provider must not hold it
// open. Email rather than a notifications row for the same reason a dispute is
// — notifications.type is an enum of task lifecycle events and user_id is NOT
// NULL, and these belong to the platform rather than to any one user.
func emailOpsAdmins(subject, html string) {
	recipients := make([]string, 0, len(opsAdmins))
	for email := range opsAdmins {
		recipients = append(recipients, email)
	}
	sortStrings(recipients)

	go func() {
		for _, to := range recipients {
			if err := notify.SendEmail(notify.EmailPayload{To: to, Subject: subject, Html: html}); err != nil {
				log.Printf("[ops][email] failed to=%s subject=%q err=%v", to, subject, err)
			}
		}
	}()
}

func derefIntOr(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}

// alreadyCaptured reports whether a task's money has moved. The gate on
// admin adjust-time: re-pricing a settled task would produce a number that
// disagrees with what the card was charged, and correcting a capture is a
// refund — a manual Stripe-dashboard operation during beta.
func alreadyCaptured(ctx context.Context, taskID string) (bool, time.Time) {
	var capturedAt *time.Time
	err := db.QueryRow(ctx, `
		select max(updated_at) from public.payments
		 where task_id = $1::uuid and status = $2
	`, taskID, paymentStatusCaptured).Scan(&capturedAt)
	if err != nil || capturedAt == nil {
		return false, time.Time{}
	}
	return true, *capturedAt
}

// settleCompletedTask is the one call every closing path makes: complete,
// admin force-complete, and a cancel that settled real work.
//
// It captures, records the read model, and — when the capture failed — writes
// the audit row and emails ops. What it never does is return an error the
// caller is expected to act on, because there is no correct action: the task
// is over either way and the supporter is not the person who should discover
// a card problem.
func settleCompletedTask(ctx context.Context, taskID, actorUID string, timeCostCents, receiptCents int) settlementOutcome {
	out := settleTaskPayment(ctx, taskID, timeCostCents, receiptCents)
	switch {
	case out.Err != nil:
		reportCaptureFailure(ctx, taskID, out, actorUID)
	case out.Attempted:
		recordTaskSettlement(ctx, taskID, timeCostCents, out.CapturedCents)
		writeAudit(ctx, taskID, orSystemActor(actorUID), "PAYMENT_CAPTURED", "", map[string]any{
			"time_cost_cents": timeCostCents,
			"receipt_cents":   receiptCents,
			"captured_cents":  out.CapturedCents,
		})
		log.Printf("[payments][settle] task=%s captured=%s (time=%s receipt=%s)",
			taskID, formatCentsUSD(out.CapturedCents),
			formatCentsUSD(timeCostCents), formatCentsUSD(receiptCents))
	}
	return out
}

// ── What the requester is told about their hold ────────────────────────────

// TaskPayment is the money side of a task, as the REQUESTER sees it.
//
// REQUESTER ONLY. It is attached by taskPaymentView's callers behind an
// explicit ownership check and must never reach the supporter: what somebody
// reserved and which card they reserved it on is theirs, and a supporter has
// no use for either. `payments` is deny-all at the database for the same
// reason (S-10) — this is the one narrow, deliberate window onto it.
//
// Everything here is a number the server already computed. Clients render it
// verbatim and derive nothing (S-05): there is no client-side arithmetic that
// can disagree with what Stripe was actually asked to hold.
type TaskPayment struct {
	// What is authorized on the card right now, or was before settlement.
	AuthorizedCents int `json:"authorized_cents"`
	// requires_auth / authorized / captured / canceled / failed / capture_failed.
	Status string `json:"status"`

	// Display card. Both empty on a hold placed before this was recorded, or
	// where Stripe returned no charge detail — clients drop the card clause
	// rather than inventing one.
	CardBrand string `json:"card_brand,omitempty"`
	CardLast4 string `json:"card_last4,omitempty"`

	// Settlement, present once the money has moved. CapturedCents is what was
	// actually taken; ReleasedCents is the rest of the hold, which Stripe frees
	// on its own — it is NOT a refund and the copy must not call it one.
	CapturedCents int `json:"captured_cents"`
	ReleasedCents int `json:"released_cents"`

	// What the hold is MADE OF: the time estimate and the shopping budget.
	// Sent so a confirmation can read "$49.50 reserved — $19.50 time + $30.00
	// budget" without a client adding anything up (S-05).
	//
	// Both are zero, and the clients then show the single total, unless the
	// two demonstrably reconcile with AuthorizedCents. A task edited down
	// after its hold was placed would otherwise be described by a breakdown
	// that does not sum to the number beside it, which is worse than no
	// breakdown at all.
	TimeCostCents       int `json:"time_cost_cents,omitempty"`
	ShoppingBudgetCents int `json:"shopping_budget_cents,omitempty"`
}

// taskPaymentView reads the task's payment row, or nil when there is none.
//
// Nil is the normal answer in the running beta: with PAYMENTS_ENFORCED off no
// task has a hold, and every surface that renders this treats nil as "say
// nothing about money" rather than as "$0.00 reserved", which would be a
// confident lie.
//
// Deliberately NOT livePaymentForTask: this has to keep answering after the
// hold is captured or released, because that is exactly when the requester
// most wants to know what happened to it.
func taskPaymentView(ctx context.Context, taskID string) *TaskPayment {
	var p TaskPayment
	var authorized, captured *int
	var brand, last4 string
	err := db.QueryRow(ctx, `
		select coalesce(authorized_cents, 0), captured_cents, status,
		       coalesce(card_brand,''), coalesce(card_last4,'')
		  from public.payments
		 where task_id = $1::uuid and kind = $2
		 order by created_at desc
		 limit 1
	`, taskID, paymentKindTaskPayment).Scan(&authorized, &captured, &p.Status, &brand, &last4)
	if err != nil {
		return nil
	}
	p.AuthorizedCents = derefIntOr(authorized, 0)
	p.CapturedCents = derefIntOr(captured, 0)
	p.CardBrand, p.CardLast4 = brand, last4

	// The split, reconstructed from the task the hold was placed for, and
	// included ONLY when it adds up to what was actually authorized.
	var category string
	var estimate, budget, rate int
	if err := db.QueryRow(ctx, `
		select coalesce(category,''), coalesce(estimated_minutes,0),
		       coalesce(prepay_amount_cents,0), coalesce(rate_cents_per_min,0)
		  from public.tasks where id = $1::uuid
	`, taskID).Scan(&category, &estimate, &budget, &rate); err == nil {
		timePart := baseFeeCents(category) + timeCostCents(estimate, rate)
		if timePart+budget == p.AuthorizedCents {
			p.TimeCostCents, p.ShoppingBudgetCents = timePart, budget
		}
	}

	// The released half is derived, not stored: Stripe frees the remainder of
	// a partially-captured authorization itself, so there is no event and no
	// column recording it — only the arithmetic. Floored at zero so a clamped
	// capture can never report a negative release.
	switch p.Status {
	case paymentStatusCaptured:
		if rest := p.AuthorizedCents - p.CapturedCents; rest > 0 {
			p.ReleasedCents = rest
		}
	case paymentStatusCanceled:
		// Nothing was taken, so the whole hold went back.
		p.ReleasedCents = p.AuthorizedCents
	}
	return &p
}

// ── Outstanding balance ────────────────────────────────────────────────────

// OutstandingBalance is what a requester owes across every uncollected
// completion. Derived from the payments ledger on every read rather than kept
// in a column: the rows already say it, and a denormalized total is a second
// place for the truth to live and a second place for it to be wrong.
type OutstandingBalance struct {
	TotalCents int    `json:"total_cents"`
	TaskID     string `json:"task_id"`
	TaskTitle  string `json:"task_title"`
	// How many tasks it spans. One is the overwhelmingly common case and the
	// copy names that task; more than one and the client says "and N more".
	TaskCount int `json:"task_count"`
}

// outstandingBalanceFor returns what this requester owes, or nil when nothing.
//
// Nil is the normal answer and every caller treats it as "say nothing" — a
// zeroed struct would have surfaces rendering "You have an outstanding balance
// of $0.00", which is both false and alarming.
func outstandingBalanceFor(ctx context.Context, requesterID string) *OutstandingBalance {
	if requesterID == "" {
		return nil
	}
	var b OutstandingBalance
	var title *string
	err := db.QueryRow(ctx, `
		select coalesce(sum(p.authorized_cents), 0),
		       count(distinct p.task_id),
		       (array_agg(p.task_id::text order by p.created_at desc))[1],
		       (array_agg(t.title order by p.created_at desc))[1]
		  from public.payments p
		  join public.tasks t on t.id = p.task_id
		 where p.requester_id = $1::uuid and p.status = $2
	`, requesterID, paymentStatusBalanceDue).Scan(&b.TotalCents, &b.TaskCount, &b.TaskID, &title)
	if err != nil || b.TotalCents <= 0 {
		return nil
	}
	if title != nil {
		b.TaskTitle = *title
	}
	return &b
}

// settleOutstandingBalance retries every balance_due charge for one requester.
//
// THREE OUTCOMES, and the middle one is the reason this endpoint exists rather
// than a background retry:
//
//	settled              every row collected; the block lifts
//	needs authentication the issuer wants the cardholder present. A client
//	                     secret comes back and the client runs the same 3DS
//	                     flow posting uses, then calls this again.
//	declined             still refused. The block stays and the message says
//	                     what to do (another card).
//
// An off-session charge that fails for 3DS cannot be rescued by retrying it
// off-session — only by putting the cardholder in front of it. That is exactly
// what a Settle button does, which is why collection moved from a silent
// retry to a deliberate user action.
func settleOutstandingBalance(c *gin.Context) {
	uid := c.GetString("uid")
	email := c.GetString("email")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	rows, err := db.Query(ctx, `
		select id::text, task_id::text, coalesce(authorized_cents, 0),
		       coalesce(stripe_payment_intent_id, '')
		  from public.payments
		 where requester_id = $1::uuid and status = $2
		 order by created_at asc
	`, uid, paymentStatusBalanceDue)
	if err != nil {
		log.Printf("[payments][settle] read task=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	type due struct {
		id, taskID, intentID string
		cents                int
	}
	var owed []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.taskID, &d.cents, &d.intentID); err != nil {
			break
		}
		owed = append(owed, d)
	}
	rows.Close()

	if len(owed) == 0 {
		// Nothing to settle is success, not an error: two taps on the button,
		// or a settle that raced the banner disappearing.
		c.JSON(http.StatusOK, gin.H{"ok": true, "settled_cents": 0, "outstanding": nil})
		return
	}

	settled := 0
	for _, d := range owed {
		p := &Payment{ID: d.id, TaskID: d.taskID, RequesterID: uid, StripePaymentIntentID: d.intentID}
		pi, err := chargeBalanceOffSession(ctx, p, d.taskID, uid, d.cents)
		if err == nil {
			if recErr := recordCapture(ctx, d.id, int(pi.AmountReceived), 0, 0); recErr != nil {
				log.Printf("[payments][settle][ERROR] charged %s but did not record %s: %v",
					pi.ID, d.id, recErr)
			}
			writeAudit(ctx, d.taskID, uid, "PAYMENT_BALANCE_SETTLED", "", map[string]any{
				"payment_id":   d.id,
				"amount_cents": d.cents,
			})
			settled += d.cents
			continue
		}

		// The recoverable failure. Hand back what the client needs to put the
		// cardholder in front of their bank, exactly as the post path does.
		var pe *PreAuthError
		if errors.As(err, &pe) && pe.RequiresAction {
			log.Printf("[payments][settle] requester=%s needs 3DS on payment=%s", uid, d.id)
			c.JSON(http.StatusPaymentRequired, gin.H{
				"error":             "payment_authentication_required",
				"message":           "Your bank needs to confirm this payment.",
				"client_secret":     pe.ClientSecret,
				"payment_intent_id": pe.PaymentIntentID,
				"publishable_key":   stripePublishableKey(),
				"settled_cents":     settled,
				"outstanding":       outstandingBalanceFor(ctx, uid),
			})
			return
		}

		message := "That card was declined. Try another card."
		if pe != nil && pe.Message != "" {
			message = pe.Message
		}
		log.Printf("[payments][settle] requester=%s payment=%s still failing: %v", uid, d.id, err)
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":         "payment_required",
			"message":       message,
			"settled_cents": settled,
			"outstanding":   outstandingBalanceFor(ctx, uid),
		})
		return
	}

	log.Printf("[payments][settle] requester=%s settled %s across %d task(s)",
		uid, formatCentsUSD(settled), len(owed))
	_ = email
	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"settled_cents": settled,
		"outstanding":   outstandingBalanceFor(ctx, uid),
	})
}

// GET /payments/outstanding-balance
//
// The banner's source. Answers `{"outstanding": null}` for everybody who owes
// nothing — which is everybody, almost always — so the client renders nothing
// rather than having to interpret a zero.
func outstandingBalanceHandler(c *gin.Context) {
	uid := c.GetString("uid")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"outstanding": outstandingBalanceFor(c.Request.Context(), uid)})
}
