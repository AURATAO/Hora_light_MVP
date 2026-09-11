package main

// POST /webhooks/stripe — Stripe's side of the payment lifecycle.
//
// Unauthenticated by design, exactly like the TalkJS webhook: Stripe carries no
// session, so the signature IS the authentication and it is verified against
// the raw request body before a single field is parsed or trusted.
//
// WHY A WEBHOOK AT ALL, when payments.go already knows what it asked Stripe to
// do: because the API response is not the last word. A hold can fail minutes
// later, a bank can reverse an authorization, and a dispute arrives with no
// API call of ours anywhere near it. The webhook is how the payments table
// learns about money events that no request of ours initiated.
//
// PHASE 1 SCOPE. The handler is complete and live; the events it handles are
// ones no Phase 1 code can currently generate, because nothing calls
// payments.go yet. It ships now so that the endpoint exists, is registered in
// the Stripe dashboard, and has been receiving (and correctly ignoring) test
// traffic long before the first real hold is placed.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/webhook"

	"hora-auth/internal/notify"
)

// stripeMaxBodyBytes caps what will be read from a webhook request. Stripe
// events are a few KB; the cap exists so an unauthenticated endpoint cannot be
// made to buffer an arbitrary amount of memory before the signature — the
// thing that would reject it — has even been checked.
const stripeMaxBodyBytes = 256 * 1024

// systemActorUID is the actor_id written to audit_logs for events that no
// human initiated. audit_logs.actor_id is NOT NULL and every other writer has
// a real admin behind it; a webhook has Stripe. The nil UUID is used rather
// than borrowing the requester's id, which would misattribute a platform event
// to a user in the one table whose entire purpose is saying who did what.
const systemActorUID = "00000000-0000-0000-0000-000000000000"

func RegisterStripeWebhooks(r *gin.Engine) {
	r.POST("/webhooks/stripe", handleStripeWebhook)
}

func handleStripeWebhook(c *gin.Context) {
	secret := strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET"))
	if secret == "" {
		// Nothing can be authenticated, so nothing is trusted. Fail closed.
		log.Printf("[stripe][webhook] STRIPE_WEBHOOK_SECRET not set — rejecting")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// The signature covers the exact bytes Stripe sent. Gin's JSON binding
	// would consume and re-encode the body, changing those bytes and breaking
	// verification, so the raw body is read first and parsed only after the
	// signature checks out.
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, stripeMaxBodyBytes))
	if err != nil {
		log.Printf("[stripe][webhook] body read error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}

	// ConstructEvent does the whole check: it parses the Stripe-Signature
	// header, recomputes HMAC-SHA256 over "timestamp.body" with the endpoint
	// secret, compares in constant time, and enforces the default 5-minute
	// timestamp tolerance so a captured request cannot be replayed later.
	event, err := webhook.ConstructEvent(raw, c.GetHeader("Stripe-Signature"), secret)
	if err != nil {
		log.Printf("[stripe][webhook] signature verification failed: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx := c.Request.Context()

	// Idempotency. Stripe delivers at least once and retries for days on any
	// non-2xx, so the same event id will arrive again — after a deploy, after
	// a timeout, after a 500 from an unrelated bug. Claiming the id first
	// means a duplicate never reaches the handlers below.
	fresh, err := claimStripeEvent(ctx, event.ID, string(event.Type))
	if err != nil {
		// Could not determine whether this is a replay. Answering non-2xx
		// asks Stripe to retry, which is the safe direction: processing twice
		// is worse than processing late.
		log.Printf("[stripe][webhook] dedupe check failed event=%s: %v", event.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "retry"})
		return
	}
	if !fresh {
		log.Printf("[stripe][webhook] duplicate event=%s type=%s — ignoring", event.ID, event.Type)
		c.Status(http.StatusOK)
		return
	}

	log.Printf("[stripe][webhook] event=%s type=%s", event.ID, event.Type)

	switch event.Type {
	case "payment_intent.succeeded":
		err = onPaymentIntentSucceeded(ctx, &event)
	case "payment_intent.canceled":
		err = onPaymentIntentStatus(ctx, &event, paymentStatusCanceled)
	case "payment_intent.payment_failed":
		err = onPaymentIntentStatus(ctx, &event, paymentStatusFailed)
	case "charge.dispute.created":
		err = onChargeDisputeCreated(ctx, &event)
	default:
		// Unknown or unsubscribed event types are acknowledged, not errored.
		// A 4xx/5xx here would make Stripe retry an event we will never care
		// about, for days, and eventually disable the endpoint.
		log.Printf("[stripe][webhook] unhandled type=%s — ignoring", event.Type)
		markStripeEventProcessed(ctx, event.ID)
		c.Status(http.StatusOK)
		return
	}

	if err != nil {
		// Leave the event unmarked so Stripe's retry gets another attempt;
		// claimStripeEvent's row stays, but processed_at is still null, which
		// is how an operator tells "seen but not handled" from "handled".
		log.Printf("[stripe][webhook][ERROR] event=%s type=%s: %v", event.ID, event.Type, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "retry"})
		return
	}

	markStripeEventProcessed(ctx, event.ID)
	c.Status(http.StatusOK)
}

// ── Event handlers ─────────────────────────────────────────────────────────

// onPaymentIntentSucceeded records a completed capture.
//
// With manual capture this fires when the capture settles, not when the hold
// is placed, so it confirms money actually moved. The amounts are taken from
// Stripe rather than from our own record of what we asked for: this event is
// the authority on what was charged.
func onPaymentIntentSucceeded(ctx context.Context, event *stripe.Event) error {
	pi, err := paymentIntentFrom(event)
	if err != nil {
		return err
	}
	p, err := paymentByIntentID(ctx, pi.ID)
	if err != nil {
		// An intent we have no row for is not an error worth retrying — it is
		// almost always a test event fired from the dashboard, or an intent
		// created outside this backend. Logged and acknowledged.
		log.Printf("[stripe][webhook] no payments row for intent=%s — ignoring", pi.ID)
		return nil
	}
	captured := int(pi.AmountReceived)
	if err := updatePaymentStatus(ctx, p.ID, paymentStatusCaptured, &captured); err != nil {
		return fmt.Errorf("record capture for payment %s: %w", p.ID, err)
	}
	log.Printf("[stripe][webhook] payment=%s task=%s captured=%d", p.ID, p.TaskID, captured)
	return nil
}

// onPaymentIntentStatus handles the two terminal non-success outcomes. Both
// mean the same thing operationally: this task has no usable hold any more.
func onPaymentIntentStatus(ctx context.Context, event *stripe.Event, status string) error {
	pi, err := paymentIntentFrom(event)
	if err != nil {
		return err
	}
	p, err := paymentByIntentID(ctx, pi.ID)
	if err != nil {
		log.Printf("[stripe][webhook] no payments row for intent=%s — ignoring", pi.ID)
		return nil
	}
	if err := updatePaymentStatus(ctx, p.ID, status, nil); err != nil {
		return fmt.Errorf("set payment %s to %s: %w", p.ID, status, err)
	}
	log.Printf("[stripe][webhook] payment=%s task=%s → %s", p.ID, p.TaskID, status)
	return nil
}

// onChargeDisputeCreated is the one event a human has to see.
//
// A dispute has a response deadline measured in days, and missing it loses the
// money by default. So this writes an audit row (durable, queryable, survives
// everyone's inbox) AND emails the ops allowlist. Neither alone is enough: the
// audit row nobody reads, the email nobody can find six weeks later.
func onChargeDisputeCreated(ctx context.Context, event *stripe.Event) error {
	var dispute stripe.Dispute
	if err := json.Unmarshal(event.Data.Raw, &dispute); err != nil {
		return fmt.Errorf("decode dispute: %w", err)
	}

	taskID, paymentID := "", ""
	if dispute.PaymentIntent != nil {
		if p, err := paymentByIntentID(ctx, dispute.PaymentIntent.ID); err == nil {
			taskID, paymentID = p.TaskID, p.ID
		}
	}

	meta := map[string]any{
		"dispute_id":     dispute.ID,
		"amount_cents":   dispute.Amount,
		"reason":         string(dispute.Reason),
		"status":         string(dispute.Status),
		"payment_id":     paymentID,
		"stripe_charge":  chargeIDOf(&dispute),
		"stripe_event":   event.ID,
		"evidence_due":   dispute.EvidenceDetails.DueBy,
		"needs_response": dispute.EvidenceDetails.HasEvidence == false,
	}

	// audit_logs.job_id is NOT NULL and is a task id everywhere else. A
	// dispute we cannot trace to a task still has to be recorded, so it is
	// filed against the nil UUID rather than dropped — an untraceable dispute
	// is more urgent than a traceable one, not less.
	auditTaskID := taskID
	if auditTaskID == "" {
		auditTaskID = systemActorUID
	}
	writeAudit(ctx, auditTaskID, systemActorUID, "PAYMENT_DISPUTED", string(dispute.Reason), meta)

	log.Printf("[stripe][webhook][DISPUTE] dispute=%s task=%s amount=%d reason=%s due=%d",
		dispute.ID, taskID, dispute.Amount, dispute.Reason, dispute.EvidenceDetails.DueBy)

	notifyOpsOfDispute(&dispute, taskID)
	return nil
}

// notifyOpsOfDispute emails the ops allowlist. Deliberately not a
// notifications row: notifications.type is a Postgres enum of task lifecycle
// events, user_id is NOT NULL, and a dispute belongs to the platform rather
// than to any one user — adding an enum value to address ops would be
// modelling the org chart in the user notification feed.
//
// Fired in a goroutine on a background context (S-32): a slow mail provider
// must never hold the webhook response open, because a Stripe webhook that
// times out is a Stripe webhook that gets retried.
func notifyOpsOfDispute(dispute *stripe.Dispute, taskID string) {
	subject := fmt.Sprintf("[HO:RA] Payment disputed — %s (%s)",
		formatCentsUSD(int(dispute.Amount)), dispute.Reason)

	taskLine := "—"
	if taskID != "" {
		taskLine = taskID
	}
	body := fmt.Sprintf(
		`<p><strong>A payment has been disputed.</strong></p>
<p>Stripe will debit the amount plus a dispute fee unless evidence is submitted before the deadline.</p>
<ul>
  <li>Dispute: %s</li>
  <li>Amount: %s</li>
  <li>Reason: %s</li>
  <li>Status: %s</li>
  <li>Task: %s</li>
</ul>
<p>Respond in the Stripe dashboard → Payments → Disputes.</p>`,
		dispute.ID, formatCentsUSD(int(dispute.Amount)), dispute.Reason, dispute.Status, taskLine)

	recipients := make([]string, 0, len(opsAdmins))
	for email := range opsAdmins {
		recipients = append(recipients, email)
	}
	sortStrings(recipients)

	go func() {
		for _, to := range recipients {
			if err := notify.SendEmail(notify.EmailPayload{To: to, Subject: subject, Html: body}); err != nil {
				log.Printf("[stripe][webhook] dispute email failed to=%s dispute=%s err=%v", to, dispute.ID, err)
			}
		}
	}()
}

// ── Helpers ────────────────────────────────────────────────────────────────

// paymentIntentFrom decodes the PaymentIntent carried by an event.
func paymentIntentFrom(event *stripe.Event) (*stripe.PaymentIntent, error) {
	var pi stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		return nil, fmt.Errorf("decode payment_intent: %w", err)
	}
	if pi.ID == "" {
		return nil, fmt.Errorf("event %s carries no payment_intent id", event.ID)
	}
	return &pi, nil
}

// chargeIDOf pulls the charge id off a dispute, which stripe-go models as an
// expandable object that may be either an id or a full object.
func chargeIDOf(d *stripe.Dispute) string {
	if d.Charge == nil {
		return ""
	}
	return d.Charge.ID
}

// claimStripeEvent records an event id, reporting whether this delivery is the
// first. The INSERT is the claim: ON CONFLICT DO NOTHING means a concurrent
// duplicate delivery loses the race atomically rather than both proceeding
// after a SELECT that found nothing.
func claimStripeEvent(ctx context.Context, eventID, eventType string) (bool, error) {
	var inserted bool
	err := db.QueryRow(ctx, `
		insert into public.stripe_webhook_events (event_id, event_type)
		values ($1, $2)
		on conflict (event_id) do nothing
		returning true
	`, eventID, eventType).Scan(&inserted)
	if err != nil {
		// No row returned means the insert conflicted: a duplicate.
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, err
	}
	return inserted, nil
}

// markStripeEventProcessed stamps an event as fully handled. Best-effort: the
// work is already done, and failing to stamp it costs a log line, not
// correctness — a redelivery would still be caught by the event_id claim.
func markStripeEventProcessed(ctx context.Context, eventID string) {
	if _, err := db.Exec(ctx, `
		update public.stripe_webhook_events set processed_at = now() where event_id = $1
	`, eventID); err != nil {
		log.Printf("[stripe][webhook] could not mark event=%s processed: %v", eventID, err)
	}
}
