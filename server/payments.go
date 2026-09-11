package main

// The payments service layer: everything that talks to Stripe about a task.
//
// PHASE 1 SCOPE. These three functions are complete and callable, and nothing
// in any task flow calls them. That is deliberate — Phase 2 wires CreatePreAuth
// into task creation and Capture/Release into completion and cancellation.
// Shipping them unwired means the Stripe integration, the payments table and
// the webhook can be deployed and exercised in test mode without a single
// user-visible payment surface, so Phase 2 is a wiring change against an
// integration that already works rather than a big-bang.
//
// MONEY FLOW (the whole thing, for orientation)
//
//	post        CreatePreAuth  authorize (base+time estimate)x1.5 + budget + $5
//	completion  Capture        take time cost + verified receipt, release rest
//	cancel      Release        cancel the hold; nothing was ever captured
//
// "Refund of unused time" is not a refund. The pre-auth is a hold; capturing
// less than the held amount releases the remainder automatically, so the
// happy path never issues a refund at all. Refunds exist only for genuine
// after-the-fact corrections, which is a Phase 3 concern.
//
// FAIL-CLOSED. Without STRIPE_SECRET_KEY every function here returns
// errPaymentsDisabled rather than half-working. Same shape as the TalkJS
// webhook's missing-secret branch: a payments path that silently no-ops is how
// a task gets marked complete with nobody charged.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/paymentintent"
)

var errPaymentsDisabled = errors.New("payments disabled: STRIPE_SECRET_KEY not set")

// ErrPaymentsDisabled is the exported form for handlers that need to answer
// 503 rather than 500 when Stripe is not configured.
var ErrPaymentsDisabled = errPaymentsDisabled

var stripeInit sync.Once

// initStripe sets the package-level API key once. stripe-go is configured
// globally rather than per-client, so this is the one place the key is read.
func initStripe() {
	stripeInit.Do(func() {
		key := strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
		if key == "" {
			log.Printf("[payments] STRIPE_SECRET_KEY not set — payment endpoints will answer 503")
			return
		}
		stripe.Key = key
		mode := "LIVE"
		if strings.HasPrefix(key, "sk_test_") || strings.HasPrefix(key, "rk_test_") {
			mode = "test"
		}
		// The mode is logged, the key never is (S-12). Getting this wrong is
		// the single most expensive configuration mistake available here, so
		// it is stated at startup rather than inferred from a failed charge.
		log.Printf("[payments] Stripe initialized in %s mode", mode)
	})
}

// paymentsEnabled reports whether Stripe is configured. Every exported
// function checks it; callers that want to gate a route should check it too so
// they can answer 503 before doing any work.
func paymentsEnabled() bool {
	initStripe()
	return stripe.Key != ""
}

// stripeIsTestMode reports whether the configured key is a test key. Used by
// the health/report surface, never to change behaviour.
func stripeIsTestMode() bool {
	initStripe()
	return strings.HasPrefix(stripe.Key, "sk_test_") || strings.HasPrefix(stripe.Key, "rk_test_")
}

// requirePaymentsEnabled refuses a request when Stripe is not configured,
// before the handler runs. Phase 1 registers no payment routes, so this is
// attached to nothing yet — it exists so that Phase 2's routes are gated by
// construction rather than by each handler remembering to check. Fail closed:
// an unconfigured payments route answers "unavailable", never "fine".
func requirePaymentsEnabled() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !paymentsEnabled() {
			log.Printf("[payments] %s %s refused — Stripe not configured", c.Request.Method, c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error":   "payments_unavailable",
				"message": "Payments are temporarily unavailable. Please try again shortly.",
			})
			return
		}
		c.Next()
	}
}

// ── The payments row ───────────────────────────────────────────────────────

// Payment mirrors one public.payments row.
type Payment struct {
	ID                    string
	TaskID                string
	RequesterID           string
	Kind                  string
	StripePaymentIntentID string
	Status                string
	AuthorizedCents       *int
	CapturedCents         *int
	TimeCostCents         *int
	ShoppingReceiptCents  *int
}

// Payment kinds and statuses. These strings are enforced by CHECK constraints
// on the table; keeping the Go constants beside the only code that writes them
// is what stops a typo becoming a constraint violation in production.
const (
	paymentKindTaskPayment = "task_payment"

	paymentStatusRequiresAuth = "requires_auth"
	paymentStatusAuthorized   = "authorized"
	paymentStatusCaptured     = "captured"
	paymentStatusCanceled     = "canceled"
	paymentStatusFailed       = "failed"
)

// PreAuthInput is what CreatePreAuth needs to hold money for a task. The
// Stripe customer and payment method come from the caller because collecting
// them is a client concern (Phase 2's payment sheet), not this layer's.
type PreAuthInput struct {
	TaskID              string
	RequesterID         string
	Category            string
	EstimatedMinutes    int
	ShoppingBudgetCents int

	// Stripe identifiers for the saved card. Both optional in test mode: with
	// neither, the intent is created unconfirmed and a client secret is
	// returned for the client to confirm — which is exactly the Phase 2 flow.
	StripeCustomerID      string
	StripePaymentMethodID string
}

// CreatePreAuth authorizes (but does not capture) the pre-auth amount for a
// task, and records the intent in public.payments.
//
// CaptureMethod is manual — the entire model depends on it. An automatic
// capture would take the full held amount at authorization time, which is the
// opposite of "you are only charged for the time actually worked".
func CreatePreAuth(ctx context.Context, in PreAuthInput) (*Payment, error) {
	if !paymentsEnabled() {
		return nil, errPaymentsDisabled
	}
	if in.TaskID == "" || in.RequesterID == "" {
		return nil, errors.New("payments: task_id and requester_id are required")
	}

	amount := preAuthAmountCents(in.Category, in.EstimatedMinutes, in.ShoppingBudgetCents)

	// The row is written BEFORE the Stripe call, in requires_auth. If the
	// process dies mid-call there is a local record of an intent that may
	// exist at Stripe, which is recoverable; the reverse — a live hold on
	// someone's card with no row naming it — is not.
	p, err := insertPayment(ctx, in.TaskID, in.RequesterID, paymentKindTaskPayment, paymentStatusRequiresAuth, amount)
	if err != nil {
		return nil, fmt.Errorf("payments: record intent: %w", err)
	}

	params := &stripe.PaymentIntentParams{
		Amount:        stripe.Int64(int64(amount)),
		Currency:      stripe.String(Billing.Currency),
		CaptureMethod: stripe.String(string(stripe.PaymentIntentCaptureMethodManual)),
		// Reconciliation handle: given a Stripe dashboard row, this says which
		// task and which payments row it belongs to without a lookup table.
		Metadata: map[string]string{
			"task_id":      in.TaskID,
			"requester_id": in.RequesterID,
			"payment_id":   p.ID,
			"kind":         paymentKindTaskPayment,
		},
	}
	params.SetIdempotencyKey("preauth_" + p.ID)
	if in.StripeCustomerID != "" {
		params.Customer = stripe.String(in.StripeCustomerID)
	}
	if in.StripePaymentMethodID != "" {
		params.PaymentMethod = stripe.String(in.StripePaymentMethodID)
		params.Confirm = stripe.Bool(true)
		// Card not present, customer not in session: the saved-card flow.
		params.OffSession = stripe.Bool(true)
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		_ = updatePaymentStatus(ctx, p.ID, paymentStatusFailed, nil)
		return nil, fmt.Errorf("payments: create intent: %w", err)
	}

	status := paymentStatusRequiresAuth
	var authorized *int
	if pi.Status == stripe.PaymentIntentStatusRequiresCapture {
		status = paymentStatusAuthorized
		a := int(pi.Amount)
		authorized = &a
	}

	if err := attachIntent(ctx, p.ID, pi.ID, status, authorized); err != nil {
		return nil, fmt.Errorf("payments: attach intent %s: %w", pi.ID, err)
	}

	log.Printf("[payments] pre-auth task=%s payment=%s intent=%s amount=%d status=%s",
		in.TaskID, p.ID, pi.ID, amount, pi.Status)

	p.StripePaymentIntentID = pi.ID
	p.Status = status
	p.AuthorizedCents = authorized
	return p, nil
}

// Capture takes the final amount from an authorized hold: the time actually
// worked plus the verified receipt. Whatever was held above that is released
// by Stripe automatically — that is the "unused time is refunded" promise, and
// it involves no refund.
//
// timeCostCents must already be the settled figure from calcTaskCostCents;
// this function does no pricing of its own, so there is exactly one place
// where a task's cost is decided.
func Capture(ctx context.Context, taskID string, timeCostCents, shoppingReceiptCents int) (*Payment, error) {
	if !paymentsEnabled() {
		return nil, errPaymentsDisabled
	}
	p, err := livePaymentForTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if p.Status != paymentStatusAuthorized {
		return nil, fmt.Errorf("payments: task %s payment is %s, not authorized", taskID, p.Status)
	}
	if timeCostCents < 0 || shoppingReceiptCents < 0 {
		return nil, errors.New("payments: capture amounts cannot be negative")
	}

	total := timeCostCents + shoppingReceiptCents

	// Stripe rejects a capture above the authorized amount, so a settlement
	// that outgrew its hold has to be clamped — and loudly. Reaching this
	// means the pre-auth multiplier is too small for real tasks, which is a
	// pricing decision, not an error to swallow: the difference is money the
	// platform cannot collect on this task.
	if p.AuthorizedCents != nil && total > *p.AuthorizedCents {
		log.Printf("[payments][UNDERCAPTURE] task=%s settlement=%d exceeds hold=%d — capturing hold, shortfall=%d",
			taskID, total, *p.AuthorizedCents, total-*p.AuthorizedCents)
		total = *p.AuthorizedCents
	}

	params := &stripe.PaymentIntentCaptureParams{
		AmountToCapture: stripe.Int64(int64(total)),
	}
	// Zero during beta; Phase 3 turns it on by setting the basis points.
	if fee := total * Billing.ApplicationFeeBasisPoints / 10000; fee > 0 {
		params.ApplicationFeeAmount = stripe.Int64(int64(fee))
	}
	params.SetIdempotencyKey("capture_" + p.ID)

	pi, err := paymentintent.Capture(p.StripePaymentIntentID, params)
	if err != nil {
		return nil, fmt.Errorf("payments: capture %s: %w", p.StripePaymentIntentID, err)
	}

	if err := recordCapture(ctx, p.ID, int(pi.AmountReceived), timeCostCents, shoppingReceiptCents); err != nil {
		// The money moved; only the bookkeeping failed. Surfacing the error
		// without losing the fact of the capture is the whole point of logging
		// the intent id here.
		log.Printf("[payments][ERROR] captured intent=%s but failed to record: %v", pi.ID, err)
		return nil, fmt.Errorf("payments: record capture: %w", err)
	}

	log.Printf("[payments] capture task=%s payment=%s intent=%s total=%d (time=%d receipt=%d)",
		taskID, p.ID, pi.ID, total, timeCostCents, shoppingReceiptCents)

	p.Status = paymentStatusCaptured
	return p, nil
}

// Release cancels an uncaptured hold, freeing the funds on the requester's
// card. Used when a task is cancelled before anything is owed.
//
// Idempotent by intent: a payment already canceled returns successfully rather
// than erroring, because "release this hold" is satisfied by a hold that is
// already released.
func Release(ctx context.Context, taskID string) (*Payment, error) {
	if !paymentsEnabled() {
		return nil, errPaymentsDisabled
	}
	p, err := livePaymentForTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing was ever held for this task. Phase 2 will call Release on
		// cancel paths that may never have pre-authed, so this is normal.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.Status == paymentStatusCanceled {
		return p, nil
	}
	if p.Status == paymentStatusCaptured {
		return nil, fmt.Errorf("payments: task %s already captured — release would need a refund", taskID)
	}

	if p.StripePaymentIntentID != "" {
		params := &stripe.PaymentIntentCancelParams{}
		params.SetIdempotencyKey("release_" + p.ID)
		if _, err := paymentintent.Cancel(p.StripePaymentIntentID, params); err != nil {
			return nil, fmt.Errorf("payments: cancel %s: %w", p.StripePaymentIntentID, err)
		}
	}

	if err := updatePaymentStatus(ctx, p.ID, paymentStatusCanceled, nil); err != nil {
		return nil, fmt.Errorf("payments: record release: %w", err)
	}

	log.Printf("[payments] release task=%s payment=%s intent=%s", taskID, p.ID, p.StripePaymentIntentID)
	p.Status = paymentStatusCanceled
	return p, nil
}

// ── Row access ─────────────────────────────────────────────────────────────

func insertPayment(ctx context.Context, taskID, requesterID, kind, status string, authorizedCents int) (*Payment, error) {
	var p Payment
	err := db.QueryRow(ctx, `
		insert into public.payments (task_id, requester_id, kind, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, $5)
		returning id::text, task_id::text, requester_id::text, kind, status
	`, taskID, requesterID, kind, status, authorizedCents).
		Scan(&p.ID, &p.TaskID, &p.RequesterID, &p.Kind, &p.Status)
	if err != nil {
		return nil, err
	}
	p.AuthorizedCents = &authorizedCents
	return &p, nil
}

func attachIntent(ctx context.Context, paymentID, intentID, status string, authorizedCents *int) error {
	_, err := db.Exec(ctx, `
		update public.payments
		   set stripe_payment_intent_id = $2,
		       status = $3,
		       authorized_cents = coalesce($4, authorized_cents),
		       updated_at = now()
		 where id = $1::uuid
	`, paymentID, intentID, status, authorizedCents)
	return err
}

func updatePaymentStatus(ctx context.Context, paymentID, status string, capturedCents *int) error {
	_, err := db.Exec(ctx, `
		update public.payments
		   set status = $2,
		       captured_cents = coalesce($3, captured_cents),
		       updated_at = now()
		 where id = $1::uuid
	`, paymentID, status, capturedCents)
	return err
}

func recordCapture(ctx context.Context, paymentID string, capturedCents, timeCostCents, shoppingReceiptCents int) error {
	_, err := db.Exec(ctx, `
		update public.payments
		   set status = $2,
		       captured_cents = $3,
		       time_cost_cents = $4,
		       shopping_receipt_cents = $5,
		       updated_at = now()
		 where id = $1::uuid
	`, paymentID, paymentStatusCaptured, capturedCents, timeCostCents, shoppingReceiptCents)
	return err
}

// livePaymentForTask returns the task's payment that is still in play — the
// one the partial unique index guarantees is at most one.
func livePaymentForTask(ctx context.Context, taskID string) (*Payment, error) {
	var p Payment
	var intentID sql.NullString
	err := db.QueryRow(ctx, `
		select id::text, task_id::text, requester_id::text, kind,
		       coalesce(stripe_payment_intent_id,''), status, authorized_cents
		  from public.payments
		 where task_id = $1::uuid
		   and kind = $2
		   and status in ('requires_auth','authorized')
		 order by created_at desc
		 limit 1
	`, taskID, paymentKindTaskPayment).
		Scan(&p.ID, &p.TaskID, &p.RequesterID, &p.Kind, &intentID, &p.Status, &p.AuthorizedCents)
	if err != nil {
		return nil, err
	}
	p.StripePaymentIntentID = intentID.String
	return &p, nil
}

// paymentByIntentID finds the row a webhook event belongs to.
func paymentByIntentID(ctx context.Context, intentID string) (*Payment, error) {
	var p Payment
	err := db.QueryRow(ctx, `
		select id::text, task_id::text, requester_id::text, kind, status
		  from public.payments
		 where stripe_payment_intent_id = $1
	`, intentID).Scan(&p.ID, &p.TaskID, &p.RequesterID, &p.Kind, &p.Status)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
