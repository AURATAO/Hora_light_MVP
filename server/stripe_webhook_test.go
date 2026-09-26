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
	"github.com/stripe/stripe-go/v86"
)

const testStripeWebhookSecret = "whsec_test_secret_for_unit_tests"

// paymentsMigrationPath is the real migration, applied by the test setup. If
// it stops being valid SQL, these tests fail before any handler runs — which
// is the point of applying it rather than restating its DDL here.
const paymentsMigrationPath = "../supabase/migrations/20260911120000_payments_and_billing_schema.sql"

// phase2aMigrationPath adds users.stripe_customer_id, tasks.payment_id and the
// 'pending_payment' status. Applied after the payments migration because
// tasks.payment_id references payments(id).
const phase2aMigrationPath = "../supabase/migrations/20260914120000_stripe_customer_and_task_payment_linkage.sql"

// phase2bMigrationPath adds payments.kind 'budget_increase', status
// 'capture_failed' and the tasks.settled_* read model. Applied last because it
// rewrites the CHECK constraints the payments migration created.
const phase2bMigrationPath = "../supabase/migrations/20260914150000_payments_capture_failed_and_budget_increase.sql"

// cardDisplayMigrationPath adds payments.card_brand / card_last4 — the card a
// hold was authorized against, which the requester's confirmation names.
const cardDisplayMigrationPath = "../supabase/migrations/20260915120000_payments_card_display.sql"

// restructureMigrationPath is the billing restructure: tasks.rate_cents_per_min,
// the dropped shopping-budget cap, and the payments values a completion
// balance needs. Last, because it rewrites CHECKs the earlier ones created.
const restructureMigrationPath = "../supabase/migrations/20260915130000_billing_restructure.sql"

// Phase 3: users.stripe_account_id + the status cache, public.payouts, and
// payments.stripe_charge_id. Applied rather than restated, like every
// migration in this fixture — a hand-copied CREATE TABLE keeps passing after
// the shipped one diverges from it, and for a money table that is exactly the
// divergence nobody would notice.
const connectPayoutsMigrationPath = "../supabase/migrations/20260916120000_connect_payouts.sql"

// users.stripe_transfers_active — the second half of the accept gate, added
// after a payouts_enabled=true account had a transfer refused (2026-09-22).
const transfersActiveMigrationPath = "../supabase/migrations/20260922222707_stripe_transfers_active.sql"

// payouts.stripe_destination_payment / stripe_bank_payout_id / bank_paid_at —
// the bank leg, so the Earnings screen can split earned into in-transit and
// paid-out.
const bankArrivalMigrationPath = "../supabase/migrations/20260922224407_payouts_bank_arrival.sql"

// Promo codes: promo_codes, promo_redemptions, and payouts.funding with a
// nullable payment_id for the platform-funded subsidy transfer. After the
// payouts table it alters.
const promoCodesMigrationPath = "../supabase/migrations/20260923104026_promo_codes.sql"

// The platform fee: service_cents / fee_cents / reimbursement_cents / fee_bps
// on payouts, with the CHECK that they reconcile to amount_cents (D-14).
const platformFeeMigrationPath = "../supabase/migrations/20260926145917_payouts_platform_fee_breakdown.sql"

func setupStripeWebhookDB(t *testing.T) {
	t.Helper()
	setupAdminOpsDB(t) // users, tasks, worklogs, audit_logs + the pool swap

	// adminOpsFixture drops only the tables it declares, and the migration
	// below is all IF NOT EXISTS — so without this, payments rows from a
	// previous run survive into a fresh fixture and collide on the intent-id
	// unique constraint. Dropped AFTER setupAdminOpsDB so that tasks and users
	// exist for the migration's foreign keys.
	if _, err := db.Exec(context.Background(), `
		DROP TABLE IF EXISTS public.promo_redemptions CASCADE;
		DROP TABLE IF EXISTS public.promo_codes CASCADE;
		DROP TABLE IF EXISTS public.payouts CASCADE;
		DROP TABLE IF EXISTS public.payments CASCADE;
		DROP TABLE IF EXISTS public.stripe_webhook_events CASCADE;
	`); err != nil {
		t.Fatalf("drop payments tables: %v", err)
	}

	// Supabase always has these roles; a bare postgres:16 container does not,
	// and the migration's REVOKE statements name them explicitly. Creating
	// them here keeps the suite self-contained on a fresh container AND means
	// the REVOKEs are actually exercised rather than skipped — they are the
	// part of that migration guarding the payments ledger against the schema's
	// ALTER DEFAULT PRIVILEGES, so a test run that silently skipped them would
	// be testing the wrong migration.
	if _, err := db.Exec(context.Background(), `
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='anon') THEN
		    CREATE ROLE anon NOLOGIN;
		  END IF;
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN
		    CREATE ROLE authenticated NOLOGIN;
		  END IF;
		END $$;
	`); err != nil {
		t.Fatalf("create supabase roles: %v", err)
	}

	for _, path := range []string{paymentsMigrationPath, phase2aMigrationPath, phase2bMigrationPath, cardDisplayMigrationPath,
		restructureMigrationPath, connectPayoutsMigrationPath, transfersActiveMigrationPath,
		bankArrivalMigrationPath, promoCodesMigrationPath, platformFeeMigrationPath} {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := db.Exec(context.Background(), string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", path, err)
		}
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

// testEventAPIVersion is the API version stamped on synthetic events.
//
// It is deliberately NOT stripe.APIVersion, and this is the whole point. These
// tests originally hardcoded "2024-06-20" — the exact version the SDK expected
// — so ConstructEvent's compatibility check could never fail and the suite
// stayed green while the deployed endpoint 401'd every real webhook Stripe
// sent, because the live account stamps events with its own, newer version.
// A test that feeds the code its own expectations back is not testing that
// check at all.
//
// So: a real account-shaped version from the same release train as the SDK,
// with a DIFFERENT date. That is exactly the case production produces, and it
// exercises the train-based comparison rather than trivially satisfying it.
var testEventAPIVersion = differentDateSameTrain(stripe.APIVersion)

// differentDateSameTrain turns "2026-08-26.dahlia" into "2026-01-15.dahlia":
// same release train, different date. Versions with no train (the pre-2025
// "2024-06-20" style) are returned unchanged, since for those the SDK requires
// an exact match and there is no train to preserve.
func differentDateSameTrain(v string) string {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return v
	}
	return "2026-01-15." + parts[1]
}

func stripeEventJSON(id, eventType, dataObject string) string {
	return fmt.Sprintf(`{"id":%q,"object":"event","type":%q,"api_version":%q,
	                     "created":%d,"data":{"object":%s}}`,
		id, eventType, testEventAPIVersion, time.Now().Unix(), dataObject)
}

// The regression test for the bug the live smoke test found: stripe-go v79
// required the event's API version to equal the SDK's exactly, so an account
// on any newer version had every webhook rejected as a signature failure —
// a 401, which makes Stripe retry for days and then disable the endpoint.
// v86 compares only the release train. This asserts we are on an SDK that
// does, and that a same-train event is accepted.
func TestStripeWebhookAcceptsSameTrainAPIVersion(t *testing.T) {
	setupStripeWebhookDB(t)

	if !strings.Contains(stripe.APIVersion, ".") {
		t.Fatalf("stripe.APIVersion %q has no release train — this SDK demands an "+
			"exact API-version match and will 401 every event from a live account",
			stripe.APIVersion)
	}
	if testEventAPIVersion == stripe.APIVersion {
		t.Fatal("synthetic events carry the SDK's own version — the compatibility check is untested")
	}

	payload := stripeEventJSON("evt_api_version", "payment_intent.canceled",
		`{"id":"pi_api_version_probe"}`)
	if code := postSignedStripeWebhook(t, payload); code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an event on the same release train (%s vs SDK %s)",
			code, testEventAPIVersion, stripe.APIVersion)
	}
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
