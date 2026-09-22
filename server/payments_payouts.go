package main

// Phase 3, the far half: moving the money.
//
// payments_connect.go got the supporter an account. This file puts money in
// it, and it is the last leg of the loop the product has been describing since
// Phase 1:
//
//	post        CreatePreAuth   hold the estimate + budget on the card    [2a]
//	completion  Capture         take time cost + verified receipt          [2b]
//	completion  payoutForTask   transfer that to the supporter            [3]
//
// SEPARATE CHARGES AND TRANSFERS, not destination charges. The charge lands on
// the platform and a separate Transfer moves it out later. The alternative —
// naming the supporter on the PaymentIntent at post time — is impossible here
// by construction: the hold is placed when the task is POSTED, and at that
// moment there is no supporter. Nobody has accepted it yet. A destination
// charge would require knowing the payee before anyone had volunteered to be
// one.
//
// THE PLATFORM CUT IS NOT AN APPLICATION FEE. With separate charges and
// transfers, Stripe's application_fee_amount does not apply — it is only valid
// on direct and destination charges, where the charge itself is attached to a
// connected account. Here the cut is simply the money that does not get
// transferred: it stays on the platform balance because nothing moved it.
// BillingConfig.ApplicationFeeBasisPoints is zero during beta, so the
// supporter receives the whole settlement.
//
// SOURCE_TRANSACTION IS LOAD-BEARING. A bare Transfer is paid out of the
// platform's AVAILABLE balance and fails outright when that balance is short —
// which it always is in test mode, and frequently is in production, because a
// card charge does not become available for days. Tying the transfer to the
// funding charge with source_transaction makes the request succeed regardless
// of available balance and makes Stripe execute it when those exact funds
// settle. That is also the cleanest possible statement of the ordering rule
// this phase needs: the supporter is paid out of the requester's money, not
// out of the platform's float, and never before it arrives.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/transfer"

	"hora-auth/internal/notify"
)

// Payout statuses. Enforced by a CHECK on the table; the Go constants live
// beside the only code that writes them.
const (
	// The row exists, the Stripe call has not succeeded yet. Written before
	// the API call for the same reason payments rows are — see below.
	payoutStatusPending = "pending"
	// Stripe accepted the transfer. NOT "the money is in their bank": with
	// source_transaction the transfer executes when the funding charge
	// settles, and the bank deposit is a separate payout on Stripe's daily
	// schedule after that. 'paid' means we have done everything we can do.
	payoutStatusPaid = "paid"
	// The transfer was refused, or was reversed after the fact. Ops are
	// emailed and POST /admin/payouts/:id/retry is the way back.
	payoutStatusFailed = "failed"
)

// Payout mirrors one public.payouts row.
type Payout struct {
	ID               string
	TaskID           string
	SupporterID      string
	PaymentID        string
	AmountCents      int
	StripeTransferID string
	Status           string
	AttemptCount     int
}

// payoutInput is everything a transfer needs, gathered by the settlement path
// so that this file reads no pricing and decides no amounts of its own.
type payoutInput struct {
	TaskID      string
	SupporterID string
	// The CAPTURED payments row funding this transfer. One payout per payment,
	// enforced by a unique index — see the migration.
	PaymentID string
	// What the supporter is owed out of THIS payment, gross, before the
	// platform cut. Already decided by the settlement; nothing here re-prices.
	OwedCents int
	// The Stripe charge this money actually came from. Becomes
	// source_transaction, which is what lets the transfer succeed against a
	// platform balance that has not settled yet.
	SourceChargeID string
	// Non-zero when the settlement collected less than the supporter is owed —
	// a completion_balance that failed. Recorded on the row rather than
	// silently absorbed; see the note in payoutForTask.
	ShortfallCents int
}

// ── What a settlement owes the supporter ───────────────────────────────────

// payoutSplit is one transfer a settlement produces: how much, funded by which
// captured payment, and how much of the supporter's money this transfer does
// NOT carry.
type payoutSplit struct {
	// Which captured payment funds it — 'hold' or 'balance'. Named rather than
	// ordered so a test failure says which one is wrong.
	Source string
	// Gross, before the platform cut. payoutForTask applies the cut.
	OwedCents int
	// Money the supporter is owed that this transfer cannot carry, because the
	// requester's balance charge failed and it was never collected.
	ShortfallCents int
}

const (
	payoutSourceHold    = "hold"
	payoutSourceBalance = "balance"
)

// settlementPayouts decides what transfers a completed settlement produces.
//
// THE WHOLE PHASE'S ARITHMETIC IS HERE, in one pure function, because every
// interesting case is a small integer relationship that a reader should be
// able to check by eye — and because the alternative, deciding it inline in
// settleTaskPayment next to two Stripe calls and an email, is where the
// off-by-one that underpays somebody would live.
//
// Three inputs:
//
//	total           what the supporter is owed: time cost + reimbursed receipt
//	mainCaptured    what the HOLD actually took (clamped to the authorization)
//	balanceCaptured what a completion_balance charge collected, or 0
//
// And the one constraint that shapes the answer: a transfer tied to a funding
// charge by source_transaction MAY NOT EXCEED THAT CHARGE. So the hold's
// transfer is capped at what the hold captured, and anything above it rides on
// the balance charge's own transfer — which is why an overrunning task
// produces two transfers rather than one.
//
//	total <= mainCaptured    one transfer for `total`; the hold covered it and
//	                         Stripe released the rest of the authorization
//	balance collected        two transfers: the hold in full, then the rest
//	balance failed           one transfer for what the hold took, with the gap
//	                         recorded as a shortfall. The supporter is short
//	                         and the platform is carrying the receivable —
//	                         which is NOT silently transferred, because a
//	                         source_transaction-bound transfer cannot exceed
//	                         its charge and an unbound one would spend platform
//	                         float nobody approved. Recovery is the requester
//	                         settling their balance (markBalanceDue) or an ops
//	                         person deciding otherwise.
func settlementPayouts(total, mainCaptured, balanceCaptured int) []payoutSplit {
	if total <= 0 || mainCaptured <= 0 {
		return nil
	}

	fromHold := minInt(total, mainCaptured)
	remainder := total - fromHold

	// The gap is what is owed, not collected, and not covered by the balance
	// transfer below.
	shortfall := remainder - minInt(remainder, balanceCaptured)

	splits := []payoutSplit{{
		Source:         payoutSourceHold,
		OwedCents:      fromHold,
		ShortfallCents: shortfall,
	}}
	if carried := minInt(remainder, balanceCaptured); carried > 0 {
		splits = append(splits, payoutSplit{Source: payoutSourceBalance, OwedCents: carried})
	}
	return splits
}

// ── Creating the transfer ──────────────────────────────────────────────────

// payoutForTask transfers one captured payment's worth of settlement to the
// supporter's connected account.
//
// NEVER returns an error the caller should act on. Same rule the whole
// settlement path bends around: a money problem is never a supporter's
// problem, and by the time this runs the task is already complete and the
// requester has already been charged. A failed transfer is an ops ticket, not
// a reason to unwind anything.
//
// WHAT IS TRANSFERRED, exactly: OwedCents minus the platform cut. When the
// hold did not cover the settlement and the balance charge succeeded, the
// balance is a second captured payment and gets its own payout row — so the
// supporter ends up whole across two transfers, each tied to the charge that
// funded it.
//
// WHEN THE BALANCE CHARGE FAILED, the supporter is short and the platform is
// carrying the receivable. This does NOT silently transfer the uncollected
// difference: a source_transaction-bound transfer may not exceed its funding
// charge, and an unbound one would be paid out of a platform balance that in
// test mode is empty and in production is not ours to spend without a
// decision. So the transfer is for what was actually captured and the gap is
// stamped on the row as meta.shortfall_cents, where the admin retry endpoint
// and an ops person can both see it. The requester is separately blocked from
// posting until they settle (markBalanceDue), which is the mechanism that
// actually recovers it.
func payoutForTask(ctx context.Context, in payoutInput) *Payout {
	if !paymentsEnabled() {
		return nil
	}
	if in.OwedCents <= 0 || in.TaskID == "" || in.SupporterID == "" || in.PaymentID == "" {
		return nil
	}

	accountID, err := supporterAccountID(ctx, in.SupporterID)
	if err != nil || accountID == "" {
		// A completed task whose supporter never onboarded. Possible whenever
		// PAYMENTS_ENFORCED was off when they accepted — which is every task
		// in the running beta — so this is a normal, expected outcome and not
		// an error. Ops are told because somebody still has to be paid.
		log.Printf("[payments][payout] task=%s supporter=%s has no connected account — nothing transferred",
			in.TaskID, in.SupporterID)
		reportUnpayableSupporter(ctx, in)
		return nil
	}

	amount := in.OwedCents - platformCutCents(in.OwedCents)
	if amount <= 0 {
		log.Printf("[payments][payout] task=%s owed=%d nets to zero after the platform cut — nothing to send",
			in.TaskID, in.OwedCents)
		return nil
	}

	// The row is written BEFORE the Stripe call, exactly as payments rows are,
	// and for the mirror-image reason: a transfer that exists at Stripe with
	// no row naming it is money that left the platform and cannot be
	// reconciled, where a row naming a transfer that may not exist is merely
	// untidy and is what the retry endpoint is for.
	//
	// The insert is also the idempotency claim. payouts.payment_id is UNIQUE,
	// so a second settle of the same payment — a retried request, a
	// redelivered webhook, an admin double-tap — loses here, atomically,
	// before reaching Stripe at all.
	p, err := insertPayout(ctx, in, amount)
	if errors.Is(err, errPayoutExists) {
		log.Printf("[payments][payout] payment=%s already has a payout — not sending a second", in.PaymentID)
		return nil
	}
	if err != nil {
		log.Printf("[payments][payout][ERROR] task=%s could not record a %s payout: %v",
			in.TaskID, formatCentsUSD(amount), err)
		return nil
	}

	tr, err := sendTransfer(ctx, p, accountID, in)
	if err != nil {
		markPayoutFailed(ctx, p, in, err)
		p.Status = payoutStatusFailed
		return p
	}

	if err := recordPayoutSent(ctx, p.ID, tr.ID, destinationPaymentOf(tr)); err != nil {
		log.Printf("[payments][payout][ERROR] sent transfer=%s but did not record payout=%s: %v",
			tr.ID, p.ID, err)
	}
	log.Printf("[payments][payout] task=%s supporter=%s payout=%s transfer=%s amount=%s group=%s",
		in.TaskID, in.SupporterID, p.ID, tr.ID, formatCentsUSD(amount), in.TaskID)

	p.Status = payoutStatusPaid
	p.StripeTransferID = tr.ID
	notifySupporterPaid(ctx, in, amount)
	return p
}

// platformCutCents is the marketplace take on one settlement.
//
// Zero during beta (BillingConfig.ApplicationFeeBasisPoints). Parameterized
// here rather than inlined so that turning it on is a config edit in
// billing.go and not a formula change in a payments file — and so there is
// exactly one place the arithmetic lives, which is the same rule the rest of
// the billing engine follows (S-05).
func platformCutCents(owedCents int) int {
	if Billing.ApplicationFeeBasisPoints <= 0 {
		return 0
	}
	return owedCents * Billing.ApplicationFeeBasisPoints / 10000
}

// sendTransfer is the Stripe call, and the three parameters that matter.
func sendTransfer(ctx context.Context, p *Payout, accountID string, in payoutInput) (*stripe.Transfer, error) {
	params := &stripe.TransferParams{
		Amount:      stripe.Int64(int64(p.AmountCents)),
		Currency:    stripe.String(Billing.Currency),
		Destination: stripe.String(accountID),
		// The task id, on both the charge and the transfer, so a Stripe
		// dashboard row on either side names the business action it belongs to
		// without a lookup. Set on the PaymentIntent at pre-auth
		// (payments.go) so the two agree by construction.
		TransferGroup: stripe.String(in.TaskID),
		Description:   stripe.String(fmt.Sprintf("HO:RA task %s", in.TaskID)),
		Metadata: map[string]string{
			"task_id":      in.TaskID,
			"supporter_id": in.SupporterID,
			"payment_id":   in.PaymentID,
			"payout_id":    p.ID,
		},
	}
	// See the file header. Without this the transfer is drawn on the
	// platform's available balance and fails whenever the funding charge has
	// not settled — which is always, in test mode, and often in production.
	if in.SourceChargeID != "" {
		params.SourceTransaction = stripe.String(in.SourceChargeID)
	}
	params.SetIdempotencyKey(transferIdempotencyKey(p.ID, p.AttemptCount))

	tr, err := transfer.New(params)
	if err != nil {
		return nil, err
	}
	_ = ctx
	return tr, nil
}

// transferIdempotencyKey keys a transfer on the payout row AND the attempt,
// and both halves are load-bearing.
//
// The payout id is what makes a retried settle idempotent: the same row asks
// Stripe for the same transfer and gets the first one back rather than a
// second.
//
// The attempt counter is what makes an ADMIN RETRY actually retry. Stripe
// caches a failed call's ERROR against its idempotency key for 24 hours, so a
// retry reusing the first key is answered with the first failure — forever,
// from the operator's point of view. Advancing the counter asks a genuinely
// new question.
//
// That is only safe because a failed attempt created nothing at Stripe. A
// SUCCESSFUL one sets stripe_transfer_id, and adminRetryPayout refuses any row
// that has one — so a changing key can never become a second payment.
func transferIdempotencyKey(payoutID string, attempt int) string {
	return fmt.Sprintf("transfer_%s_%d", payoutID, attempt)
}

// ── Rows ───────────────────────────────────────────────────────────────────

// errPayoutExists is the unique-index violation, named so the caller can tell
// "somebody already paid this" from "the database is broken". The two need
// opposite responses and both arrive as a generic insert error otherwise.
var errPayoutExists = errors.New("payouts: this payment already has a payout")

func insertPayout(ctx context.Context, in payoutInput, amount int) (*Payout, error) {
	meta := map[string]any{}
	if in.ShortfallCents > 0 {
		// The supporter is owed more than this transfer carries because the
		// requester's balance charge failed. Recorded here so the gap is
		// visible on the payout itself rather than only inferable by joining
		// two tables and knowing which statuses to look for.
		meta["shortfall_cents"] = in.ShortfallCents
		meta["shortfall_reason"] = "completion_balance_uncollected"
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		raw = []byte("{}")
	}

	var p Payout
	err = db.QueryRow(ctx, `
		insert into public.payouts
		       (task_id, supporter_id, payment_id, amount_cents, status, attempt_count, meta)
		values ($1::uuid, $2::uuid, $3::uuid, $4, $5, 1, $6::jsonb)
		on conflict (payment_id) do nothing
		returning id::text, task_id::text, supporter_id::text, payment_id::text,
		          amount_cents, status, attempt_count
	`, in.TaskID, in.SupporterID, in.PaymentID, amount, payoutStatusPending, string(raw)).
		Scan(&p.ID, &p.TaskID, &p.SupporterID, &p.PaymentID, &p.AmountCents, &p.Status, &p.AttemptCount)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned nothing: the unique index caught a
		// second payout for this payment.
		return nil, errPayoutExists
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// destinationPayment is the py_… the Transfer created on the connected
// account — the join key to the bank payout that will carry it (see
// payments_bank_arrival.go). Empty when Stripe did not include one; the
// reconciler fills it from the Transfer later.
func recordPayoutSent(ctx context.Context, payoutID, transferID, destinationPayment string) error {
	var py *string
	if destinationPayment != "" {
		py = &destinationPayment
	}
	_, err := db.Exec(ctx, `
		update public.payouts
		   set status = $2, stripe_transfer_id = $3,
		       stripe_destination_payment = coalesce($4, stripe_destination_payment),
		       updated_at = now()
		 where id = $1::uuid
	`, payoutID, payoutStatusPaid, transferID, py)
	return err
}

// supporterAccountID reads the connected account, or empty when there is none.
func supporterAccountID(ctx context.Context, uid string) (string, error) {
	var accountID *string
	if err := db.QueryRow(ctx,
		`select stripe_account_id from public.users where id = $1::uuid`, uid,
	).Scan(&accountID); err != nil {
		return "", err
	}
	if accountID == nil {
		return "", nil
	}
	return *accountID, nil
}

// ── When it goes wrong ─────────────────────────────────────────────────────

// markPayoutFailed records the failure and tells ops.
//
// A failed transfer is the one thing in this whole phase that needs a human:
// the requester has been charged, the task is closed, and somebody has worked
// and not been paid. Both halves of the alert matter for the same reason they
// do for disputes — the audit row nobody reads, the email nobody can find six
// weeks later.
func markPayoutFailed(ctx context.Context, p *Payout, in payoutInput, cause error) {
	if _, err := db.Exec(ctx, `
		update public.payouts
		   set status = $2,
		       meta = meta || jsonb_build_object('error', $3::text),
		       updated_at = now()
		 where id = $1::uuid
	`, p.ID, payoutStatusFailed, cause.Error()); err != nil {
		log.Printf("[payments][payout][ERROR] could not mark payout=%s failed: %v", p.ID, err)
	}

	log.Printf("[payments][PAYOUT FAILED] task=%s supporter=%s payout=%s amount=%s: %v",
		in.TaskID, in.SupporterID, p.ID, formatCentsUSD(p.AmountCents), cause)

	writeAudit(ctx, in.TaskID, systemActorUID, "PAYOUT_FAILED", "", map[string]any{
		"payout_id":    p.ID,
		"supporter_id": in.SupporterID,
		"payment_id":   in.PaymentID,
		"amount_cents": p.AmountCents,
		"error":        cause.Error(),
	})

	supporter := supporterLabel(ctx, in.SupporterID)
	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Payout failed — %s to %s", formatCentsUSD(p.AmountCents), supporter),
		fmt.Sprintf(`<p><strong>A supporter could not be paid.</strong></p>
<p>The task is complete and the requester has been charged. The money is sitting on the platform
balance and has not reached the supporter.</p>
<ul>
  <li>Task: %s</li>
  <li>Supporter: %s</li>
  <li>Amount: %s</li>
  <li>Payout row: %s</li>
  <li>Error: %s</li>
</ul>
<p>The usual cause is a connected account that is restricted or has requirements overdue — check it
in Stripe &rarr; Connect &rarr; Accounts. Once the account is good again, retry with
<code>POST /admin/payouts/%s/retry</code>.</p>`,
			in.TaskID, supporter, formatCentsUSD(p.AmountCents), p.ID, cause.Error(), p.ID))
}

// reportUnpayableSupporter is the no-account case: not a failure of anything,
// just a settlement with nowhere to send the money.
//
// Expected for every task in the running beta, where PAYMENTS_ENFORCED is off
// and no supporter has ever been asked to onboard. Logged and emailed rather
// than written to payouts, because there is no transfer to record — a payouts
// row with no amount and no destination would be a row about an absence.
func reportUnpayableSupporter(ctx context.Context, in payoutInput) {
	writeAudit(ctx, in.TaskID, systemActorUID, "PAYOUT_SKIPPED_NO_ACCOUNT", "", map[string]any{
		"supporter_id": in.SupporterID,
		"payment_id":   in.PaymentID,
		"owed_cents":   in.OwedCents,
	})
	supporter := supporterLabel(ctx, in.SupporterID)
	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Owed but not payable — %s to %s", formatCentsUSD(in.OwedCents), supporter),
		fmt.Sprintf(`<p><strong>A completed task owes a supporter who has no payout account.</strong></p>
<p>Normal while PAYMENTS_ENFORCED is off — nobody has been asked to set up payouts yet — and this is
the list of people to settle with by hand when it is flipped on.</p>
<ul>
  <li>Task: %s</li>
  <li>Supporter: %s</li>
  <li>Owed: %s</li>
</ul>`, in.TaskID, supporter, formatCentsUSD(in.OwedCents)))
}

// supporterLabel is "Name <email>" for an ops email, or the uid when the row
// is unreadable — an alert that cannot name anybody is still worth sending.
func supporterLabel(ctx context.Context, uid string) string {
	var email string
	if err := db.QueryRow(ctx,
		`select coalesce(email,'') from public.users where id = $1::uuid`, uid,
	).Scan(&email); err != nil {
		return uid
	}
	name := resolveDisplayName(ctx, email)
	switch {
	case name != "" && email != "":
		return fmt.Sprintf("%s &lt;%s&gt;", name, email)
	case email != "":
		return email
	default:
		return uid
	}
}

// notifySupporterPaid tells the supporter money is on its way.
//
// Task-scoped, so unlike the payouts-disabled alert this one IS a notification
// row (notifications.task_id is NOT NULL and this event has a real task behind
// it). Deliberately worded as "on its way" rather than "paid": with
// source_transaction the transfer executes when the funding charge settles,
// and the bank deposit is a further step on Stripe's daily payout schedule.
// Telling somebody the money is in their account when it is two days out is
// the kind of copy that generates support tickets.
func notifySupporterPaid(ctx context.Context, in payoutInput, amount int) {
	t, err := loadAdminTask(ctx, in.TaskID)
	if err != nil {
		return
	}
	var email string
	_ = db.QueryRow(ctx,
		`select coalesce(email,'') from public.users where id = $1::uuid`, in.SupporterID).Scan(&email)

	notifyUser(ctx, in.SupporterID, email, notify.CreateNotificationInput{
		TaskID: in.TaskID,
		Type:   "PAYOUT_SENT",
		Title:  fmt.Sprintf("%s on its way", formatCentsUSD(amount)),
		Body: fmt.Sprintf(
			"Your payment for %q is on its way to your bank. You can see it in Profile → Earnings.",
			t.Title),
		TaskTitle: t.Title,
	})
}

// ── Admin retry ────────────────────────────────────────────────────────────

// POST /admin/payouts/:id/retry
//
// The way back from a failed transfer. requireOpsAdmin and audited, like every
// other destructive admin action — and this one moves real money, so it is
// held to the tighter of the two standards: it refuses anything that is not
// unambiguously a retry of something that did not happen.
//
// THE DOUBLE-PAY GUARD IS THE STRIPE TRANSFER ID, not the status. A row could
// be marked failed by a webhook while a transfer exists (a reversal, say), and
// "status is failed" would happily pay it again. "No transfer id" cannot be
// true of a row where money moved.
func adminRetryPayout(c *gin.Context) {
	payoutID := strings.TrimSpace(c.Param("id"))
	actor := c.GetString("uid")
	ctx := c.Request.Context()

	var p Payout
	var transferID *string
	err := db.QueryRow(ctx, `
		select id::text, task_id::text, supporter_id::text, payment_id::text,
		       amount_cents, status, attempt_count, stripe_transfer_id
		  from public.payouts where id = $1::uuid
	`, payoutID).Scan(&p.ID, &p.TaskID, &p.SupporterID, &p.PaymentID,
		&p.AmountCents, &p.Status, &p.AttemptCount, &transferID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if transferID != nil && *transferID != "" {
		c.JSON(http.StatusConflict, gin.H{
			"error":              "already_transferred",
			"message":            "This payout already has a Stripe transfer. Reverse it in the dashboard rather than sending a second one.",
			"stripe_transfer_id": *transferID,
		})
		return
	}

	accountID, err := supporterAccountID(ctx, p.SupporterID)
	if err != nil || accountID == "" {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "no_connect_account",
			"message": "This supporter has not set up payouts yet. There is nowhere to send it.",
		})
		return
	}

	// The account has to be TRANSFERABLE, and "the cache says so" is not
	// enough for an action that moves money. readConnectStatus re-reads
	// Stripe when it can — the v1 account AND the v2 recipient capability,
	// see transfersActiveFor — and refreshes the cache on the way, so the
	// answer here is as current as Stripe's own. Refused without burning an
	// attempt: an attempt number is the only thing that makes the NEXT retry
	// a real retry, and spending one on a transfer Stripe is certain to
	// refuse would waste it.
	//
	// This is the guard the 2026-09-22 $12 payout lacked. The cache said
	// payable, the v1 account agreed, and the Transfer was refused anyway.
	st, err := readConnectStatus(ctx, p.SupporterID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !(st.PayoutsEnabled && st.TransfersActive) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "supporter_not_payable",
			"message": "Stripe cannot yet accept transfers into this supporter's account. " +
				"Nothing was sent. Retry once the account's transfers capability is active.",
			"onboarding_state": st.State,
			"payouts_enabled":  st.PayoutsEnabled,
			"transfers_active": st.TransfersActive,
			"requirements_due": st.RequirementsDue,
		})
		return
	}

	// Fail closed, AFTER the refusals above and BEFORE the counter bump. The
	// ordering is the point: an operator retrying a payout that must not be
	// resent deserves to be told that, whatever the deployment's Stripe
	// configuration — and an unconfigured Stripe must not burn an attempt on a
	// call that never had a chance, because the attempt number is the only
	// thing that makes the NEXT retry a real retry rather than a replay of a
	// cached failure.
	if !paymentsEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "payments_unavailable",
			"message": "Stripe is not configured on this deployment.",
		})
		return
	}

	// A fresh attempt number, which is what makes the new idempotency key
	// different from the one Stripe has a cached failure against.
	if err := db.QueryRow(ctx, `
		update public.payouts set attempt_count = attempt_count + 1, updated_at = now()
		 where id = $1::uuid returning attempt_count
	`, p.ID).Scan(&p.AttemptCount); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	in := payoutInput{
		TaskID:         p.TaskID,
		SupporterID:    p.SupporterID,
		PaymentID:      p.PaymentID,
		OwedCents:      p.AmountCents,
		SourceChargeID: chargeForPayment(ctx, p.PaymentID),
	}
	tr, err := sendTransfer(ctx, &p, accountID, in)
	if err != nil {
		log.Printf("[payments][payout] admin retry payout=%s attempt=%d failed: %v", p.ID, p.AttemptCount, err)
		writeAudit(ctx, p.TaskID, orSystemActor(actor), "PAYOUT_RETRY_FAILED", "", map[string]any{
			"payout_id": p.ID, "attempt": p.AttemptCount, "error": err.Error(),
		})
		c.JSON(http.StatusBadGateway, gin.H{"error": "transfer_failed", "message": err.Error()})
		return
	}

	if err := recordPayoutSent(ctx, p.ID, tr.ID, destinationPaymentOf(tr)); err != nil {
		log.Printf("[payments][payout][ERROR] retry sent transfer=%s but did not record payout=%s: %v",
			tr.ID, p.ID, err)
	}
	writeAudit(ctx, p.TaskID, orSystemActor(actor), "PAYOUT_RETRIED", "", map[string]any{
		"payout_id":          p.ID,
		"attempt":            p.AttemptCount,
		"amount_cents":       p.AmountCents,
		"stripe_transfer_id": tr.ID,
	})
	log.Printf("[payments][payout] admin retry payout=%s transfer=%s amount=%s by %s",
		p.ID, tr.ID, formatCentsUSD(p.AmountCents), actor)

	notifySupporterPaid(ctx, in, p.AmountCents)
	c.JSON(http.StatusOK, gin.H{
		"ok":                 true,
		"status":             payoutStatusPaid,
		"stripe_transfer_id": tr.ID,
		"amount_cents":       p.AmountCents,
	})
}

// chargeForPayment finds the Stripe charge behind a captured payment, so a
// retry is funded the same way the original attempt would have been.
//
// Stored on the payments row at capture (payments.stripe_charge_id). Empty for
// a row captured before that column existed, in which case the retry is drawn
// on the platform's available balance instead — which is correct for a manual
// ops action taken deliberately, and is why this is not the automatic path.
func chargeForPayment(ctx context.Context, paymentID string) string {
	var charge *string
	if err := db.QueryRow(ctx,
		`select stripe_charge_id from public.payments where id = $1::uuid`, paymentID,
	).Scan(&charge); err != nil || charge == nil {
		return ""
	}
	return *charge
}

// ── Webhook: the two ways a sent transfer still goes wrong ─────────────────

// onTransferReversed handles money coming back.
//
// A reversal means the funds were pulled back out of the supporter's connected
// account — most often because the charge that funded it was disputed or
// refunded, which Stripe resolves by reversing the transfer that spent it. The
// row goes to 'failed' because that is operationally what it is: this
// supporter has not been paid. It is NOT retried automatically; the reason the
// money came back is upstream of the transfer, and re-sending it would simply
// spend the platform's money on a charge that no longer exists.
func onTransferReversed(ctx context.Context, event *stripe.Event) error {
	var tr stripe.Transfer
	if err := json.Unmarshal(event.Data.Raw, &tr); err != nil {
		return fmt.Errorf("decode transfer: %w", err)
	}
	if tr.ID == "" {
		return fmt.Errorf("event %s carries no transfer id", event.ID)
	}

	var payoutID, taskID string
	var amount int
	err := db.QueryRow(ctx, `
		select id::text, task_id::text, amount_cents from public.payouts
		 where stripe_transfer_id = $1
	`, tr.ID).Scan(&payoutID, &taskID, &amount)
	if err != nil {
		log.Printf("[stripe][webhook] no payout row for transfer=%s — ignoring", tr.ID)
		return nil
	}

	if _, err := db.Exec(ctx, `
		update public.payouts
		   set status = $2,
		       meta = meta || jsonb_build_object('reversed_cents', $3::int),
		       updated_at = now()
		 where id = $1::uuid
	`, payoutID, payoutStatusFailed, tr.AmountReversed); err != nil {
		return fmt.Errorf("mark payout %s reversed: %w", payoutID, err)
	}

	log.Printf("[stripe][webhook][PAYOUT REVERSED] payout=%s transfer=%s task=%s reversed=%d",
		payoutID, tr.ID, taskID, tr.AmountReversed)
	writeAudit(ctx, taskID, systemActorUID, "PAYOUT_REVERSED", "", map[string]any{
		"payout_id":          payoutID,
		"stripe_transfer_id": tr.ID,
		"reversed_cents":     tr.AmountReversed,
		"amount_cents":       amount,
	})
	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Payout reversed — %s", formatCentsUSD(int(tr.AmountReversed))),
		fmt.Sprintf(`<p><strong>A transfer to a supporter has been reversed.</strong></p>
<p>The money has been pulled back out of their connected account and is on the platform balance.
This usually follows a dispute or refund of the charge that funded it.</p>
<ul>
  <li>Task: %s</li>
  <li>Payout row: %s</li>
  <li>Transfer: %s</li>
  <li>Reversed: %s of %s</li>
</ul>
<p>This is NOT retried automatically — the reason is upstream of the transfer. Check the funding
charge in Stripe &rarr; Payments before doing anything.</p>`,
			taskID, payoutID, tr.ID,
			formatCentsUSD(int(tr.AmountReversed)), formatCentsUSD(amount)))
	return nil
}

// onPayoutFailed handles the LAST leg: Stripe → the supporter's bank.
//
// Distinct from a failed transfer, and the distinction matters. A transfer is
// platform → connected account; a payout is connected account → bank, run on
// Stripe's daily automatic schedule, and this event means the bank rejected
// the deposit (closed account, wrong details). Our payouts rows are about
// transfers and are all still correct — the money IS in their Stripe balance.
// Nothing of ours is wrong, so nothing of ours is updated. What is needed is
// for the supporter to fix their bank details, which is a thing only they can
// do, in the Express dashboard.
//
// Arrives on the connected-accounts webhook endpoint, carrying the account id
// in the event's top-level `account` field rather than anywhere in the payout.
func onConnectedPayoutFailed(ctx context.Context, event *stripe.Event) error {
	var po stripe.Payout
	if err := json.Unmarshal(event.Data.Raw, &po); err != nil {
		return fmt.Errorf("decode payout: %w", err)
	}
	accountID := event.Account
	if accountID == "" {
		log.Printf("[stripe][webhook] payout.failed with no account — ignoring")
		return nil
	}

	var uid, email string
	if err := db.QueryRow(ctx, `
		select id::text, coalesce(email,'') from public.users where stripe_account_id = $1
	`, accountID).Scan(&uid, &email); err != nil {
		log.Printf("[stripe][webhook] no user for connect account=%s — ignoring payout.failed", accountID)
		return nil
	}

	log.Printf("[stripe][webhook][BANK PAYOUT FAILED] user=%s account=%s payout=%s amount=%d code=%s",
		uid, accountID, po.ID, po.Amount, po.FailureCode)
	writeAudit(ctx, systemActorUID, systemActorUID, "BANK_PAYOUT_FAILED", string(po.FailureCode),
		map[string]any{
			"user_id":           uid,
			"stripe_account_id": accountID,
			"stripe_payout_id":  po.ID,
			"amount_cents":      po.Amount,
			"failure_message":   po.FailureMessage,
		})

	// Stripe disables the external account on a failed payout, so nothing will
	// reach this person until they fix it. They are the only one who can.
	if email != "" {
		body := fmt.Sprintf(
			`<p><strong>Your bank rejected a payout.</strong></p>
<p>%s couldn't be deposited into your bank account, so payouts have been paused until the details are
updated. Your money is safe — it's still in your HO:RA payouts balance.</p>
<p>Open HO:RA &rarr; Profile &rarr; Earnings &rarr; <strong>Manage payouts</strong> and update your
bank account there.</p>`, formatCentsUSD(int(po.Amount)))
		go func() {
			if err := notify.SendEmail(notify.EmailPayload{
				To:      email,
				Subject: "[HO:RA] Your bank rejected a payout",
				Html:    body,
			}); err != nil {
				log.Printf("[payments][payout] bank-failure email failed to=%s: %v", email, err)
			}
		}()
	}

	emailOpsAdmins(
		fmt.Sprintf("[HO:RA] Bank payout failed — %s", formatCentsUSD(int(po.Amount))),
		fmt.Sprintf(`<p><strong>A supporter's bank rejected their payout.</strong></p>
<p>Stripe has disabled the external account; nothing will reach them until they update it in the
Express dashboard. They have been emailed. Nothing to do here unless they ask for help.</p>
<ul>
  <li>Supporter: %s</li>
  <li>Connected account: %s</li>
  <li>Amount: %s</li>
  <li>Reason: %s (%s)</li>
</ul>`, supporterLabel(ctx, uid), accountID, formatCentsUSD(int(po.Amount)),
			po.FailureCode, po.FailureMessage))
	return nil
}
