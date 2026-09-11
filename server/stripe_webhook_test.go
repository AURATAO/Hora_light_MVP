package main

// Stripe webhook coverage.
//
// The endpoint is unauthenticated, so the signature check is the only thing
// standing between the public internet and the payments table — most of what
// follows is about that boundary rather than about payments.
//
// Runs against the same disposable Postgres as the ops-action suite, and
// applies the real payments migration rather than a hand-written fixture, so
// the migration itself is exercised on every run:
//
//	docker run -d --rm --name hora-billing-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run StripeWebhook -v

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const testStripeWebhookSecret = "whsec_test_secret_for_unit_tests"

// paymentsMigrationPath is the real migration, applied by the test setup. If
// it stops being valid SQL, these tests fail before any handler runs — which
// is the point of applying it rather than restating its DDL here.
const paymentsMigrationPath = "../supabase/migrations/20260911120000_payments_and_billing_schema.sql"

func setupStripeWebhookDB(t *testing.T) {
	t.Helper()
	setupAdminOpsDB(t) // users, tasks, worklogs, audit_logs + the pool swap

	// adminOpsFixture drops only the tables it declares, and the migration
	// below is all IF NOT EXISTS — so without this, payments rows from a
	// previous run survive into a fresh fixture and collide on the intent-id
	// unique constraint. Dropped AFTER setupAdminOpsDB so that tasks and users
	// exist for the migration's foreign keys.
	if _, err := db.Exec(context.Background(), `
		DROP TABLE IF EXISTS public.payments CASCADE;
		DROP TABLE IF EXISTS public.stripe_webhook_events CASCADE;
	`); err != nil {
		t.Fatalf("drop payments tables: %v", err)
	}

	migration, err := os.ReadFile(paymentsMigrationPath)
	if err != nil {
		t.Fatalf("read payments migration: %v", err)
	}
	if _, err := db.Exec(context.Background(), string(migration)); err != nil {
		t.Fatalf("apply payments migration: %v", err)
	}
	t.Setenv("STRIPE_WEBHOOK_SECRET", testStripeWebhookSecret)
}

// signStripePayload builds a valid Stripe-Signature header:
// HMAC-SHA256 over "timestamp.payload", hex-encoded, keyed with the endpoint
// secret. Same construction stripe-go's webhook.ConstructEvent verifies.
func signStripePayload(payload, secret string, ts time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts.Unix(), payload)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func postStripeWebhook(t *testing.T, payload, signature string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	if signature != "" {
		c.Request.Header.Set("Stripe-Signature", signature)
	}
	handleStripeWebhook(c)
	return w.Code
}

// postSignedStripeWebhook signs correctly and posts, the normal path.
func postSignedStripeWebhook(t *testing.T, payload string) int {
	t.Helper()
	return postStripeWebhook(t, payload, signStripePayload(payload, testStripeWebhookSecret, time.Now()))
}

func stripeEventJSON(id, eventType, dataObject string) string {
	return fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"api_version":"2024-06-20",
	                     "created":%d,"data":{"object":%s}}`,
		id, eventType, time.Now().Unix(), dataObject)
}

// ── The signature boundary ─────────────────────────────────────────────────

func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	setupStripeWebhookDB(t)

	payload := stripeEventJSON("evt_bad_sig", "payment_intent.succeeded", `{"id":"pi_x","amount_received":1000}`)

	cases := []struct {
		name      string
		signature string
	}{
		{"no signature header at all", ""},
		{"malformed header", "not-a-signature"},
		{"right shape, wrong key", signStripePayload(payload, "whsec_a_different_secret", time.Now())},
		{
			// Stripe's 5-minute tolerance: a correctly-signed request captured
			// off the wire must not still be replayable an hour later.
			"correctly signed but stale",
			signStripePayload(payload, testStripeWebhookSecret, time.Now().Add(-1*time.Hour)),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := postStripeWebhook(t, payload, tc.signature); code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", code)
			}
		})
	}
}

// A signature that verifies against the wrong body is the subtle version of
// the same attack: the handler must hash what was actually sent.
func TestStripeWebhookRejectsSignatureForDifferentBody(t *testing.T) {
	setupStripeWebhookDB(t)

	signed := stripeEventJSON("evt_a", "payment_intent.succeeded", `{"id":"pi_a","amount_received":100}`)
	tampered := stripeEventJSON("evt_a", "payment_intent.succeeded", `{"id":"pi_a","amount_received":999999}`)

	sig := signStripePayload(signed, testStripeWebhookSecret, time.Now())
	if code := postStripeWebhook(t, tampered, sig); code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — the amount was altered after signing", code)
	}
}

// Fail closed: with no configured secret nothing can be authenticated, so
// nothing is accepted. Same branch as the TalkJS webhook.
func TestStripeWebhookFailsClosedWithoutSecret(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")

	payload := stripeEventJSON("evt_nosecret", "payment_intent.succeeded", `{"id":"pi_x","amount_received":1}`)
	if code := postStripeWebhook(t, payload, signStripePayload(payload, testStripeWebhookSecret, time.Now())); code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when STRIPE_WEBHOOK_SECRET is unset", code)
	}
}

// ── Event routing ──────────────────────────────────────────────────────────

// An event type we do not handle is acknowledged, never errored: a non-2xx
// makes Stripe retry for days and eventually disable the endpoint.
func TestStripeWebhookIgnoresUnknownEvents(t *testing.T) {
	setupStripeWebhookDB(t)

	for i, eventType := range []string{
		"customer.created",
		"invoice.paid",
		"payment_intent.created",
		"radar.early_fraud_warning.created",
	} {
		payload := stripeEventJSON(fmt.Sprintf("evt_unknown_%d", i), eventType, `{"id":"obj_1"}`)
		if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", eventType, code)
		}
	}
}

// An intent we have no row for (a dashboard test event, or an intent created
// outside this backend) is acknowledged, not retried.
func TestStripeWebhookAcceptsUnknownPaymentIntent(t *testing.T) {
	setupStripeWebhookDB(t)

	payload := stripeEventJSON("evt_orphan_pi", "payment_intent.succeeded",
		`{"id":"pi_not_ours","amount_received":2500}`)
	if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
}

// The lifecycle events actually move the payments row.
func TestStripeWebhookUpdatesPaymentStatus(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	cases := []struct {
		name       string
		eventType  string
		intentID   string
		dataObject string
		wantStatus string
	}{
		{
			name: "succeeded records the capture", eventType: "payment_intent.succeeded",
			intentID: "pi_succeeded", dataObject: `{"id":"pi_succeeded","amount_received":1950}`,
			wantStatus: paymentStatusCaptured,
		},
		{
			name: "canceled releases the hold", eventType: "payment_intent.canceled",
			intentID: "pi_canceled", dataObject: `{"id":"pi_canceled"}`,
			wantStatus: paymentStatusCanceled,
		},
		{
			name: "payment_failed marks the hold dead", eventType: "payment_intent.payment_failed",
			intentID: "pi_failed", dataObject: `{"id":"pi_failed"}`,
			wantStatus: paymentStatusFailed,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedPayment(t, w.taskID, w.requesterID, tc.intentID, paymentStatusAuthorized)

			payload := stripeEventJSON(fmt.Sprintf("evt_status_%d", i), tc.eventType, tc.dataObject)
			if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}

			if got := paymentStatusOf(t, tc.intentID); got != tc.wantStatus {
				t.Errorf("payment status = %q, want %q", got, tc.wantStatus)
			}
		})
	}

	// The capture amount is taken from Stripe, which is the authority on what
	// actually moved — not from what we asked for.
	var captured *int
	if err := db.QueryRow(context.Background(),
		`select captured_cents from public.payments where stripe_payment_intent_id='pi_succeeded'`,
	).Scan(&captured); err != nil {
		t.Fatalf("read captured_cents: %v", err)
	}
	if captured == nil || *captured != 1950 {
		t.Errorf("captured_cents = %v, want 1950", captured)
	}
}

// ── Disputes ───────────────────────────────────────────────────────────────

// A dispute has a response deadline measured in days and loses by default if
// missed, so it must leave a durable trace.
func TestStripeWebhookDisputeWritesAuditRow(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	seedPayment(t, w.taskID, w.requesterID, "pi_disputed", paymentStatusCaptured)

	payload := stripeEventJSON("evt_dispute_1", "charge.dispute.created", `{
		"id": "dp_test_1",
		"amount": 1950,
		"reason": "fraudulent",
		"status": "needs_response",
		"charge": "ch_test_1",
		"payment_intent": "pi_disputed",
		"evidence_details": {"due_by": 1760000000, "has_evidence": false}
	}`)

	if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	rows := auditRows(t, w.taskID, "PAYMENT_DISPUTED")
	if len(rows) != 1 {
		t.Fatalf("PAYMENT_DISPUTED audit rows = %d, want 1", len(rows))
	}
	if rows[0].Reason != "fraudulent" {
		t.Errorf("reason = %q, want %q", rows[0].Reason, "fraudulent")
	}
	for _, want := range []string{"dp_test_1", "1950", "ch_test_1", "evt_dispute_1"} {
		if !strings.Contains(rows[0].Meta, want) {
			t.Errorf("audit meta is missing %q: %s", want, rows[0].Meta)
		}
	}
}

// A dispute we cannot trace to a task is MORE urgent than a traceable one, so
// it must still be recorded rather than dropped on the lookup failure.
func TestStripeWebhookDisputeWithoutKnownPaymentIsStillRecorded(t *testing.T) {
	setupStripeWebhookDB(t)

	payload := stripeEventJSON("evt_dispute_orphan", "charge.dispute.created", `{
		"id": "dp_orphan",
		"amount": 5000,
		"reason": "product_not_received",
		"status": "needs_response",
		"charge": "ch_orphan",
		"payment_intent": "pi_never_seen",
		"evidence_details": {"due_by": 1760000000, "has_evidence": false}
	}`)

	if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	rows := auditRows(t, systemActorUID, "PAYMENT_DISPUTED")
	if len(rows) != 1 {
		t.Fatalf("untraceable dispute left %d audit rows, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Meta, "dp_orphan") {
		t.Errorf("audit meta is missing the dispute id: %s", rows[0].Meta)
	}
}

// ── Idempotency ────────────────────────────────────────────────────────────

// Stripe delivers at least once and retries on any non-2xx, so the same event
// id WILL arrive again. A replayed dispute must not produce a second audit row
// and a second ops alert.
func TestStripeWebhookDedupesReplayedEvents(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	seedPayment(t, w.taskID, w.requesterID, "pi_replay", paymentStatusCaptured)

	payload := stripeEventJSON("evt_replayed", "charge.dispute.created", `{
		"id": "dp_replay",
		"amount": 1200,
		"reason": "duplicate",
		"status": "needs_response",
		"charge": "ch_replay",
		"payment_intent": "pi_replay",
		"evidence_details": {"due_by": 1760000000, "has_evidence": false}
	}`)

	// Three deliveries of the same event id, each correctly signed — exactly
	// what a Stripe retry storm looks like.
	for i := 0; i < 3; i++ {
		if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i+1, code)
		}
	}

	if rows := auditRows(t, w.taskID, "PAYMENT_DISPUTED"); len(rows) != 1 {
		t.Errorf("audit rows after 3 deliveries = %d, want 1", len(rows))
	}

	var deliveries int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.stripe_webhook_events where event_id='evt_replayed'`,
	).Scan(&deliveries); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if deliveries != 1 {
		t.Errorf("stripe_webhook_events rows = %d, want 1", deliveries)
	}
}

// Deduplication is by event id, not by content: two genuinely different events
// about the same intent must both be processed.
func TestStripeWebhookDistinctEventsBothProcess(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedPayment(t, w.taskID, w.requesterID, "pi_two_events", paymentStatusAuthorized)

	first := stripeEventJSON("evt_first", "payment_intent.succeeded",
		`{"id":"pi_two_events","amount_received":1200}`)
	second := stripeEventJSON("evt_second", "payment_intent.canceled",
		`{"id":"pi_two_events"}`)

	if code := postSignedStripeWebhook(t, first); code != http.StatusOK {
		t.Fatalf("first: status = %d", code)
	}
	if code := postSignedStripeWebhook(t, second); code != http.StatusOK {
		t.Fatalf("second: status = %d", code)
	}

	if got := paymentStatusOf(t, "pi_two_events"); got != paymentStatusCanceled {
		t.Errorf("status = %q, want %q — the second event was dropped", got, paymentStatusCanceled)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

func seedPayment(t *testing.T, taskID, requesterID, intentID, status string) string {
	t.Helper()
	var id string
	err := db.QueryRow(context.Background(), `
		insert into public.payments
		  (task_id, requester_id, kind, stripe_payment_intent_id, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, $5, 5000)
		returning id::text
	`, taskID, requesterID, paymentKindTaskPayment, intentID, status).Scan(&id)
	if err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	return id
}

func paymentStatusOf(t *testing.T, intentID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(context.Background(),
		`select status from public.payments where stripe_payment_intent_id=$1`, intentID,
	).Scan(&status); err != nil {
		t.Fatalf("read payment status for %s: %v", intentID, err)
	}
	return status
}
