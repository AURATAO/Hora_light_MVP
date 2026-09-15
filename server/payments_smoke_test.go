package main

// Live smoke test against the real Stripe test-mode API.
//
// This is NOT a unit test and does not run in CI or verify.sh. It requires BOTH
// STRIPE_SMOKE=1 and a real STRIPE_SECRET_KEY, and it skips otherwise — so
// `go test ./...` stays hermetic and offline.
//
// What it is for: everything the unit tests cannot reach. stripe_webhook_test.go
// proves the handler's logic against synthetic events it signs itself; this
// proves the other half — that payments.go can actually talk to Stripe, that a
// real PaymentIntent comes back with the shape we expect, and that the
// resulting webhook is delivered to and accepted by the DEPLOYED endpoint with
// a signature we did not forge.
//
//	docker run -d --rm --name hora-billing-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	STRIPE_SMOKE=1 \
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run PaymentsSmoke -v
//
// The payments row lives in the LOCAL test database on purpose: the production
// payments table is a financial ledger and a smoke test has no business
// writing a row into it against some real user's task. The consequence is
// expected and is the documented behaviour of onPaymentIntentStatus — the
// deployed webhook looks the intent up in the production database, does not
// find it, logs "no payments row for intent=… — ignoring" and returns 200.
// That path is itself covered by TestStripeWebhookAcceptsUnknownPaymentIntent.

import (
	"context"
	"os"
	"testing"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/paymentintent"
)

func TestPaymentsSmokeCreateAndRelease(t *testing.T) {
	if os.Getenv("STRIPE_SMOKE") != "1" {
		t.Skip("STRIPE_SMOKE != 1 — skipping live Stripe smoke test")
	}
	if os.Getenv("STRIPE_SECRET_KEY") == "" {
		t.Skip("STRIPE_SECRET_KEY not set — skipping live Stripe smoke test")
	}

	setupStripeWebhookDB(t)

	// Refuse to run against live keys. A smoke test that creates real holds on
	// real cards is not a smoke test.
	if !stripeIsTestMode() {
		t.Fatal("STRIPE_SECRET_KEY is not a test key (sk_test_/rk_test_) — refusing to run")
	}
	if !paymentsEnabled() {
		t.Fatal("paymentsEnabled() is false despite STRIPE_SECRET_KEY being set")
	}

	w := seedOpsWorld(t, "open")
	ctx := context.Background()

	// A 60-minute standard task with a $20.00 shopping budget:
	//   $12.00 base + 45 billable min x $0.50 = $34.50, + $20.00 budget = $54.50.
	//
	// EXACTLY the quote. No multiplier, no buffer — the billing restructure
	// made the hold equal to what the requester is shown, which is why this
	// number dropped from $81.75.
	const wantAmount = 5450
	in := PreAuthInput{
		TaskID:              w.taskID,
		RequesterID:         w.requesterID,
		Category:            "standard",
		EstimatedMinutes:    60,
		ShoppingBudgetCents: 2000,
	}
	if got := preAuthAmountCents(in.Category, in.EstimatedMinutes, in.ShoppingBudgetCents, Billing.PerMinuteRateCents); got != wantAmount {
		t.Fatalf("pre-auth amount = %d, want %d", got, wantAmount)
	}

	// ── create ──────────────────────────────────────────────────────────────
	p, err := CreatePreAuth(ctx, in)
	if err != nil {
		t.Fatalf("CreatePreAuth: %v", err)
	}
	t.Logf("PAYMENT_INTENT=%s", p.StripePaymentIntentID)
	t.Logf("PAYMENT_ROW=%s status=%s", p.ID, p.Status)

	if p.StripePaymentIntentID == "" {
		t.Fatal("no PaymentIntent id recorded")
	}

	// Read it back from Stripe rather than trusting our own return value.
	pi, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		t.Fatalf("fetch intent: %v", err)
	}
	if pi.Amount != wantAmount {
		t.Errorf("Stripe amount = %d, want %d", pi.Amount, wantAmount)
	}
	if string(pi.Currency) != Billing.Currency {
		t.Errorf("currency = %q, want %q", pi.Currency, Billing.Currency)
	}
	// The whole model depends on this: automatic capture would take the full
	// held amount at authorization, the opposite of billing for time worked.
	if pi.CaptureMethod != stripe.PaymentIntentCaptureMethodManual {
		t.Errorf("capture_method = %q, want manual", pi.CaptureMethod)
	}
	// Reconciliation handles — given a dashboard row, which task is this?
	for k, want := range map[string]string{
		"task_id":      w.taskID,
		"requester_id": w.requesterID,
		"payment_id":   p.ID,
		"kind":         paymentKindTaskPayment,
	} {
		if pi.Metadata[k] != want {
			t.Errorf("metadata[%s] = %q, want %q", k, pi.Metadata[k], want)
		}
	}

	// ── release ─────────────────────────────────────────────────────────────
	// This is what fires payment_intent.canceled at the deployed endpoint.
	released, err := Release(ctx, w.taskID)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released == nil {
		t.Fatal("Release returned nil — it found no live payment for the task")
	}
	if released.Status != paymentStatusCanceled {
		t.Errorf("row status = %q, want %q", released.Status, paymentStatusCanceled)
	}

	after, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		t.Fatalf("re-fetch intent: %v", err)
	}
	if after.Status != stripe.PaymentIntentStatusCanceled {
		t.Errorf("Stripe status = %q, want canceled", after.Status)
	}

	if got := paymentStatusOf(t, p.StripePaymentIntentID); got != paymentStatusCanceled {
		t.Errorf("persisted status = %q, want %q", got, paymentStatusCanceled)
	}

	// Release is idempotent — "release this hold" is satisfied by a hold that
	// is already released, which matters because Phase 2 calls it on cancel
	// paths that may run more than once.
	if _, err := Release(ctx, w.taskID); err != nil {
		t.Errorf("second Release should be a no-op, got: %v", err)
	}

	t.Logf("SMOKE OK — intent %s created ($%.2f, manual capture) and canceled",
		p.StripePaymentIntentID, float64(wantAmount)/100)
	t.Logf("Now check Render logs for: [stripe][webhook] event=evt_… type=payment_intent.canceled")
}
