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
//	time     base fee + billable minutes x rate, clamped to the consented
//	         ceiling (billing.go, taskTimeCapMinutes)
//	receipt  the verified receipt, reimbursed up to approved budget + $5
//	total    the sum, clamped again to what is actually authorized
//
// The second clamp should never bite: preAuthAmountCents holds 1.5x the time
// estimate plus the budget plus $5, and ensureHoldCoversTask grows the hold
// whenever an approval raises either ceiling. If it does bite, that is money
// the platform cannot collect and it is logged as UNDERCAPTURE, loudly.
//
// WHY A SETTLEMENT CAN SPAN TWO HOLDS
//
// When a requester approves a budget increase that outgrows the original hold,
// the preferred fix is an incremental authorization on the existing intent:
// one hold, one line on the statement, no second charge. Most online card
// payments do not support it — it is largely a card-present feature — so the
// fallback is a second PaymentIntent for the shortfall. Settlement therefore
// captures the main hold first and the supplementary ones in order until the
// total is covered, then releases whatever is left over.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

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
}

func readSettlementInputs(ctx context.Context, taskID string) settlementInputs {
	in := settlementInputs{Category: taskCategory(ctx, taskID)}
	in.LoggedMinutes, _ = totalClosedMinutes(ctx, taskID)
	in.CapMinutes, in.Cap = taskTimeCapMinutes(ctx, taskID)
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
	return quoteSettlement(in.Category, in.LoggedMinutes, in.CapMinutes, in.ApprovedBudgetCents, in.ReceiptCents)
}

// projectedSettlementCents is the MOST this task could end up charging, used
// to size the hold: the full consented time plus the full approved budget plus
// the tolerance that is auto-approved on top of it.
//
// Deliberately the ceiling and not the current figure. Sizing the hold to time
// logged so far would mean re-authorizing every few minutes as a supporter
// works, which is a fresh decline risk on every one of them.
func projectedSettlementCents(ctx context.Context, taskID string) int {
	capMinutes, _ := taskTimeCapMinutes(ctx, taskID)
	var approvedBudget int
	_ = db.QueryRow(ctx,
		`select coalesce(shopping_budget_approved_cents, 0) from public.tasks where id=$1::uuid`,
		taskID).Scan(&approvedBudget)

	timeHalf := baseFeeCents(taskCategory(ctx, taskID))
	if capMinutes > 0 {
		timeHalf += timeCostCents(capMinutes)
	} else {
		// No recorded estimate, so no ceiling. Nothing sensible to project
		// beyond what is already held; ensureHoldCoversTask reads the zero as
		// "leave it alone".
		return 0
	}
	shoppingHalf := 0
	if approvedBudget > 0 {
		shoppingHalf = approvedBudget + Billing.OverageToleranceCents
	}
	return timeHalf + shoppingHalf
}

// ── Growing the hold ───────────────────────────────────────────────────────

// holdAdjustment is what ensureHoldCoversTask did, reported back to the client
// and written into the audit row. Method is the interesting field: it is the
// only place the codebase records whether a real card supported an incremental
// authorization, which is the question this phase exists to answer.
type holdAdjustment struct {
	// "none" — already covered; "incremental_authorization" — the existing
	// hold grew; "supplementary_payment" — a second hold was opened for the
	// shortfall; "failed" — neither worked and the platform is short.
	Method         string `json:"method"`
	ShortfallCents int    `json:"shortfall_cents"`
	AuthorizedNow  int    `json:"authorized_cents"`
	Err            string `json:"error,omitempty"`
}

// ensureHoldCoversTask makes the authorized total at least what the task can
// now settle at.
//
// Returns nil when there is nothing to do at all — payments disabled, or a
// task posted before enforcement that has no hold behind it. That is the
// common case in the running beta and is not a failure.
func ensureHoldCoversTask(ctx context.Context, taskID string) *holdAdjustment {
	if !paymentsEnabled() {
		return nil
	}
	main, err := livePaymentForTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		log.Printf("[payments][hold] task=%s read: %v", taskID, err)
		return nil
	}
	if main.Status != paymentStatusAuthorized || main.AuthorizedCents == nil {
		// A hold that has not landed yet cannot be incremented, and one that is
		// already captured is not this task's problem any more.
		return nil
	}

	needed := projectedSettlementCents(ctx, taskID)
	if needed <= 0 {
		return nil
	}
	covered := *main.AuthorizedCents + authorizedSupplementaryCents(ctx, taskID)
	if covered >= needed {
		return &holdAdjustment{Method: "none", AuthorizedNow: covered}
	}
	shortfall := needed - covered

	adj := &holdAdjustment{ShortfallCents: shortfall, AuthorizedNow: covered}

	// Preferred, where the account supports it: grow the existing hold. One
	// hold, one line on the requester's statement, no second charge.
	//
	// Gated on the same flag CreatePreAuth requests the capability under, and
	// skipped entirely when it is off — an intent created without
	// request_incremental_authorization can never be incremented, so trying
	// would be a guaranteed-to-fail API call sitting in the latency of a
	// requester's Approve tap. See stripeIncrementalAuthEnabled.
	if stripeIncrementalAuthEnabled() {
		// Stripe's Amount here is the NEW TOTAL the intent is authorized for,
		// not the delta — passing the delta would shrink a hold rather than
		// grow it, which the API would then reject for being below the current
		// amount.
		newTotal := *main.AuthorizedCents + shortfall
		params := &stripe.PaymentIntentIncrementAuthorizationParams{
			Amount: stripe.Int64(int64(newTotal)),
		}
		params.SetIdempotencyKey(fmt.Sprintf("increment_%s_%d", main.ID, newTotal))
		pi, incErr := paymentintent.IncrementAuthorization(main.StripePaymentIntentID, params)
		if incErr == nil {
			authorized := int(pi.Amount)
			if err := attachIntent(ctx, main.ID, pi.ID, paymentStatusAuthorized, &authorized); err != nil {
				log.Printf("[payments][hold][ERROR] intent=%s incremented to %d but row %s not updated: %v",
					pi.ID, authorized, main.ID, err)
			}
			adj.Method = "incremental_authorization"
			adj.AuthorizedNow = authorized + authorizedSupplementaryCents(ctx, taskID)
			log.Printf("[payments][hold] task=%s intent=%s incremented to %s",
				taskID, pi.ID, formatCentsUSD(authorized))
			return adj
		}
		log.Printf("[payments][hold] task=%s intent=%s cannot be incremented (%v) — opening a supplementary hold for %s",
			taskID, main.StripePaymentIntentID, incErr, formatCentsUSD(shortfall))
	} else {
		log.Printf("[payments][hold] task=%s needs %s more; opening a supplementary hold (incremental authorization not enabled on this account)",
			taskID, formatCentsUSD(shortfall))
	}

	supp, suppErr := createSupplementaryHold(ctx, taskID, main.RequesterID, shortfall)
	if suppErr != nil {
		adj.Method = "failed"
		adj.Err = suppErr.Error()
		// Not fatal to the approval — see applyApprovedExtension. The requester
		// said yes and the supporter may spend; the platform carries the gap
		// and settles it by hand if the capture comes up short.
		log.Printf("[payments][hold][ERROR][SHORTFALL] task=%s needs %s more and neither an increment nor a supplementary hold worked: %v",
			taskID, formatCentsUSD(shortfall), suppErr)
		writeAudit(ctx, taskID, systemActorUID, "PAYMENT_HOLD_SHORTFALL", "", map[string]any{
			"shortfall_cents": shortfall,
			"error":           suppErr.Error(),
		})
		return adj
	}

	adj.Method = "supplementary_payment"
	adj.AuthorizedNow = covered + derefIntOr(supp.AuthorizedCents, 0)
	return adj
}

// authorizedSupplementaryCents is what the task's extra holds add up to.
func authorizedSupplementaryCents(ctx context.Context, taskID string) int {
	var total int
	_ = db.QueryRow(ctx, `
		select coalesce(sum(authorized_cents), 0)
		  from public.payments
		 where task_id = $1::uuid and kind = $2 and status = $3
	`, taskID, paymentKindBudgetIncrease, paymentStatusAuthorized).Scan(&total)
	return total
}

// createSupplementaryHold authorizes a second PaymentIntent for the shortfall.
//
// Off-session against the same saved card the original hold used, resolved the
// same way createTask resolves it. A 3DS challenge here is a dead end — there
// is no requester in front of a screen at this moment, they are somewhere else
// having just tapped Approve on a notification — so a challenge is treated as
// a failure and the platform carries the gap rather than parking a half-open
// intent nobody will ever finish.
func createSupplementaryHold(ctx context.Context, taskID, requesterID string, amountCents int) (*Payment, error) {
	if amountCents <= 0 {
		return nil, errors.New("payments: supplementary hold needs a positive amount")
	}
	var email string
	if err := db.QueryRow(ctx,
		`select coalesce(email,'') from public.users where id = $1::uuid`, requesterID).Scan(&email); err != nil {
		return nil, fmt.Errorf("payments: requester %s: %w", requesterID, err)
	}
	payCtx, err := resolvePreAuthContext(ctx, requesterID, email)
	if err != nil {
		return nil, fmt.Errorf("payments: resolve card: %w", err)
	}

	p, err := insertPayment(ctx, taskID, requesterID, paymentKindBudgetIncrease, paymentStatusRequiresAuth, amountCents)
	if err != nil {
		return nil, fmt.Errorf("payments: record supplementary intent: %w", err)
	}

	params := &stripe.PaymentIntentParams{
		Amount:        stripe.Int64(int64(amountCents)),
		Currency:      stripe.String(Billing.Currency),
		CaptureMethod: stripe.String(string(stripe.PaymentIntentCaptureMethodManual)),
		Customer:      stripe.String(payCtx.CustomerID),
		PaymentMethod: stripe.String(payCtx.PaymentMethodID),
		Confirm:       stripe.Bool(true),
		OffSession:    stripe.Bool(true),
		Metadata: map[string]string{
			"task_id":      taskID,
			"requester_id": requesterID,
			"payment_id":   p.ID,
			"kind":         paymentKindBudgetIncrease,
		},
	}
	params.SetIdempotencyKey("supplementary_" + p.ID)

	pi, err := paymentintent.New(params)
	if err != nil {
		pe := classifyPreAuthError(err)
		if pe.PaymentIntentID != "" {
			_ = attachIntent(ctx, p.ID, pe.PaymentIntentID, paymentStatusFailed, nil)
		} else {
			_ = updatePaymentStatus(ctx, p.ID, paymentStatusFailed, nil)
		}
		return nil, fmt.Errorf("payments: supplementary hold: %w", pe)
	}
	if pi.Status != stripe.PaymentIntentStatusRequiresCapture {
		// Anything else — requires_action above all — is unusable here.
		_ = attachIntent(ctx, p.ID, pi.ID, paymentStatusFailed, nil)
		return nil, fmt.Errorf("payments: supplementary hold came back %q, not authorized", pi.Status)
	}

	authorized := int(pi.Amount)
	if err := attachIntent(ctx, p.ID, pi.ID, paymentStatusAuthorized, &authorized); err != nil {
		return nil, fmt.Errorf("payments: attach supplementary intent %s: %w", pi.ID, err)
	}
	log.Printf("[payments][hold] task=%s supplementary payment=%s intent=%s amount=%s",
		taskID, p.ID, pi.ID, formatCentsUSD(authorized))
	writeAudit(ctx, taskID, systemActorUID, "PAYMENT_SUPPLEMENTARY_HOLD", "", map[string]any{
		"payment_id":        p.ID,
		"stripe_payment_id": pi.ID,
		"authorized_cents":  authorized,
	})

	p.StripePaymentIntentID = pi.ID
	p.Status = paymentStatusAuthorized
	p.AuthorizedCents = &authorized
	return p, nil
}

// ── Settling ───────────────────────────────────────────────────────────────

// settlementOutcome is what happened to the money when a task closed.
type settlementOutcome struct {
	// Attempted is false when the task had no hold at all — every task posted
	// with PAYMENTS_ENFORCED off. Nothing failed; there was nothing to do.
	Attempted bool
	// CapturedCents is what actually moved, across every hold on the task.
	CapturedCents int
	// TimeCostCents / ReceiptCents is the split that was recorded.
	TimeCostCents int
	ReceiptCents  int
	// Err is set when the capture did not go through. The task is still
	// completed; the payment row is in capture_failed.
	Err error
}

// settleTaskPayment captures a finished task's settlement across its holds.
//
// ORDER: the main hold first, supplementary holds after, oldest first, until
// the total is covered. Then every remaining uncaptured hold on the task is
// released, because a supplementary authorization that turned out not to be
// needed is money sitting on somebody's card for no reason.
//
// NEVER returns an error the caller should act on by refusing the completion.
// The error is reported so it can be logged, audited and emailed, not so the
// task can be rolled back.
func settleTaskPayment(ctx context.Context, taskID string, timeCostCents, receiptCents int) settlementOutcome {
	out := settlementOutcome{TimeCostCents: timeCostCents, ReceiptCents: receiptCents}
	if !paymentsEnabled() {
		return out
	}

	main, err := livePaymentForTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No live hold. Either the task predates enforcement, or the hold was
		// already released or captured. Nothing owed here.
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
	// logs UNDERCAPTURE if the clamp bites, and records the split. Doing the
	// clamp here as well would be two places deciding one number.
	captured, err := Capture(ctx, taskID, timeCostCents, receiptCents)
	if err != nil {
		out.Err = err
		markCaptureFailed(ctx, main.ID, err)
		return out
	}
	out.CapturedCents = derefIntOr(captured.CapturedCents, 0)

	// Whatever the main hold could not cover comes out of the supplementary
	// holds an approved increase opened.
	if remainder := total - out.CapturedCents; remainder > 0 {
		out.CapturedCents += captureSupplementaryHolds(ctx, taskID, remainder)
	}

	releaseSupplementaryHolds(ctx, taskID)

	if out.CapturedCents < total {
		log.Printf("[payments][UNDERCAPTURE] task=%s settled at %s of %s owed",
			taskID, formatCentsUSD(out.CapturedCents), formatCentsUSD(total))
	}
	return out
}

// captureSupplementaryHolds takes up to `remainder` cents from the task's
// extra holds, oldest first. Returns what it actually took.
//
// A failure on one hold is logged and skipped rather than aborting: the money
// already captured from the main hold has moved, and giving up on the rest
// would leave more uncollected than trying the next one.
func captureSupplementaryHolds(ctx context.Context, taskID string, remainder int) int {
	rows, err := db.Query(ctx, `
		select id::text, coalesce(stripe_payment_intent_id,''), coalesce(authorized_cents, 0)
		  from public.payments
		 where task_id = $1::uuid and kind = $2 and status = $3
		 order by created_at asc
	`, taskID, paymentKindBudgetIncrease, paymentStatusAuthorized)
	if err != nil {
		log.Printf("[payments][settle] supplementary holds task=%s: %v", taskID, err)
		return 0
	}
	type hold struct {
		id, intentID string
		authorized   int
	}
	var holds []hold
	for rows.Next() {
		var h hold
		if err := rows.Scan(&h.id, &h.intentID, &h.authorized); err != nil {
			log.Printf("[payments][settle] scan task=%s: %v", taskID, err)
			break
		}
		holds = append(holds, h)
	}
	rows.Close()

	taken := 0
	for _, h := range holds {
		if remainder <= 0 {
			break
		}
		amount := remainder
		if amount > h.authorized {
			amount = h.authorized
		}
		params := &stripe.PaymentIntentCaptureParams{AmountToCapture: stripe.Int64(int64(amount))}
		params.SetIdempotencyKey("capture_" + h.id)
		pi, err := paymentintent.Capture(h.intentID, params)
		if err != nil {
			log.Printf("[payments][settle][ERROR] supplementary capture task=%s payment=%s: %v", taskID, h.id, err)
			markCaptureFailed(ctx, h.id, err)
			continue
		}
		// The whole of a supplementary hold is shopping money by construction:
		// it exists only because an approved budget increase outgrew the
		// original hold.
		if err := recordCapture(ctx, h.id, int(pi.AmountReceived), 0, amount); err != nil {
			log.Printf("[payments][settle][ERROR] captured %s but did not record payment=%s: %v",
				h.intentID, h.id, err)
		}
		log.Printf("[payments][settle] supplementary capture task=%s payment=%s amount=%s",
			taskID, h.id, formatCentsUSD(amount))
		taken += amount
		remainder -= amount
	}
	return taken
}

// releaseSupplementaryHolds cancels every extra hold still standing after
// settlement. An unused authorization is real money frozen on a real card.
func releaseSupplementaryHolds(ctx context.Context, taskID string) {
	rows, err := db.Query(ctx, `
		select id::text, coalesce(stripe_payment_intent_id,'')
		  from public.payments
		 where task_id = $1::uuid and kind = $2 and status in ($3, $4)
	`, taskID, paymentKindBudgetIncrease, paymentStatusAuthorized, paymentStatusRequiresAuth)
	if err != nil {
		log.Printf("[payments][settle] leftover holds task=%s: %v", taskID, err)
		return
	}
	type leftover struct{ id, intentID string }
	var leftovers []leftover
	for rows.Next() {
		var l leftover
		if err := rows.Scan(&l.id, &l.intentID); err != nil {
			break
		}
		leftovers = append(leftovers, l)
	}
	rows.Close()

	for _, l := range leftovers {
		if l.intentID != "" {
			params := &stripe.PaymentIntentCancelParams{}
			params.SetIdempotencyKey("release_" + l.id)
			if _, err := paymentintent.Cancel(l.intentID, params); err != nil {
				log.Printf("[payments][settle][ERROR][STRANDED HOLD] task=%s payment=%s not released: %v",
					taskID, l.id, err)
				continue
			}
		}
		if err := updatePaymentStatus(ctx, l.id, paymentStatusCanceled, nil); err != nil {
			log.Printf("[payments][settle][ERROR] released %s but did not record: %v", l.intentID, err)
		}
	}
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
