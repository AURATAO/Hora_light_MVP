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
//   - whether an online card PaymentIntent supports INCREMENTAL AUTHORIZATION.
//     This is the open question the phase was asked to answer, and the answer
//     decides whether an approved budget increase grows the existing hold or
//     opens a second one. The test asserts neither outcome — it asserts that
//     ensureHoldCoversTask ends up covered either way, and LOGS which path ran
//     so the answer is recorded rather than assumed.
//
//	docker run -d --rm --name hora-p2b-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	STRIPE_SMOKE=1 \
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Phase2bSmoke -v

import (
	"context"
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

// A capture cannot exceed its hold — which is why Capture clamps rather than
// letting Stripe refuse, and why the clamp logs UNDERCAPTURE when it bites.
func TestPhase2bSmokeCaptureIsClampedToTheHold(t *testing.T) {
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
		StripeCustomerID:      stripeCustomerIDFor(t, uid),
		StripePaymentMethodID: defaultPMFor(t, uid),
	})
	if err != nil {
		t.Fatalf("pre-auth: %v", err)
	}
	held := derefIntOr(p.AuthorizedCents, 0)

	// Ask for twice the hold. The clamp is what keeps this a capture rather
	// than a Stripe error surfaced to a supporter finishing a task.
	out := settleTaskPayment(context.Background(), taskID, held*2, 0)
	if out.Err != nil {
		t.Fatalf("settle: %v", out.Err)
	}
	if out.CapturedCents != held {
		t.Errorf("captured %s against a %s hold", formatCentsUSD(out.CapturedCents), formatCentsUSD(held))
	}
}

// THE OPEN QUESTION. Does a saved online card support incremental
// authorization, or does an approved budget increase need a second hold?
//
// Deliberately asserts the OUTCOME (the task ends up covered for what it can
// now settle at) rather than the METHOD, and logs the method. Stripe's support
// for incremental authorization on online payments is narrow and can change;
// a test that demanded one path would start failing for a reason that is not a
// bug in this codebase, and a test that demanded the other would stop noticing
// the day the good path became available.
func TestPhase2bSmokeBudgetIncreaseGrowsTheHoldSomehow(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "increase.smoke@example.test"
	uid, _ := seedRequesterWithCard(t, email, testPMVisa)
	taskID := seedSmokeTask(t, uid, email)
	setTaskBudget(t, taskID, 2000)

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
	held := derefIntOr(p.AuthorizedCents, 0)

	// A budget increase large enough that the existing hold cannot cover the
	// new ceiling: +$30 on a $20 budget.
	if _, err := db.Exec(context.Background(), `
		update public.tasks
		   set shopping_budget_approved_cents = shopping_budget_approved_cents + 3000
		 where id = $1::uuid
	`, taskID); err != nil {
		t.Fatalf("raise budget: %v", err)
	}

	needed := projectedSettlementCents(context.Background(), taskID)
	if needed <= held {
		t.Fatalf("the raised budget does not outgrow the hold (%s vs %s) — this test proves nothing",
			formatCentsUSD(needed), formatCentsUSD(held))
	}

	adj := ensureHoldCoversTask(context.Background(), taskID)
	if adj == nil {
		t.Fatal("no hold adjustment attempted on a task whose settlement outgrew its hold")
	}
	t.Logf("INCREMENTAL AUTHORIZATION RESULT: method=%q shortfall=%s authorized_now=%s err=%q",
		adj.Method, formatCentsUSD(adj.ShortfallCents), formatCentsUSD(adj.AuthorizedNow), adj.Err)

	if adj.Method == "failed" {
		t.Fatalf("neither an increment nor a supplementary hold worked: %s", adj.Err)
	}
	if adj.AuthorizedNow < needed {
		t.Errorf("authorized %s against a projected settlement of %s",
			formatCentsUSD(adj.AuthorizedNow), formatCentsUSD(needed))
	}

	// Whichever path ran, a settlement above the original hold must now be
	// collectable in full.
	settleAt := held + 1000
	out := settleTaskPayment(context.Background(), taskID, 1950, settleAt-1950)
	if out.Err != nil {
		t.Fatalf("settle: %v", out.Err)
	}
	if out.CapturedCents != settleAt {
		t.Errorf("captured %s of %s owed — the grown hold did not cover the settlement",
			formatCentsUSD(out.CapturedCents), formatCentsUSD(settleAt))
	}

	// And nothing is left holding the requester's money.
	var live int
	if err := db.QueryRow(context.Background(), `
		select count(*) from public.payments
		 where task_id = $1::uuid and status in ('requires_auth','authorized')
	`, taskID).Scan(&live); err != nil {
		t.Fatalf("count live holds: %v", err)
	}
	if live != 0 {
		t.Errorf("%d hold(s) still standing after settlement", live)
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
