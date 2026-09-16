package main

// Phase 3 against the real Stripe test-mode API.
//
// Same contract as the other three smoke files: needs BOTH STRIPE_SMOKE=1 and
// a real test STRIPE_SECRET_KEY, skips otherwise so `go test ./...` stays
// hermetic, and refuses outright to run against a live key.
//
// What ONLY this can prove, because every one of them is a claim about
// Stripe's behaviour rather than ours:
//
//   - that a `type=express` account can actually be created with only the
//     `transfers` capability, and comes back not-yet-payable with a list of
//     requirements. The whole onboarding state machine is built on the shape
//     of that response.
//   - that an Account Link of type account_onboarding can be minted for it,
//     which is the single URL the entire onboarding flow consists of.
//   - THAT A TRANSFER WITH source_transaction SUCCEEDS AGAINST A ZERO
//     AVAILABLE BALANCE. This is the load-bearing claim of the whole phase. A
//     bare transfer fails with `balance_insufficient` on a fresh test account —
//     which is every test account — and the only reason the payout path works
//     at all is that tying the transfer to its funding charge makes Stripe
//     accept it now and execute it when those funds settle. Nothing offline
//     can check this, and getting it wrong means every supporter payout fails
//     in production on day one.
//   - that transfer_group survives onto the transfer and matches the charge's,
//     so the two sides of one task's money are grouped in the dashboard.
//   - that the idempotency key actually prevents a second transfer.
//
//	docker run -d --rm --name hora-p3-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	STRIPE_SMOKE=1 \
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Phase3Smoke -v

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/account"
	"github.com/stripe/stripe-go/v86/transfer"
)

// An Express account, created the way connectAccountFor creates one, and the
// onboarding link that is the whole flow.
//
// Deletes the account on cleanup: test-mode connected accounts are free but
// they accumulate in the platform's Connect dashboard, and a smoke suite that
// litters it stops being run.
func TestPhase3SmokeExpressAccountAndOnboardingLink(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	uid := seedSmokeSupporter(t, "express.smoke@example.test")

	accountID, err := connectAccountFor(context.Background(), uid, "express.smoke@example.test")
	requireAccountsV1(t, err)
	if err != nil {
		t.Fatalf("create express account: %v", err)
	}
	t.Cleanup(func() { _, _ = account.Del(accountID, nil) })
	t.Logf("created %s", accountID)

	if !strings.HasPrefix(accountID, "acct_") {
		t.Errorf("account id %q does not look like a connected account", accountID)
	}

	acct, err := account.GetByID(accountID, &stripe.AccountParams{})
	if err != nil {
		t.Fatalf("read back account: %v", err)
	}

	// The three properties the phase depends on. `type` in particular: a
	// controller-properties account reports "none", and login links plus the
	// Express dashboard are defined against Express — so this asserts we
	// actually got what connectAccountFor's comment claims.
	if string(acct.Type) != "express" {
		t.Errorf("account type = %q, want express — login links and the Express dashboard depend on it", acct.Type)
	}
	if acct.Country != connectAccountCountry {
		t.Errorf("country = %q, want %q (immutable once created)", acct.Country, connectAccountCountry)
	}
	if acct.PayoutsEnabled {
		t.Error("a brand-new account is already payouts_enabled — the gate would let anyone through")
	}

	// A fresh account has requirements. The Earnings screen's "in progress"
	// state is built on this list being non-empty before onboarding and empty
	// after, so an empty one here would mean the state machine never advances.
	due := requirementsDue(acct)
	if len(due) == 0 {
		t.Error("a fresh account reports no requirements — the onboarding state machine has nothing to show")
	}
	t.Logf("requirements currently due: %v", due)

	// And what the whole flow actually consists of: one URL.
	if _, err := connectAccountFor(context.Background(), uid, "express.smoke@example.test"); err != nil {
		t.Fatalf("second call should return the stored account, not create one: %v", err)
	}

	link := mustOnboardingLink(t, accountID)
	if !strings.HasPrefix(link, "https://connect.stripe.com/") {
		t.Errorf("onboarding link %q is not a Stripe Connect URL", link)
	}
	t.Logf("onboarding link (single-use): %s", link)
}

// THE ONE THAT MATTERS. A transfer funded by source_transaction against a
// platform balance that has not settled.
//
// Run this and nothing else if you only run one: it is the difference between
// supporters being paid and every payout failing with `balance_insufficient`.
func TestPhase3SmokeTransferIsFundedByItsCharge(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	// A requester with a card, a task, a hold, and a capture — the whole Phase
	// 2 path, because the charge it produces is what funds the transfer.
	const requester = "payout.smoke@example.test"
	requesterUID, _ := seedRequesterWithCard(t, requester, testPMVisa)
	taskID := seedSmokeTask(t, requesterUID, requester)

	p, err := CreatePreAuth(context.Background(), PreAuthInput{
		TaskID:                taskID,
		RequesterID:           requesterUID,
		Category:              "delivery",
		EstimatedMinutes:      30,
		ShoppingBudgetCents:   0,
		StripeCustomerID:      stripeCustomerIDFor(t, requesterUID),
		StripePaymentMethodID: defaultPMFor(t, requesterUID),
	})
	if err != nil {
		t.Fatalf("pre-auth: %v", err)
	}

	captured, err := Capture(context.Background(), taskID, 2450, 0)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	// Recorded at capture, and the reason the transfer below can be funded.
	// An empty one here means the latest_charge expand was lost, which fails
	// silently everywhere else — the capture still works and every payout
	// quietly starts drawing on the platform balance instead.
	if captured.StripeChargeID == "" {
		t.Fatal("capture recorded no charge id — every transfer would fall back to the platform balance")
	}
	t.Logf("captured %s via %s", formatCentsUSD(derefIntOr(captured.CapturedCents, 0)), captured.StripeChargeID)

	// An onboarded supporter. Test-mode accounts created this way are not
	// payouts_enabled — you cannot complete hosted onboarding from a test — so
	// the transfer is asserted against an account with the transfers
	// capability requested. That is enough to prove the funding mechanism,
	// which is what this test is for.
	supporterUID := seedSmokeSupporter(t, "supporter.smoke@example.test")
	acctID, err := connectAccountFor(context.Background(), supporterUID, "supporter.smoke@example.test")
	requireAccountsV1(t, err)
	if err != nil {
		t.Fatalf("supporter account: %v", err)
	}
	t.Cleanup(func() { _, _ = account.Del(acctID, nil) })
	mustExec(t, `update public.tasks set assigned_to_id = $2::uuid where id = $1::uuid`, taskID, supporterUID)

	payout := payoutForTask(context.Background(), payoutInput{
		TaskID:         taskID,
		SupporterID:    supporterUID,
		PaymentID:      captured.ID,
		OwedCents:      2450,
		SourceChargeID: captured.StripeChargeID,
	})
	if payout == nil {
		t.Fatal("no payout row was produced at all")
	}
	if payout.Status != payoutStatusPaid {
		t.Fatalf("payout is %q, not paid — see the row's meta for Stripe's reason (%s)",
			payout.Status, payoutMeta(t, payout.ID)["error"])
	}
	t.Logf("transferred %s as %s", formatCentsUSD(payout.AmountCents), payout.StripeTransferID)

	tr, err := transfer.Get(payout.StripeTransferID, nil)
	if err != nil {
		t.Fatalf("read back transfer: %v", err)
	}

	// Beta takes nothing, so the supporter gets the whole settlement.
	if tr.Amount != 2450 {
		t.Errorf("transferred %s, want the full $24.50 — beta takes no cut", formatCentsUSD(int(tr.Amount)))
	}
	// The grouping, on both sides of the task's money.
	if tr.TransferGroup != taskID {
		t.Errorf("transfer_group = %q, want the task id %q", tr.TransferGroup, taskID)
	}
	// And the claim this whole test exists for: the transfer is drawn on the
	// charge, not on the platform's available balance.
	if tr.SourceTransaction == nil || tr.SourceTransaction.ID != captured.StripeChargeID {
		t.Errorf("source_transaction = %v, want the funding charge %s — this transfer would have needed a settled platform balance",
			tr.SourceTransaction, captured.StripeChargeID)
	}
	if tr.Destination == nil || tr.Destination.ID != acctID {
		t.Errorf("destination = %v, want %s", tr.Destination, acctID)
	}

	// A second settle of the same payment must not send a second transfer. The
	// unique index catches it before Stripe is reached; this proves the whole
	// path declines rather than just the index.
	if again := payoutForTask(context.Background(), payoutInput{
		TaskID: taskID, SupporterID: supporterUID, PaymentID: captured.ID,
		OwedCents: 2450, SourceChargeID: captured.StripeChargeID,
	}); again != nil {
		t.Errorf("a second settle produced payout %s — the supporter would be paid twice", again.ID)
	}
	if n := payoutCount(t, taskID); n != 1 {
		t.Errorf("%d payout rows for one payment", n)
	}

	_ = p
}

// A transfer WITHOUT source_transaction, to prove the failure this phase is
// designed around is real rather than theoretical.
//
// If this ever starts passing, the platform test balance has been topped up
// and the test is no longer proving anything — it skips rather than fails in
// that case, because a funded balance is not a defect.
func TestPhase3SmokeBareTransferFailsOnAnEmptyBalance(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	supporterUID := seedSmokeSupporter(t, "bare.transfer@example.test")
	acctID, err := connectAccountFor(context.Background(), supporterUID, "bare.transfer@example.test")
	requireAccountsV1(t, err)
	if err != nil {
		t.Fatalf("supporter account: %v", err)
	}
	t.Cleanup(func() { _, _ = account.Del(acctID, nil) })

	_, err = transfer.New(&stripe.TransferParams{
		Amount:      stripe.Int64(2450),
		Currency:    stripe.String(Billing.Currency),
		Destination: stripe.String(acctID),
	})
	if err == nil {
		t.Skip("the platform test balance is funded — this test can no longer demonstrate the failure")
	}
	if !strings.Contains(err.Error(), "balance_insufficient") {
		t.Logf("bare transfer failed for a different reason: %v", err)
	}
	t.Logf("confirmed: a transfer with no source_transaction fails — %v", err)
}

// ── Fixtures ───────────────────────────────────────────────────────────────

// requireAccountsV1 turns the one platform-configuration failure this suite
// cannot fix into a SKIP with instructions, rather than a failure that looks
// like a bug in this code.
//
// Stripe refuses v1 connected-account creation by default on platforms
// onboarded since it began steering new integrations to Accounts v2. Every
// test below needs a connected account, so without the toggle none of them can
// say anything — and reporting that as a red suite would send the next reader
// hunting through payments_connect.go for a fault that is not there.
func requireAccountsV1(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, errConnectV1Disabled) {
		t.Skip("Accounts v1 support is OFF on this Stripe platform — enable it at " +
			"https://dashboard.stripe.com/settings/features/feat_accounts_v1_support " +
			"(Settings → Features → Accounts v1 support), then re-run. " +
			"Until then no Express account can be created and none of these tests can run.")
	}
}

func seedSmokeSupporter(t *testing.T, email string) string {
	t.Helper()
	var uid string
	if err := db.QueryRow(context.Background(),
		`insert into public.users (email, name) values ($1, $2) returning id::text`,
		email, "Smoke Supporter",
	).Scan(&uid); err != nil {
		t.Fatalf("seed supporter: %v", err)
	}
	return uid
}

func mustOnboardingLink(t *testing.T, accountID string) string {
	t.Helper()
	link, err := accountLinkFor(accountID)
	if err != nil {
		t.Fatalf("account link: %v", err)
	}
	return link
}
