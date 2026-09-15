package main

// Phase 2b against the real Stripe test-mode API.
//
// Same contract as the other two smoke files: needs BOTH STRIPE_SMOKE=1 and a
// real test STRIPE_SECRET_KEY, skips otherwise so `go test ./...` stays
// hermetic, and refuses outright to run against a live key.
//
// What ONLY this can prove, because every one of them is a claim about
// Stripe's behaviour rather than ours:
//
//   - a manual-capture hold can actually be captured for LESS than it holds,
//     and the remainder releases itself — the entire "you only pay for the
//     time worked" promise, which involves no refund
//   - a capture cannot exceed its hold, which is why Capture clamps
//   - that a settlement which OUTGROWS its hold can be collected as a second
//     immediate charge on the same saved card, right after the first intent
//     was captured. The hold is now exactly the estimate, so this is the path
//     every overrun takes — and whether Stripe permits it is a fact about
//     Stripe, not about this code.
//
//	docker run -d --rm --name hora-p2b-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	STRIPE_SMOKE=1 \
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Phase2bSmoke -v

import (
	"context"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/paymentintent"
)

// The promise, against the real API: hold more than you take, take what was
// worked, and the rest goes back on its own.
func TestPhase2bSmokeCaptureLessThanTheHoldReleasesTheRest(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "capture.smoke@example.test"
	uid, _ := seedRequesterWithCard(t, email, testPMVisa)
	taskID := seedSmokeTask(t, uid, email)

	p, err := CreatePreAuth(context.Background(), PreAuthInput{
		TaskID:                taskID,
		RequesterID:           uid,
		Category:              "delivery",
		EstimatedMinutes:      30,
		ShoppingBudgetCents:   2000,
		StripeCustomerID:      stripeCustomerIDFor(t, uid),
		StripePaymentMethodID: defaultPMFor(t, uid),
	})
	if err != nil {
		t.Fatalf("pre-auth: %v", err)
	}
	if p.Status != paymentStatusAuthorized {
		t.Fatalf("pre-auth came back %q, not authorized", p.Status)
	}
	held := derefIntOr(p.AuthorizedCents, 0)
	t.Logf("held %s", formatCentsUSD(held))

	// The display card, which only a real authorization can produce: it comes
	// off the expanded charge on the intent, and getting the expand wrong
	// fails silently — the hold still lands, and the requester is simply told
	// "reserved on your card" with no card named. pm_card_visa is 4242.
	if p.CardBrand != "visa" || p.CardLast4 != "4242" {
		t.Errorf("card not read off the authorization: brand=%q last4=%q", p.CardBrand, p.CardLast4)
	}
	var rowBrand, rowLast4 string
	if err := db.QueryRow(context.Background(),
		`select coalesce(card_brand,''), coalesce(card_last4,'') from public.payments where id=$1::uuid`,
		p.ID).Scan(&rowBrand, &rowLast4); err != nil {
		t.Fatalf("read display card: %v", err)
	}
	if rowBrand != "visa" || rowLast4 != "4242" {
		t.Errorf("card not persisted: brand=%q last4=%q", rowBrand, rowLast4)
	}
	// And it is what the requester's task payload will carry.
	if view := taskPaymentView(context.Background(), taskID); view == nil || view.CardLast4 != "4242" {
		t.Errorf("taskPaymentView does not name the card: %+v", view)
	}

	// Settle at well under the hold: 30 minutes and a $17.40 receipt.
	const timeCost, receipt = 1950, 1740
	out := settleTaskPayment(context.Background(), taskID, timeCost, receipt)
	if out.Err != nil {
		t.Fatalf("settle: %v", out.Err)
	}
	if out.CapturedCents != timeCost+receipt {
		t.Errorf("captured %s, want %s", formatCentsUSD(out.CapturedCents), formatCentsUSD(timeCost+receipt))
	}

	// Stripe is the authority on what moved and on what happened to the rest.
	pi, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		t.Fatalf("fetch intent: %v", err)
	}
	if pi.Status != stripe.PaymentIntentStatusSucceeded {
		t.Errorf("intent status = %q, want succeeded", pi.Status)
	}
	if int(pi.AmountReceived) != timeCost+receipt {
		t.Errorf("Stripe took %s, we asked for %s",
			formatCentsUSD(int(pi.AmountReceived)), formatCentsUSD(timeCost+receipt))
	}
	// The uncaptured remainder is gone from the cardholder's available balance
	// without any refund having been issued — that is the whole design.
	if int(pi.Amount) != held {
		t.Errorf("authorized amount changed under us: %d vs %d", pi.Amount, held)
	}
	t.Logf("captured %s of a %s hold; %s released with no refund",
		formatCentsUSD(int(pi.AmountReceived)), formatCentsUSD(held),
		formatCentsUSD(held-int(pi.AmountReceived)))

	if got := paymentStatusByID(t, p.ID); got != paymentStatusCaptured {
		t.Errorf("payment status = %q, want captured", got)
	}
}

// A single capture cannot exceed its hold — so settlement takes the hold in
// full and collects the rest separately.
//
// The clamp inside Capture is still real and still logs UNDERCAPTURE; what
// changed is that the shortfall is no longer written off. This asserts both
// halves: the capture stops at the hold, and the requester is nonetheless
// charged everything they owe.
func TestPhase2bSmokeCaptureIsClampedAndTheRestCollected(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "clamp.smoke@example.test"
	uid, _ := seedRequesterWithCard(t, email, testPMVisa)
	taskID := seedSmokeTask(t, uid, email)

	p, err := CreatePreAuth(context.Background(), PreAuthInput{
		TaskID:                taskID,
		RequesterID:           uid,
		Category:              "delivery",
		EstimatedMinutes:      30,
		RateCents:             Billing.PerMinuteRateCents,
		StripeCustomerID:      stripeCustomerIDFor(t, uid),
		StripePaymentMethodID: defaultPMFor(t, uid),
	})
	if err != nil {
		t.Fatalf("pre-auth: %v", err)
	}
	held := derefIntOr(p.AuthorizedCents, 0)

	// Owe twice the hold.
	owed := held * 2
	out := settleTaskPayment(context.Background(), taskID, owed, 0)
	if out.Err != nil {
		t.Fatalf("settle: %v", out.Err)
	}
	if out.CapturedCents != owed {
		t.Errorf("collected %s of %s owed", formatCentsUSD(out.CapturedCents), formatCentsUSD(owed))
	}

	// The hold itself was taken for exactly its own amount — not more.
	pi, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		t.Fatalf("fetch intent: %v", err)
	}
	if int(pi.AmountReceived) != held {
		t.Errorf("the hold was captured for %s, not its %s",
			formatCentsUSD(int(pi.AmountReceived)), formatCentsUSD(held))
	}
}

// A settlement that outgrows its hold, against the real API.
//
// This is the branch the restructure creates and the one no offline test can
// prove: the hold is now exactly the estimate, so a task that runs over has to
// be collected with a SECOND charge on the saved card — a different intent, an
// immediate capture, off-session, after the first one is already captured.
// Whether Stripe lets you do that to a customer you have just captured from is
// a fact about Stripe.
func TestPhase2bSmokeOverageIsChargedAsASecondPayment(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "overage.smoke@example.test"
	uid, _ := seedRequesterWithCard(t, email, testPMVisa)
	taskID := seedSmokeTask(t, uid, email)

	p, err := CreatePreAuth(context.Background(), PreAuthInput{
		TaskID:                taskID,
		RequesterID:           uid,
		Category:              "delivery",
		EstimatedMinutes:      30,
		RateCents:             Billing.PerMinuteRateCents,
		StripeCustomerID:      stripeCustomerIDFor(t, uid),
		StripePaymentMethodID: defaultPMFor(t, uid),
	})
	if err != nil {
		t.Fatalf("pre-auth: %v", err)
	}
	held := derefIntOr(p.AuthorizedCents, 0)
	// The hold is the bare estimate now: $12.00 + 15 billable x $0.50.
	if held != 1950 {
		t.Fatalf("hold %s, want $19.50 — the hold is no longer the bare estimate", formatCentsUSD(held))
	}

	// Settle for $10 more than was ever reserved.
	const overBy = 1000
	out := settleTaskPayment(context.Background(), taskID, held+overBy, 0)
	if out.Err != nil {
		t.Fatalf("settle: %v", out.Err)
	}
	if out.BalanceDueCents != 0 {
		t.Fatalf("a good card left %s outstanding", formatCentsUSD(out.BalanceDueCents))
	}
	if out.CapturedCents != held+overBy {
		t.Errorf("collected %s of %s owed", formatCentsUSD(out.CapturedCents), formatCentsUSD(held+overBy))
	}

	// Two rows: the hold, captured in full, and the balance charge.
	var kinds string
	if err := db.QueryRow(context.Background(), `
		select string_agg(kind || ':' || status, ', ' order by created_at)
		  from public.payments where task_id = $1::uuid
	`, taskID).Scan(&kinds); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	t.Logf("ledger: %s", kinds)
	if !strings.Contains(kinds, "completion_balance:captured") {
		t.Errorf("no captured completion_balance row: %s", kinds)
	}
	// And nothing is left owing.
	if owed := outstandingBalanceFor(context.Background(), uid); owed != nil {
		t.Errorf("balance outstanding after a successful collection: %+v", owed)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

func seedSmokeTask(t *testing.T, requesterID, requesterEmail string) string {
	t.Helper()
	var taskID string
	if err := db.QueryRow(context.Background(), `
		insert into public.tasks (title, category, estimated_minutes, requester, requester_id, status)
		values ('Smoke settlement', 'delivery', 30, $1, $2::uuid, 'open')
		returning id::text
	`, requesterEmail, requesterID).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return taskID
}

func stripeCustomerIDFor(t *testing.T, uid string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(),
		`select coalesce(stripe_customer_id,'') from public.users where id=$1::uuid`, uid).Scan(&id); err != nil {
		t.Fatalf("read customer id: %v", err)
	}
	return id
}

func defaultPMFor(t *testing.T, uid string) string {
	t.Helper()
	pm, err := defaultPaymentMethodFor(stripeCustomerIDFor(t, uid))
	if err != nil {
		t.Fatalf("default payment method: %v", err)
	}
	return pm
}
