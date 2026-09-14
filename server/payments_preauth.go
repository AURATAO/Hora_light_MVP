package main

// Phase 2a: the hold, wired into posting.
//
// Phase 1 left CreatePreAuth/Capture/Release complete and called by nothing.
// This file is the wiring — the feature flag that decides whether posting
// needs money at all, the translation of a Stripe failure into something a
// requester can act on, and the two lifecycle hooks (post places a hold,
// cancel releases it).
//
// THE FLAG. PAYMENTS_ENFORCED is off by default and posting behaves exactly as
// it did before this file existed: no card required, no hold, no payments row.
// That is not a stub — it is the running beta's behaviour, and it stays the
// default until someone deliberately flips it. Every payment branch below is
// downstream of paymentsEnforced(), so "off" is one condition rather than a
// state the code has to be trusted to reproduce.
//
// WHAT A FAILED HOLD MEANS. It means no task. A task that exists without a
// hold behind it is a supporter travelling to a job that cannot pay them, so
// the post is refused with a 402 the client can render in place — and refused
// completely: no task row, no payments row, nothing for anyone to trip over
// later. The one exception is a card that wants its owner present (3DS), which
// is not a refusal but a pause; see the pending_payment status.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/paymentintent"
)

// taskStatusPendingPayment is a task that exists only so its hold can name it.
// It is in no feed, no list and no profile — see the migration for why the row
// has to exist before the Stripe call at all.
const taskStatusPendingPayment = "pending_payment"

// paymentsEnforced reports whether posting a task requires a card and a hold.
//
// Read on every call rather than cached at startup: flipping this is a
// deliberate, watched operation, and an operator who sets the variable and
// restarts should not also have to wonder whether some other process is still
// running the old value. It is the cheapest possible read.
func paymentsEnforced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PAYMENTS_ENFORCED"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// stripePublishableKey is the client-side key, read from the environment on
// every call. Public by design (S-12) — it identifies the account and can do
// nothing without a client secret alongside it. Sent from the backend rather
// than built into each client so that rotating it is one env change instead of
// a web deploy plus a native rebuild.
func stripePublishableKey() string {
	return strings.TrimSpace(os.Getenv("STRIPE_PUBLISHABLE_KEY"))
}

// ── Turning a Stripe failure into an answer ────────────────────────────────

// PreAuthError is a failed hold, classified.
//
// The distinction that matters is RequiresAction: everything else means the
// card will not pay and the post is refused, while that one means the card
// will pay as soon as its owner taps through a challenge. Collapsing the two
// into "declined" is the bug this type exists to prevent — a 3DS card is
// perfectly good and telling its owner otherwise leaves them with no path
// forward at all.
type PreAuthError struct {
	// Code is Stripe's error code ("card_declined", "authentication_required").
	Code string
	// DeclineCode is the issuer's reason, when they gave one
	// ("insufficient_funds", "lost_card"). Frequently empty.
	DeclineCode string
	// Message is safe to show a requester: what happened and what to do.
	Message string

	RequiresAction  bool
	PaymentIntentID string
	// ClientSecret is present only when RequiresAction — it is what the client
	// hands to the Stripe SDK to run the challenge.
	ClientSecret string

	// PaymentID is our row, filled in by CreatePreAuth so the caller can clean
	// it up without a second lookup.
	PaymentID string

	wrapped error
}

func (e *PreAuthError) Error() string {
	if e.DeclineCode != "" {
		return fmt.Sprintf("pre-auth failed: %s (%s)", e.Code, e.DeclineCode)
	}
	if e.Code != "" {
		return fmt.Sprintf("pre-auth failed: %s", e.Code)
	}
	return fmt.Sprintf("pre-auth failed: %v", e.wrapped)
}

func (e *PreAuthError) Unwrap() error { return e.wrapped }

// classifyPreAuthError reads a stripe-go error into the shape above.
//
// Anything that is not a *stripe.Error — a timeout, a DNS failure, a bug —
// lands here too, as a non-actionable failure with a generic message. That is
// correct: from the requester's side "we could not reach the card network" and
// "your bank said no" both mean the post did not happen, and only the log
// needs to know which.
func classifyPreAuthError(err error) *PreAuthError {
	pe := &PreAuthError{wrapped: err}

	var se *stripe.Error
	if !errors.As(err, &se) {
		pe.Message = "We couldn't reach your bank just now. Please try again in a moment."
		return pe
	}

	pe.Code = string(se.Code)
	pe.DeclineCode = string(se.DeclineCode)
	if se.PaymentIntent != nil {
		pe.PaymentIntentID = se.PaymentIntent.ID
		pe.ClientSecret = se.PaymentIntent.ClientSecret
	}
	// Two spellings of the same condition. `authentication_required` is what an
	// off-session confirm returns when the issuer wants 3DS; the status check
	// catches an intent that stopped at the challenge without the error code
	// naming it.
	pe.RequiresAction = se.Code == stripe.ErrorCodeAuthenticationRequired ||
		(se.PaymentIntent != nil && se.PaymentIntent.Status == stripe.PaymentIntentStatusRequiresAction)
	if pe.RequiresAction {
		pe.Message = "Your bank needs to confirm this payment. Approve it to post your task."
		return pe
	}

	pe.Message = declineMessage(se)
	return pe
}

// declineMessage is what the requester reads.
//
// Stripe's own se.Msg is written for the developer ("Your card was declined.")
// and is not wrong, but it never says what to do next — and "try another card"
// is the only useful instruction for most of these. The codes below are the
// ones that actually occur on US cards in volume; everything else falls
// through to Stripe's wording rather than being flattened into a guess.
//
// Deliberately vague on fraud outcomes. `lost_card`, `stolen_card` and
// `fraudulent` must NOT be spelled out to the person holding the card: if the
// card really is stolen, the message is a tip-off, and the issuer's own
// guidance is to say nothing beyond "declined". Same generic wording as a
// plain decline.
func declineMessage(se *stripe.Error) string {
	switch se.DeclineCode {
	case "insufficient_funds":
		return "Your card doesn't have enough available funds for the hold on this task. Try another card."
	case "expired_card":
		return "That card has expired. Add a different card to post."
	case "incorrect_cvc", "invalid_cvc":
		return "That card's security code wasn't accepted. Add the card again, or try another."
	case "card_not_supported", "currency_not_supported":
		return "That card can't be used for this kind of payment. Try another card."
	case "lost_card", "stolen_card", "fraudulent", "pickup_card":
		return "Your card was declined. Try another card, or contact your bank."
	case "do_not_honor", "generic_decline", "transaction_not_allowed", "service_not_allowed":
		return "Your bank declined the hold for this task. Try another card, or contact your bank."
	}

	switch se.Code {
	case stripe.ErrorCodeCardDeclined:
		return "Your card was declined. Try another card, or contact your bank."
	case stripe.ErrorCodeExpiredCard:
		return "That card has expired. Add a different card to post."
	case stripe.ErrorCodeIncorrectCVC:
		return "That card's security code wasn't accepted. Add the card again, or try another."
	case stripe.ErrorCodeProcessingError:
		return "Your bank couldn't process the hold just now. Please try again in a moment."
	}

	if se.Msg != "" {
		return se.Msg
	}
	return "We couldn't place a hold on your card. Try another card."
}

// stripeUserMessage is the same translation for the non-pre-auth endpoints
// (saving and removing a card), which have no intent and no decline code.
func stripeUserMessage(err error) string {
	var se *stripe.Error
	if errors.As(err, &se) {
		return declineMessage(se)
	}
	return "Something went wrong talking to our payment provider. Please try again."
}

// ── Placing the hold at post time ──────────────────────────────────────────

// preAuthContext is everything the post path needs before it writes a task
// row: which customer pays and with which card. Resolved BEFORE the task is
// created so that "this requester has no card" costs nothing but a 402.
type preAuthContext struct {
	CustomerID      string
	PaymentMethodID string
}

var errNoCardOnFile = errors.New("payments: requester has no saved card")

// resolvePreAuthContext finds the requester's Customer and charging card.
func resolvePreAuthContext(ctx context.Context, uid, email string) (preAuthContext, error) {
	customerID, err := stripeCustomerFor(ctx, uid, email)
	if err != nil {
		return preAuthContext{}, err
	}
	pmID, err := defaultPaymentMethodFor(customerID)
	if err != nil {
		return preAuthContext{}, err
	}
	if pmID == "" {
		return preAuthContext{}, errNoCardOnFile
	}
	return preAuthContext{CustomerID: customerID, PaymentMethodID: pmID}, nil
}

// promoteTaskToOpen is the single statement that posts a task and records
// which hold funds it.
//
// One UPDATE, conditional on the task still being pending_payment, so a retry
// or a racing webhook cannot post the same task twice. RowsAffected of zero
// means somebody got there first, which is success, not an error.
func promoteTaskToOpen(ctx context.Context, taskID, paymentID string) error {
	_, err := db.Exec(ctx, `
		update public.tasks
		   set status = 'open', payment_id = $2::uuid
		 where id = $1::uuid and status = $3
	`, taskID, paymentID, taskStatusPendingPayment)
	return err
}

// discardUnpaidTask removes a task that never got its hold, and the payments
// row naming the attempt.
//
// Both rows go, in FK order. The alternative — keeping the failed payments row
// as a record of the decline — was rejected because the row's task_id is NOT
// NULL and would have to point at a task that must not exist: a task nobody
// can see, in no feed, that a requester's own list would still have to filter.
// The decline is recorded in the log with its intent id, which is what a
// support ticket needs and what the Stripe dashboard can be joined against.
//
// EVERY statement re-tests status = 'pending_payment', including the one that
// deletes payments rows. "Only called on an unposted task" is true today, and
// a comment saying so is not what should be standing between a future caller
// and deleting the payments row of a live task — which would leave a real hold
// on somebody's card with nothing naming it, the one outcome payments.go is
// built around avoiding. A posted task reaching here deletes nothing.
//
// Three statements, in this order, because the tables reference each other:
// payments.task_id points at the task and tasks.payment_id points back, so the
// link has to be cut from the task side first or the second statement trips
// tasks_payment_id_fkey.
func discardUnpaidTask(ctx context.Context, taskID string) {
	if _, err := db.Exec(ctx, `
		update public.tasks set payment_id = null
		 where id = $1::uuid and status = $2
	`, taskID, taskStatusPendingPayment); err != nil {
		log.Printf("[payments][ERROR] could not unlink payment from unpaid task=%s: %v", taskID, err)
		return
	}
	if _, err := db.Exec(ctx, `
		delete from public.payments p
		 using public.tasks t
		 where p.task_id = t.id and t.id = $1::uuid and t.status = $2
	`, taskID, taskStatusPendingPayment); err != nil {
		log.Printf("[payments][ERROR] could not delete payments rows for unpaid task=%s: %v", taskID, err)
		return
	}
	if _, err := db.Exec(ctx,
		`delete from public.tasks where id = $1::uuid and status = $2`,
		taskID, taskStatusPendingPayment); err != nil {
		log.Printf("[payments][ERROR] could not delete unpaid task=%s: %v", taskID, err)
	}
}

// ── Releasing the hold ─────────────────────────────────────────────────────

// releaseTaskHold cancels a task's pre-auth, if it has one.
//
// Best-effort BY DESIGN, and the ordering is the point: a cancel that already
// wrote status='cancelled' must not be undone because Stripe was briefly
// unreachable, because the alternative leaves the requester unable to cancel a
// task at all while their money stays held either way. So a failure here is
// logged at ERROR with the task id and written to audit_logs, where an
// operator can find it and release the hold by hand — and the hold expires on
// its own within a week regardless.
//
// Returns the payment row when one was released, nil when the task never had a
// hold (every task posted with PAYMENTS_ENFORCED off).
func releaseTaskHold(ctx context.Context, taskID, actorUID, reason string) *Payment {
	if !paymentsEnabled() {
		return nil
	}
	p, err := Release(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		log.Printf("[payments][ERROR][STRANDED HOLD] task=%s could not be released: %v", taskID, err)
		writeAudit(ctx, taskID, orSystemActor(actorUID), "PAYMENT_RELEASE_FAILED", reason, map[string]any{
			"error": err.Error(),
		})
		return nil
	}
	if p == nil {
		return nil
	}
	log.Printf("[payments] released hold task=%s payment=%s reason=%s", taskID, p.ID, reason)
	writeAudit(ctx, taskID, orSystemActor(actorUID), "PAYMENT_RELEASED", reason, map[string]any{
		"payment_id":        p.ID,
		"stripe_payment_id": p.StripePaymentIntentID,
	})
	return p
}

// orSystemActor keeps audit_logs.actor_id NOT NULL satisfied on paths with no
// human behind them, the same way the Stripe webhook does.
func orSystemActor(uid string) string {
	if uid == "" {
		return systemActorUID
	}
	return uid
}

// ── POST /tasks/:id/payment/confirm — the 3DS second half ──────────────────

// confirmTaskPayment posts a task whose hold needed the cardholder present.
//
// The client has just run the challenge with the SDK, which means the intent
// at Stripe has already moved — this endpoint does not confirm anything, it
// READS the outcome and acts on it. Reading rather than trusting the client's
// word is the whole security of the endpoint: a client that lies about having
// completed 3DS gets a task that is still pending_payment.
func confirmTaskPayment(c *gin.Context) {
	taskID := c.Param("id")
	meUID := c.GetString("uid")
	if meUID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	ctx := c.Request.Context()

	var requesterID, status string
	if err := db.QueryRow(ctx,
		`select requester_id::text, status from public.tasks where id = $1::uuid`, taskID,
	).Scan(&requesterID, &status); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if requesterID != meUID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if status == "open" {
		// The confirm already landed — a double tap, or a retry after a dropped
		// response. Answering OK is what makes this endpoint safe to retry.
		c.JSON(http.StatusOK, gin.H{"ok": true, "status": "open"})
		return
	}
	if status != taskStatusPendingPayment {
		c.JSON(http.StatusBadRequest, gin.H{"error": "task_not_awaiting_payment", "status": status})
		return
	}

	p, err := livePaymentForTask(ctx, taskID)
	if err != nil || p.StripePaymentIntentID == "" {
		log.Printf("[payments][confirm] no live intent for task=%s: %v", taskID, err)
		discardUnpaidTask(ctx, taskID)
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":   "payment_required",
			"message": "That payment expired. Post the task again to try once more.",
		})
		return
	}

	pi, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		log.Printf("[payments][confirm] fetch intent=%s: %v", p.StripePaymentIntentID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "payments_error"})
		return
	}

	switch pi.Status {
	case stripe.PaymentIntentStatusRequiresCapture:
		authorized := int(pi.Amount)
		if err := attachIntent(ctx, p.ID, pi.ID, paymentStatusAuthorized, &authorized); err != nil {
			log.Printf("[payments][confirm][ERROR] hold landed on intent=%s but row %s not updated: %v",
				pi.ID, p.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if err := promoteTaskToOpen(ctx, taskID, p.ID); err != nil {
			log.Printf("[payments][confirm][ERROR] task=%s hold=%s authorized but task not posted: %v",
				taskID, p.ID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		log.Printf("[payments] 3DS confirmed task=%s payment=%s intent=%s amount=%d",
			taskID, p.ID, pi.ID, authorized)
		announceNewTask(ctx, taskID)
		c.JSON(http.StatusOK, gin.H{"ok": true, "status": "open"})

	case stripe.PaymentIntentStatusRequiresAction, stripe.PaymentIntentStatusRequiresConfirmation:
		// Still waiting on the cardholder. The task stays parked.
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":             "payment_authentication_required",
			"message":           "Your bank still needs to confirm this payment.",
			"client_secret":     pi.ClientSecret,
			"payment_intent_id": pi.ID,
			"task_id":           taskID,
			"publishable_key":   stripePublishableKey(),
		})

	default:
		// requires_payment_method (the challenge failed), canceled, or anything
		// else terminal. Nothing is held; the attempt is over.
		log.Printf("[payments][confirm] task=%s intent=%s ended as %s", taskID, pi.ID, pi.Status)
		discardUnpaidTask(ctx, taskID)
		c.JSON(http.StatusPaymentRequired, gin.H{
			"error":   "payment_required",
			"message": "That payment wasn't approved. Try posting again with another card.",
		})
	}
}

// ── Pre-auth expiry (ops visibility only) ──────────────────────────────────

// preAuthExpiryDays is when the card networks start dropping manual-capture
// holds. Visa and Mastercard both release an uncaptured authorization after
// about seven days; some issuers do it sooner.
const preAuthExpiryDays = 7

// preAuthWarnAfterDays is when we start saying so — one day before the hold
// can begin evaporating, which is the last point at which anything could be
// done about it.
const preAuthWarnAfterDays = 6

// watchExpiringPreAuths logs open tasks whose hold is about to lapse.
//
// LOGGING ONLY, and that is a deliberate limitation rather than an oversight.
// The fix — re-authorizing the card off-session to get a fresh seven days — is
// a second charge attempt on somebody's card for a task they posted a week
// ago, which can itself decline, can double-hold if the first release lags,
// and needs a requester-facing story for when it fails. None of that belongs
// in the phase that first places a hold at all.
//
// What happens today when a hold lapses: the task stays open and, at
// completion, Capture fails because the intent is no longer capturable. That
// is loud (the capture path errors) rather than silent, and it is the reason
// this warning exists — so that an operator sees the task BEFORE a supporter
// works it, not after.
//
// A HO:RA task is same-day or scheduled within days, so a task still open at
// six days is already anomalous; in the beta's entire history the longest-open
// task has not come close.
func watchExpiringPreAuths(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reportExpiringPreAuths(ctx)
		}
	}
}

func reportExpiringPreAuths(ctx context.Context) {
	rows, err := db.Query(ctx, `
		select t.id::text, p.id::text, coalesce(p.stripe_payment_intent_id,''),
		       coalesce(p.authorized_cents, 0),
		       extract(epoch from (now() - p.created_at))/86400.0
		  from public.tasks t
		  join public.payments p on p.task_id = t.id
		 where t.status = 'open'
		   and p.status = 'authorized'
		   and p.created_at < now() - make_interval(days => $1)
		 order by p.created_at asc
	`, preAuthWarnAfterDays)
	if err != nil {
		log.Printf("[payments][expiry] query failed: %v", err)
		return
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var taskID, paymentID, intentID string
		var cents int
		var ageDays float64
		if err := rows.Scan(&taskID, &paymentID, &intentID, &cents, &ageDays); err != nil {
			log.Printf("[payments][expiry] scan failed: %v", err)
			return
		}
		n++
		log.Printf("[payments][EXPIRING HOLD] task=%s payment=%s intent=%s amount=%s age=%.1fd "+
			"— card networks release manual-capture holds after ~%dd; capture will fail once it lapses",
			taskID, paymentID, intentID, formatCentsUSD(cents), ageDays, preAuthExpiryDays)
	}
	if n > 0 {
		log.Printf("[payments][expiry] %d open task(s) carrying a hold older than %dd", n, preAuthWarnAfterDays)
	}
}
