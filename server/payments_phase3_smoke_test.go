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
	"os"
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

	// Settle for MORE than the hold on purpose, so the capture clamps — the
	// ordinary overrun. What comes back is what the charge is actually worth,
	// and it is the ceiling on what can be transferred against it.
	captured, err := Capture(context.Background(), taskID, 2450, 0)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	capturedCents := derefIntOr(captured.CapturedCents, 0)
	if capturedCents <= 0 {
		t.Fatalf("capture took nothing")
	}
	// Recorded at capture, and the reason the transfer below can be funded.
	// An empty one here means the latest_charge expand was lost, which fails
	// silently everywhere else — the capture still works and every payout
	// quietly starts drawing on the platform balance instead.
	if captured.StripeChargeID == "" {
		t.Fatal("capture recorded no charge id — every transfer would fall back to the platform balance")
	}
	t.Logf("captured %s via %s", formatCentsUSD(capturedCents), captured.StripeChargeID)

	supporterUID := seedSmokeSupporter(t, "supporter.smoke@example.test")
	acctID := onboardedSmokeAccount(t, supporterUID)
	mustExec(t, `update public.tasks set assigned_to_id = $2::uuid where id = $1::uuid`, taskID, supporterUID)

	// THE CONSTRAINT settlementPayouts IS BUILT AROUND, proven rather than
	// assumed: a transfer tied to a charge may not exceed it. This is why an
	// overrunning task pays out across TWO transfers — one per funding charge
	// — instead of one transfer for the whole settlement. Nothing offline can
	// establish it, and if Stripe ever relaxed it this test would be the only
	// thing that noticed.
	if _, err := transfer.New(&stripe.TransferParams{
		Amount:            stripe.Int64(int64(capturedCents + 100)),
		Currency:          stripe.String(Billing.Currency),
		Destination:       stripe.String(acctID),
		SourceTransaction: stripe.String(captured.StripeChargeID),
	}); err == nil {
		t.Error("Stripe allowed a transfer ABOVE its source charge — settlementPayouts' cap is no longer load-bearing")
	} else if !strings.Contains(err.Error(), "must not exceed the source amount") {
		t.Logf("over-charge transfer refused for another reason: %v", err)
	} else {
		t.Logf("confirmed: a transfer may not exceed its source charge")
	}

	// And the real thing: transfer exactly what the charge is worth, which is
	// what settlementPayouts would ask for on this settlement.
	payout := payoutForTask(context.Background(), payoutInput{
		TaskID:         taskID,
		SupporterID:    supporterUID,
		PaymentID:      captured.ID,
		OwedCents:      capturedCents,
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

	// Beta takes nothing, so the supporter gets the whole captured amount.
	if int(tr.Amount) != capturedCents {
		t.Errorf("transferred %s of a %s charge — beta takes no cut",
			formatCentsUSD(int(tr.Amount)), formatCentsUSD(capturedCents))
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
		OwedCents: capturedCents, SourceChargeID: captured.StripeChargeID,
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
	acctID := onboardedSmokeAccount(t, supporterUID)

	_, err := transfer.New(&stripe.TransferParams{
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

// smokeConnectAccountEnv names an ALREADY-ONBOARDED test-mode Express account
// for the transfer tests to pay into.
//
// WHY THIS CANNOT BE AUTOMATED. A freshly created Express account has the
// `transfers` capability REQUESTED but not ACTIVE, and every transfer to it is
// refused with insufficient_capabilities_for_transfer. The capability only
// activates once onboarding completes — and onboarding cannot be completed by
// API, because Stripe refuses ToS acceptance on any account where it collects
// the requirements itself:
//
//	"You cannot accept the Terms of Service on behalf of accounts where
//	 controller[requirement_collection]=stripe, which includes Standard and
//	 Express accounts."
//
// That is Express working as designed, and it is the same fact the accept gate
// exists to enforce in production: a supporter must be payouts_enabled BEFORE
// they work a task, so by settlement time their capability is live.
//
// So: onboard ONE test account by hand, once, and pin it here. Every run after
// that actually proves the transfer path.
const smokeConnectAccountEnv = "STRIPE_SMOKE_CONNECT_ACCOUNT"

// onboardedSmokeAccount returns a connected account that can actually receive
// a transfer, or skips with instructions for creating one.
//
// A pinned account is REUSED, never deleted — it took a human a few minutes to
// onboard, and a suite that throws it away would have to be re-onboarded on
// every run, which is how a smoke test stops being run.
func onboardedSmokeAccount(t *testing.T, uid string) string {
	t.Helper()

	if pinned := strings.TrimSpace(os.Getenv(smokeConnectAccountEnv)); pinned != "" {
		acct, err := account.GetByID(pinned, &stripe.AccountParams{})
		if err != nil {
			t.Fatalf("%s=%s could not be read: %v", smokeConnectAccountEnv, pinned, err)
		}
		if !transfersActive(acct) {
			t.Skipf("%s=%s has no ACTIVE transfers capability (payouts_enabled=%v, still due: %v). "+
				"Finish its onboarding in the Stripe dashboard, then re-run.",
				smokeConnectAccountEnv, pinned, acct.PayoutsEnabled, requirementsDue(acct))
		}
		mustExec(t, `update public.users set stripe_account_id = $2 where id = $1::uuid`, uid, pinned)
		t.Logf("paying into pinned account %s (payouts_enabled=%v)", pinned, acct.PayoutsEnabled)
		return pinned
	}

	// No pinned account: make one and hand back a real onboarding link.
	//
	// NOT deleted on cleanup, unlike every other account this suite creates.
	// The whole point of this branch is that somebody is about to open that
	// link, and an account torn down when the test process exits would hand
	// them a URL that 404s by the time they read it. The cost is one stray
	// test account per unpinned run, which is exactly the nudge to pin it.
	acctID, err := connectAccountFor(context.Background(), uid, "supporter.smoke@example.test")
	requireAccountsV1(t, err)
	if err != nil {
		t.Fatalf("supporter account: %v", err)
	}
	link, linkErr := accountLinkFor(acctID)
	if linkErr != nil {
		link = "(could not mint an onboarding link: " + linkErr.Error() + ")"
	}

	// AN ACCOUNT LINK IS VALID FOR ABOUT FIVE MINUTES, and that is short enough
	// to be a trap rather than a detail: a URL printed into test output is
	// almost always dead by the time a human reads it, and an expired link does
	// not say so — Stripe redirects it straight to refresh_url, so the person
	// lands back on our site having been shown no form at all and reasonably
	// believes they completed something. That is exactly how this account ended
	// up with details_submitted=false after somebody had "finished" onboarding.
	//
	// So the skip below leads with the expiry and tells the reader how to mint
	// a fresh link on demand, rather than pretending the one above will keep.

	t.Skipf("no %s set, so there is no account that can receive a transfer.\n\n"+
		"A fresh Express account has `transfers` requested but NOT active, and Stripe refuses to "+
		"accept its ToS by API, so one account must be onboarded by hand — once.\n\n"+
		"  THE LINK BELOW EXPIRES IN ABOUT 5 MINUTES and is single-use. An expired one does not"+
		" say so: Stripe bounces it to refresh_url, so you land back on the site having seen no"+
		" form, and nothing is saved. If it has gone stale, mint another with:\n\n"+
		"      go test ./ -run TestPhase3SmokeMintOnboardingLink -v -count=1 \\\n"+
		"        # with %s=%s set\n\n"+
		"  1. Open: %s\n"+
		"  2. Phone 000-000-0000 / code 000000, SSN 000-00-0000, any DOB 18+, any US address;\n"+
		"     bank routing 110000000, account 000123456789.\n"+
		"  3. Confirm it took — details_submitted must be true — then re-run with"+
		" %s=%s\n\n"+
		"That account is reused and never deleted, so this is a one-time cost.",
		smokeConnectAccountEnv, smokeConnectAccountEnv, acctID, link, smokeConnectAccountEnv, acctID)
	return ""
}

// transfersActive moved to payments_connect.go, where the gate now uses it.

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

// TestPhase3SmokeMintOnboardingLink issues a fresh Account Link for the pinned
// account and prints it. Not a test of anything — a tool, living here because
// it needs the same real key and the same account the suite uses.
//
// It exists because an Account Link is valid for about FIVE MINUTES. Any URL
// printed into test output is usually dead by the time a human opens it, and a
// dead one is silent: Stripe redirects it to refresh_url, so the reader lands
// back on the site having been shown no form and reasonably concludes they
// finished. This is the supported way to get a live one.
//
//	STRIPE_SMOKE=1 STRIPE_SMOKE_CONNECT_ACCOUNT=acct_… \
//	  go test ./ -run TestPhase3SmokeMintOnboardingLink -v -count=1
//
// RETURN URL. Minted from APP_BASE_URL like every other link, so a local .env
// pointing at localhost produces a localhost redirect — fine when the web app
// is running, confusing when it is not. Override it for the command if you
// want to land somewhere real:
//
//	APP_BASE_URL=https://mvp.horaapp.co STRIPE_SMOKE=1 … go test …
func TestPhase3SmokeMintOnboardingLink(t *testing.T) {
	requireStripeSmoke(t)

	acctID := strings.TrimSpace(os.Getenv(smokeConnectAccountEnv))
	if acctID == "" {
		t.Skipf("set %s to the account you want an onboarding link for", smokeConnectAccountEnv)
	}

	acct, err := account.GetByID(acctID, &stripe.AccountParams{})
	if err != nil {
		t.Fatalf("read %s: %v", acctID, err)
	}
	if transfersActive(acct) {
		t.Logf("%s is already onboarded (payouts_enabled=%v) — no link needed", acctID, acct.PayoutsEnabled)
		return
	}

	link, err := accountLinkFor(acctID)
	if err != nil {
		t.Fatalf("mint account link: %v", err)
	}

	t.Logf("account          : %s", acctID)
	t.Logf("details_submitted: %v", acct.DetailsSubmitted)
	t.Logf("still due        : %v", requirementsDue(acct))
	t.Logf("returns to       : %s", connectReturnURL())
	t.Logf("")
	t.Logf("OPEN WITHIN ~5 MINUTES, single use:")
	t.Logf("  %s", link)
	t.Logf("")
	t.Logf("Phone 000-000-0000 / code 000000, SSN 000-00-0000, any DOB 18+, any US address;")
	t.Logf("bank routing 110000000, account 000123456789.")
}
