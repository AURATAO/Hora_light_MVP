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

	chargeID := chargeIDFromIntent(pi)
	if err := recordCapture(ctx, p.ID, int(pi.AmountReceived), 0, 0, chargeID); err != nil {
		log.Printf("[payments][balance][ERROR] charged intent=%s but did not record payment=%s: %v",
			pi.ID, p.ID, err)
	}
	log.Printf("[payments][balance] task=%s payment=%s intent=%s collected %s",
		taskID, p.ID, pi.ID, formatCentsUSD(amountCents))
	p.Status = paymentStatusCaptured
	p.StripeChargeID = chargeID
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
		// Same task id the pre-auth carries, so a supporter transfer funded by
		// this charge groups with the rest of the task's money in Stripe.
		TransferGroup: stripe.String(taskID),
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

	pi, err := stripeCreatePaymentIntent(params)
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
	// Who was paid, when anybody was. Empty on a task nobody accepted, and on
	// every task settled before Phase 3. Reported so the completion response
	// can tell the supporter what they earned without a second lookup.
	SupporterID string

	// The promo discount that came off the requester's charge (promo.go), and
	// the payments rows the money moved through — what the receipt names.
	// Zero and empty on the overwhelming majority of tasks.
	DiscountCents        int
	MainPaymentID        string
	MainCapturedCents    int
	BalancePaymentID     string
	BalanceCapturedCents int
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
	out.MainPaymentID = main.ID

	// TWO TOTALS. `total` is what the SUPPORTER is owed — time plus receipt,
	// undiscounted. `charged` is what the REQUESTER pays: the same, less any
	// promo discount, never below $0. Every branch below collects `charged`
	// and pays out `total`; the gap is the platform's (promo.go).
	total := timeCostCents + receiptCents
	_, promoDiscount := promoDiscountForTask(ctx, taskID)
	charged := afterPromo(total, promoDiscount)
	out.DiscountCents = total - charged

	// Capture reuses the Phase 1 service: it clamps to the hold, logs
	// UNDERCAPTURE when the clamp bites, and records the split.
	captured, err := Capture(ctx, taskID, timeCostCents, receiptCents, promoDiscount)
	if err != nil {
		out.Err = err
		markCaptureFailed(ctx, main.ID, err)
		return out
	}
	out.CapturedCents = derefIntOr(captured.CapturedCents, 0)
	// What the HOLD alone covered, kept before the balance branch adds to it.
	// The supporter's first transfer is funded by this charge and may not
	// exceed it — see payoutForTask.
	mainCaptured := out.CapturedCents
	out.MainCapturedCents = mainCaptured

	// The overage. This is the branch the whole restructure buys: holding
	// exactly the estimate means a task that ran over settles above its hold,
	// and the difference is charged now rather than pre-emptively frozen on
	// everybody's card for weeks.
	//
	// A remainder below Stripe's minimum charge cannot be collected at all — a
	// promo that took the hold to $0 on a task that then settled for 30¢ — and
	// is waived rather than left as a balance due that would block the
	// requester's next post over money nobody can take. The supporter is still
	// paid it, from the platform balance, beside the promo subsidy.
	var balance *Payment
	waived := 0
	shortfall := charged - out.CapturedCents
	if shortfall > 0 && shortfall < stripeMinimumChargeCents {
		log.Printf("[payments][settle] task=%s remainder %s is below Stripe's minimum — waived",
			taskID, formatCentsUSD(shortfall))
		waived, shortfall = shortfall, 0
	}
	if shortfall > 0 {
		balance = collectCompletionBalance(ctx, taskID, main.RequesterID, shortfall)
		switch {
		case balance == nil:
			out.BalanceDueCents = shortfall
		case balance.Status == paymentStatusCaptured:
			out.CapturedCents += shortfall
			out.BalancePaymentID = balance.ID
			out.BalanceCapturedCents = shortfall
		default:
			out.BalanceDueCents = shortfall
		}
	}

	// ── Phase 3: the supporter's side of the same settlement ──────────────
	//
	// ONE PAYOUT PER CAPTURED PAYMENT, and that is not an implementation
	// detail — it is what makes the whole thing idempotent. Each transfer is
	// tied by source_transaction to the exact charge that funded it, so it
	// cannot be paid before that money arrives and cannot exceed it; and
	// payouts.payment_id is UNIQUE, so a second settle of the same payment
	// cannot produce a second transfer.
	//
	// Nothing below can fail the settlement. payoutForTask never returns an
	// error the caller acts on, for the same reason capture does not: by this
	// line the task is complete and the requester has been charged, and a
	// transfer problem is an ops ticket rather than a reason to unwind money
	// that has already moved.
	out.SupporterID = taskSupporterID(ctx, taskID)
	if out.SupporterID != "" {
		// How much the balance charge actually collected. Distinct from
		// BalanceDueCents, which is what it FAILED to collect.
		balanceCaptured := out.BalanceCapturedCents
		// What the platform owes the supporter on top of the requester's
		// money: the promo discount, plus any remainder too small to charge.
		subsidy := out.DiscountCents + waived
		for _, split := range settlementPayouts(total, mainCaptured, balanceCaptured, subsidy) {
			in := payoutInput{
				TaskID:         taskID,
				SupporterID:    out.SupporterID,
				OwedCents:      split.OwedCents,
				ShortfallCents: split.ShortfallCents,
			}
			switch split.Source {
			case payoutSourceHold:
				in.PaymentID, in.SourceChargeID = main.ID, captured.StripeChargeID
			case payoutSourceBalance:
				in.PaymentID, in.SourceChargeID = balance.ID, balance.StripeChargeID
			case payoutSourcePromo:
				in.Funding = payoutFundingPromoSubsidy
			}
			payoutForTask(ctx, in)
		}
	}
	return out
}

// taskSupporterID is who worked the task, or empty when nobody did.
//
// Read at settlement rather than passed in because all three closing paths
// (complete, force-complete, a cancel that settled real work) reach here with
// different amounts of the task already in hand, and the assignment is the one
// thing all three can agree to look up the same way.
func taskSupporterID(ctx context.Context, taskID string) string {
	var supporterID *string
	// cancelled_assignee_id is the fallback, not the primary: the cancel path
	// settles BEFORE it detaches, so assigned_to_id is still there when this
	// runs. It is read anyway because "who gets paid" must not depend on the
	// order of two statements in a handler three files away — a cancel that
	// charged somebody and then paid nobody is the failure this whole change
	// exists to stop.
	if err := db.QueryRow(ctx,
		`select coalesce(assigned_to_id, cancelled_assignee_id) from public.tasks where id = $1::uuid`, taskID,
	).Scan(&supporterID); err != nil || supporterID == nil {
		return ""
	}
	return *supporterID
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
			"time_cost_cents":      timeCostCents,
			"receipt_cents":        receiptCents,
			"promo_discount_cents": out.DiscountCents,
			"captured_cents":       out.CapturedCents,
		})
		log.Printf("[payments][settle] task=%s captured=%s (time=%s receipt=%s discount=%s)",
			taskID, formatCentsUSD(out.CapturedCents),
			formatCentsUSD(timeCostCents), formatCentsUSD(receiptCents), formatCentsUSD(out.DiscountCents))
		// THE RECEIPT, from the same call that captured. This is the only
		// place a completion's charges are known together — the hold and the
		// balance — and putting the receipt here rather than in each closing
		// handler is what makes "a charge can never happen without a receipt"
		// a property of the code rather than a convention (receipts.go).
		sendRequesterReceipt(ctx, taskID, receiptChargesFor(out))
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
	// The two halves of TimeCostCents, so a confirmation can say "$25.00 base
	// + $7.50 time" — which is how a companionship task shows its base — with
	// no subtraction on the client. Sent under the same reconciliation rule.
	BaseFeeCents     int `json:"base_fee_cents,omitempty"`
	MinutesCostCents int `json:"minutes_cost_cents,omitempty"`

	// The promo, when the task was posted under one: the code, what it took
	// off, and what the hold would have been without it. "$19.50 − $10.00
	// promo = $9.50 reserved" is these three numbers in that order (S-05).
	PromoCode          string `json:"promo_code,omitempty"`
	PromoDiscountCents int    `json:"promo_discount_cents,omitempty"`
	PreDiscountCents   int    `json:"pre_discount_cents,omitempty"`
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
	// included ONLY when it adds up to what was actually authorized — with
	// the promo discount taken off first, when there is one.
	var category string
	var estimate, budget, rate int
	if err := db.QueryRow(ctx, `
		select coalesce(category,''), coalesce(estimated_minutes,0),
		       coalesce(prepay_amount_cents,0), coalesce(rate_cents_per_min,0)
		  from public.tasks where id = $1::uuid
	`, taskID).Scan(&category, &estimate, &budget, &rate); err == nil {
		base := baseFeeCents(category)
		minutes := timeCostCents(estimate, rate)
		timePart := base + minutes
		code, discount := promoDiscountForTask(ctx, taskID)
		applied := promoApplied(discount, timePart+budget)
		if timePart+budget-applied == p.AuthorizedCents {
			p.TimeCostCents, p.ShoppingBudgetCents = timePart, budget
			p.BaseFeeCents, p.MinutesCostCents = base, minutes
			if applied > 0 {
				p.PromoCode, p.PromoDiscountCents, p.PreDiscountCents = code, applied, timePart+budget
			}
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

	// Unlocked fast path, and the common one: nothing owed costs no
	// transaction and no lock. Nothing to settle is success, not an error —
	// a settle that raced the banner disappearing lands here.
	owed, err := readBalanceDue(ctx, db, uid)
	if err != nil {
		log.Printf("[payments][settle] read requester=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if len(owed) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "settled_cents": 0, "outstanding": nil})
		return
	}

	out, err := runSettlePass(ctx, uid)
	if err != nil {
		log.Printf("[payments][settle] requester=%s: %v", uid, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// The recoverable failure. Hand back what the client needs to put the
	// cardholder in front of their bank, exactly as the post path does.
	if out.authErr != nil {
		log.Printf("[payments][settle] requester=%s needs 3DS on payment=%s", uid, out.stoppedAt)
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":             "payment_authentication_required",
			"message":           "Your bank needs to confirm this payment.",
			"client_secret":     out.authErr.ClientSecret,
			"payment_intent_id": out.authErr.PaymentIntentID,
			"publishable_key":   stripePublishableKey(),
			"settled_cents":     out.settledCents,
			"outstanding":       outstandingBalanceFor(ctx, uid),
		})
		return
	}
	if out.declineMessage != "" {
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":         "payment_required",
			"message":       out.declineMessage,
			"settled_cents": out.settledCents,
			"outstanding":   outstandingBalanceFor(ctx, uid),
		})
		return
	}

	log.Printf("[payments][settle] requester=%s settled %s across %d task(s)",
		uid, formatCentsUSD(out.settledCents), out.settledCount)
	_ = email
	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"settled_cents": out.settledCents,
		"outstanding":   outstandingBalanceFor(ctx, uid),
	})
}

// balanceDue is one collectable row: a payments row sitting in 'balance_due'.
type balanceDue struct {
	id, taskID, intentID string
	cents                int
}

// readBalanceDue lists what a requester still owes, oldest first. Takes the
// querier so it can run on the pool (the unlocked pre-read) or inside the
// transaction holding the settle lock (the double-check).
func readBalanceDue(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, uid string) ([]balanceDue, error) {
	rows, err := q.Query(ctx, `
		select id::text, task_id::text, coalesce(authorized_cents, 0),
		       coalesce(stripe_payment_intent_id, '')
		  from public.payments
		 where requester_id = $1::uuid and status = $2
		 order by created_at asc
	`, uid, paymentStatusBalanceDue)
	if err != nil {
		return nil, fmt.Errorf("payments: read balance due for requester %s: %w", uid, err)
	}
	defer rows.Close()

	var owed []balanceDue
	for rows.Next() {
		var d balanceDue
		if err := rows.Scan(&d.id, &d.taskID, &d.cents, &d.intentID); err != nil {
			return nil, fmt.Errorf("payments: scan balance due for requester %s: %w", uid, err)
		}
		owed = append(owed, d)
	}
	return owed, rows.Err()
}

// settleOutcome is what one settlement pass produced. At most one of authErr
// and declineMessage is set; either means the pass stopped early, with
// settledCents already collected and committed.
type settleOutcome struct {
	settledCents   int
	settledCount   int
	authErr        *PreAuthError
	declineMessage string
	stoppedAt      string // payment id the pass stopped on, for the log line
}

// settleBalanceTimeout bounds the locked section. Longer than
// stripeRefCreateTimeout because a pass is N charges, not one create, and
// still short enough that a hung Stripe connection releases the lock rather
// than parking every later settle for that requester behind it.
const settleBalanceTimeout = 90 * time.Second

// runSettlePass charges every balance_due row for one requester, serialized so
// that two concurrent passes cannot both charge the same row.
//
// ── WHY THERE IS A LOCK, AND WHY IT IS NOT `SELECT … FOR UPDATE` ───────────
//
// The hazard is the one that produced the customer-create 500 on 2026-09-18,
// in a second shape. Double-tapping Settle ran two passes; both read the same
// balance_due rows, both called paymentintent.New with the same idempotency
// key `balance_<payment_id>`, and Stripe answered the second with HTTP 409
// `idempotency_key_in_use` — an error, not a replay, because the cache
// replays a COMPLETED request and does not serialize an in-flight one. The
// key still did its real job: the requester was never charged twice. But the
// loser's 409 was classified as a decline, so a settle that had in fact just
// succeeded told the cardholder "we couldn't reach your bank just now".
//
// The obvious fix is a row lock — `select … from payments … for update` — and
// it deadlocks. chargeBalanceOffSession writes that same row through the POOL
// (attachIntent, recordPaymentCard), as does recordCapture afterwards, each on
// its own connection. A pass holding the row lock would wait on Stripe while
// its own pool-side UPDATE waited on the row lock: a circular wait across two
// connections, which Postgres cannot detect and break, so it hangs to the
// context deadline instead of erroring.
//
// So the serialization is a per-requester advisory lock instead. It excludes
// the other pass without locking the rows the pass itself has to write, and
// being transaction-scoped it is released by the rollback below — including
// the rollback a panic or a context timeout triggers.
func runSettlePass(ctx context.Context, uid string) (settleOutcome, error) {
	var out settleOutcome

	ctx, cancel := context.WithTimeout(ctx, settleBalanceTimeout)
	defer cancel()

	tx, err := db.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("payments: begin settle for requester %s: %w", uid, err)
	}
	// Nothing is written through tx — every write below goes through the pool,
	// deliberately, for the deadlock reason above. The transaction exists only
	// to scope the advisory lock, so this rollback IS the unlock.
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Blocks until whoever else is settling this requester has finished.
	if err := lockPerUser(ctx, tx, advisoryLockBalanceSettle, uid); err != nil {
		return out, err
	}

	// The second half of double-checked locking, and the branch that makes the
	// whole mechanism work: the winner settled these rows while we were
	// queued, so they are no longer balance_due and there is nothing left to
	// charge. The loser of a double-tap returns "settled 0, you owe nothing"
	// rather than a decline.
	//
	// Correct only because the transaction is READ COMMITTED (pgx's default):
	// this statement takes a fresh snapshot after the lock was granted, so it
	// sees the winner's committed writes. Under REPEATABLE READ it would read
	// the pre-lock snapshot and charge everything a second time.
	owed, err := readBalanceDue(ctx, tx, uid)
	if err != nil {
		return out, err
	}
	if len(owed) == 0 {
		log.Printf("[payments][settle] requester=%s has nothing left to charge — already collected, or a concurrent pass got there first", uid)
		return out, nil
	}

	for _, d := range owed {
		p := &Payment{ID: d.id, TaskID: d.taskID, RequesterID: uid, StripePaymentIntentID: d.intentID}
		pi, err := chargeBalanceOffSession(ctx, p, d.taskID, uid, d.cents)
		if err == nil {
			if recErr := recordCapture(ctx, d.id, int(pi.AmountReceived), 0, 0, chargeIDFromIntent(pi)); recErr != nil {
				log.Printf("[payments][settle][ERROR] charged %s but did not record %s: %v",
					pi.ID, d.id, recErr)
			}
			writeAudit(ctx, d.taskID, uid, "PAYMENT_BALANCE_SETTLED", "", map[string]any{
				"payment_id":   d.id,
				"amount_cents": d.cents,
			})
			// A charge happened; the requester gets the same receipt they would
			// have got had it gone through at completion.
			sendRequesterReceipt(ctx, d.taskID, []receiptCharge{{
				Label: "Outstanding balance", PaymentID: d.id, Cents: d.cents,
			}})
			out.settledCents += d.cents
			out.settledCount++
			continue
		}

		out.stoppedAt = d.id
		var pe *PreAuthError
		if errors.As(err, &pe) && pe.RequiresAction {
			out.authErr = pe
			return out, nil
		}

		out.declineMessage = "That card was declined. Try another card."
		if pe != nil && pe.Message != "" {
			out.declineMessage = pe.Message
		}
		log.Printf("[payments][settle] requester=%s payment=%s still failing: %v", uid, d.id, err)
		return out, nil
	}
	return out, nil
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
