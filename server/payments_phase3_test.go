package main

// Stripe Phase 3: Connect onboarding, the accept gate, and the supporter's
// transfer.
//
// Same four-layer split as Phase 2a/2b, and for the same reason — the layers
// fail for different causes and a reader chasing a failure should be able to
// tell immediately which kind they have:
//
//  1. Arithmetic (no DB, no network). What a settlement owes the supporter and
//     how it splits across the charges funding it. Table-driven, because every
//     case is a small integer relationship a reader should check by eye — and
//     because this is the function that would underpay somebody.
//
//  2. Onboarding (DB, no network). The state machine, and account.updated
//     driving it. Stripe is unconfigured, so what is under test is the cache
//     and the transitions rather than the API — which is the half that decides
//     whether somebody is allowed to work.
//
//  3. The gate (DB, no network). The property the whole phase exists for: with
//     the flag on, a supporter who cannot be paid cannot accept; with it off —
//     the running beta — nobody is stopped from anything.
//
//  4. Payouts and failure (DB, no network). The payout ledger's idempotency,
//     the failure path's ops alert, and the admin retry's refusals. Provoked
//     without a network call, because every guard worth having fires before
//     Stripe is reached.
//
// The fifth layer — a real Express account, a real onboarding, a real transfer
// visible in the test dashboard — is payments_phase3_smoke_test.go, behind
// STRIPE_SMOKE=1.
//
//	docker run -d --rm --name hora-p3-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Phase3 -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// ── 1. What the supporter is owed ──────────────────────────────────────────

// The arithmetic the whole phase turns on, and the constraint that shapes it:
// a transfer tied to a funding charge by source_transaction MAY NOT EXCEED
// THAT CHARGE. So an overrunning task pays out across two transfers, one per
// charge, and a task whose balance charge failed pays out short with the gap
// recorded rather than quietly transferred out of platform float.
//
// Run with the fee OFF so the cases read as the plain integer relationships
// they are; the fee's own arithmetic, and how it lands on these same splits,
// is pinned in platform_fee_test.go.
func TestPhase3SettlementPayoutSplit(t *testing.T) {
	withPlatformFeeBps(t, 0)
	cases := []struct {
		name string
		// What the supporter is owed: time cost + reimbursed receipt.
		total int
		// What the hold actually captured (clamped to the authorization).
		mainCaptured int
		// What a completion_balance charge collected, if any.
		balanceCaptured int

		want []payoutSplit
	}{
		// The ordinary task. The hold covered the settlement, Stripe released
		// the difference by itself, one transfer carries the whole thing.
		{
			"settled inside the hold", 2450, 4950, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 2450}},
		},
		// Exactly at the hold. No remainder, still one transfer.
		{
			"settled exactly at the hold", 4950, 4950, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 4950}},
		},
		// Overran, and the requester's card paid the difference. TWO
		// transfers, because the first cannot exceed the charge funding it.
		{
			"overran, balance collected", 6000, 4950, 1050,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 4950}, {Source: payoutSourceBalance, ServiceCents: 1050}},
		},
		// Overran and the balance charge was DECLINED. The supporter gets what
		// was actually collected; the $10.50 gap is stamped on the row. This
		// is the case the shortfall field exists for, and the one where
		// transferring the full amount anyway would spend money nobody
		// collected.
		{
			"overran, balance failed — short, and flagged", 6000, 4950, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 4950, ShortfallCents: 1050}},
		},
		// The balance charge partially landed. Vanishingly rare, but the
		// arithmetic must not produce a negative or a double-count.
		{
			"overran, balance partly collected", 6000, 4950, 600,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 4950, ShortfallCents: 450}, {Source: payoutSourceBalance, ServiceCents: 600}},
		},
		// Nothing captured: the capture itself failed. No transfer at all —
		// there is no charge to fund one and no money to send.
		{"capture failed entirely", 2450, 0, 0, nil},
		// A task that owes nothing. Cancelled before anyone clocked in.
		{"nothing owed", 0, 4950, 0, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No promo on any of these rows; the subsidy cases are in
			// TestPromoPayoutIsUndiscounted (promo_test.go).
			got := settlementPayouts(supporterPayFor(tc.total, 0), tc.mainCaptured, tc.balanceCaptured, 0)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d split(s) %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("split %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}

			// The property behind every row above: a supporter is never paid
			// more than the settlement owed them, and never more than the
			// money actually collected.
			paid := 0
			for _, s := range got {
				paid += s.AmountCents()
			}
			if paid > tc.total {
				t.Errorf("paid %s on a settlement of %s — the supporter was overpaid",
					formatCentsUSD(paid), formatCentsUSD(tc.total))
			}
			if collected := tc.mainCaptured + tc.balanceCaptured; paid > collected {
				t.Errorf("paid %s out of %s collected — the platform paid money it never took",
					formatCentsUSD(paid), formatCentsUSD(collected))
			}
			// And the shortfall is exactly what was owed and not paid, so the
			// two numbers on the row always reconcile to the settlement.
			short := 0
			for _, s := range got {
				short += s.ShortfallCents
			}
			if len(got) > 0 && paid+short != tc.total {
				t.Errorf("paid %s + shortfall %s != owed %s",
					formatCentsUSD(paid), formatCentsUSD(short), formatCentsUSD(tc.total))
			}
		})
	}
}

// withPlatformFeeBps pins the rate for one test and restores it after.
func withPlatformFeeBps(t *testing.T, bps int) {
	t.Helper()
	prev := Billing.PlatformFeeBps
	Billing.PlatformFeeBps = bps
	t.Cleanup(func() { Billing.PlatformFeeBps = prev })
}

// ── 2. Onboarding ──────────────────────────────────────────────────────────

// The state machine both clients render, driven by the two booleans Stripe
// gives us. Pure, and worth its own test because "in progress" has two
// distinct causes — a half-finished form and a requirement that came due
// later — and collapsing either into "not started" would tell a supporter who
// is nearly done to begin again.
func TestPhase3OnboardingStateMachine(t *testing.T) {
	cases := []struct {
		name                                              string
		payoutsEnabled, transfersActive, detailsSubmitted bool
		hasAccount                                        bool
		due                                               int
		want                                              string
	}{
		{"never opened the flow", false, false, false, false, 0, onboardingNotStarted},
		{"account created, form abandoned", false, false, false, true, 3, onboardingInProgress},
		{"form submitted, Stripe still wants things", false, false, true, true, 2, onboardingInProgress},
		{"payable", true, true, true, true, 0, onboardingComplete},
		// A previously-good account that has gone back to needing something.
		// Still 'in progress' — there is an account and it is not payable,
		// which is what the supporter needs to act on.
		{"was payable, now has requirements due", false, false, true, true, 1, onboardingInProgress},

		// THE GAP. Form in, nothing due, and Stripe has not made the account
		// transferable: 'verifying', never 'complete'. The 2026-09-22 shape
		// exactly — payouts_enabled=true, transfers not active, nothing due —
		// which used to render as "Payouts are set up" and pass the gate.
		{"form in, nothing due, transfers not yet active", false, false, true, true, 0, onboardingVerifying},
		{"payouts_enabled but transfers not active", true, false, true, true, 0, onboardingVerifying},
		// The other half-state — transfers active, payouts not — is equally
		// not complete: the platform could reach the account but the account
		// could not reach a bank.
		{"transfers active but payouts not enabled", false, true, true, true, 0, onboardingVerifying},
		// Both true is the ONLY complete, whatever else is pending.
		{"both true with eventually_due items", true, true, true, true, 0, onboardingComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stateFor(tc.payoutsEnabled, tc.transfersActive, tc.detailsSubmitted, tc.hasAccount, tc.due); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

// requirementsDue merges currently_due and past_due. Reading only
// currently_due would show NOTHING to a supporter whose account has blown its
// deadline — the exact moment they most need telling.
func TestPhase3RequirementsIncludePastDue(t *testing.T) {
	acct := acctWith([]string{"individual.id_number"}, []string{"individual.verification.document"})
	got := requirementsDue(acct)
	if len(got) != 2 {
		t.Fatalf("requirements = %v, want both currently_due and past_due", got)
	}

	// Deduplicated: Stripe lists a field in both once it is overdue, and
	// showing "2 things needed" for one thing is a lie the supporter can count.
	dup := acctWith([]string{"individual.id_number"}, []string{"individual.id_number"})
	if got := requirementsDue(dup); len(got) != 1 {
		t.Errorf("one overdue field reported as %d: %v", len(got), got)
	}

	// No requirements block at all — an account Stripe has not evaluated yet.
	// Must be an empty slice and not nil, so the JSON is `[]` rather than
	// `null` and a client can call .length on it.
	if got := requirementsDue(acctWith(nil, nil)); got == nil || len(got) != 0 {
		t.Errorf("empty requirements = %v, want an empty slice", got)
	}
}

// account.updated is what keeps the accept gate honest without an API call per
// accept. This is the whole transition table, driven through the real webhook
// handler against real rows.
func TestPhase3AccountUpdatedDrivesTheCache(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	const acctID = "acct_phase3_cache"
	linkConnectAccount(t, w.supporterID, acctID)

	// Not payable yet, and Stripe wants two things.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, acctID, false, true, []string{"individual.id_number", "external_account"})); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	st := cachedStatus(t, w.supporterID)
	if st.State != onboardingInProgress {
		t.Errorf("state = %q, want in_progress", st.State)
	}
	if st.PayoutsEnabled {
		t.Error("cached as payable while Stripe says payouts are off")
	}
	if len(st.RequirementsDue) != 2 {
		t.Errorf("requirements = %v, want 2", st.RequirementsDue)
	}

	// Onboarding completes.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, acctID, true, true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	st = cachedStatus(t, w.supporterID)
	if st.State != onboardingComplete || !st.PayoutsEnabled {
		t.Errorf("after completion: state=%q payouts=%v, want complete/true", st.State, st.PayoutsEnabled)
	}
	if len(st.RequirementsDue) != 0 {
		t.Errorf("requirements = %v, want none", st.RequirementsDue)
	}

	// And back off again — a document expired, a deadline passed. This is the
	// transition that costs somebody money, so it is audited.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, acctID, false, true, []string{"individual.verification.document"})); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	if st = cachedStatus(t, w.supporterID); st.PayoutsEnabled {
		t.Error("still cached as payable after Stripe disabled payouts")
	}
	if n := auditCount(t, "PAYOUTS_DISABLED"); n != 1 {
		t.Errorf("PAYOUTS_DISABLED audit rows = %d, want exactly 1", n)
	}

	// Going from not-payable to not-payable is not a transition and must not
	// audit again — an ops feed that cries wolf on every requirement refresh
	// is one nobody reads.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, acctID, false, true, []string{"individual.verification.document"})); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	if n := auditCount(t, "PAYOUTS_DISABLED"); n != 1 {
		t.Errorf("PAYOUTS_DISABLED audited %d times for one transition", n)
	}
}

// An account we have no user for — a dashboard test event, an account made
// outside this backend. Acknowledged, never retried: a 5xx here would make
// Stripe redeliver for days and eventually disable the endpoint.
func TestPhase3AccountUpdatedForAnUnknownAccountIsIgnored(t *testing.T) {
	setupStripeWebhookDB(t)
	seedOpsWorld(t, "open")
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, "acct_nobody_here", true, true, nil)); err != nil {
		t.Fatalf("unknown account should be ignored, got: %v", err)
	}
}

// ── 3. The gate ────────────────────────────────────────────────────────────

// The running beta. PAYMENTS_ENFORCED off means the payouts surface is
// entirely optional: nobody has a connected account, nobody is asked for one,
// and accepting behaves exactly as it did before this phase existed.
func TestPhase3AcceptIsUngatedWhileTheFlagIsOff(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)

	code, body := acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("accept with the flag off: %d (%v)", code, body)
	}
	if got := assignedTo(t, w.taskID); got != w.supporterID {
		t.Errorf("task assigned to %q, want the supporter", got)
	}
}

// With the flag on, a supporter who cannot be paid cannot accept — and the
// refusal is the specific code the clients turn into the onboarding CTA, not a
// generic 403 they would render as "something went wrong".
func TestPhase3AcceptIsGatedOnPayoutsWhenEnforced(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)

	code, body := acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusForbidden {
		t.Fatalf("accept before onboarding: %d (%v), want 403", code, body)
	}
	if body["error"] != "payouts_onboarding_required" {
		t.Errorf("error = %v, want payouts_onboarding_required", body["error"])
	}
	if msg, _ := body["message"].(string); !strings.Contains(strings.ToLower(msg), "payout") {
		t.Errorf("message does not mention payouts: %q", msg)
	}

	// THE PROPERTY THAT MATTERS MOST HERE: the refusal happened BEFORE the
	// claiming UPDATE. A gate that fired after would have taken the task off
	// the board and handed it to somebody it then turned away.
	if got := assignedTo(t, w.taskID); got != "" {
		t.Fatalf("a refused accept still claimed the task for %q", got)
	}
	if got := taskStatusOf(t, w.taskID); got != "open" {
		t.Errorf("task is %q after a refused accept, want open", got)
	}

	// Once Stripe says they are payable, the same accept goes through.
	linkConnectAccount(t, w.supporterID, "acct_phase3_gate")
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, "acct_phase3_gate", true, true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	code, body = acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("accept after onboarding: %d (%v)", code, body)
	}
	if got := assignedTo(t, w.taskID); got != w.supporterID {
		t.Errorf("task assigned to %q, want the supporter", got)
	}
}

// An approved-but-not-onboarded supporter can still BROWSE. The gate is on
// accepting, not on looking — a supporter who cannot see what is available has
// no reason to onboard in the first place.
func TestPhase3UnonboardedSupporterCanStillBrowse(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)

	code, listW := callTaskHandler(t, listAvailableTasks, http.MethodGet, "/tasks/available",
		"", w.supporterID, oldSupporterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("browsing while un-onboarded: %d (%s)", code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), w.taskID) {
		t.Errorf("the open task is missing from an un-onboarded supporter's feed: %s", listW.Body.String())
	}
}

// Failing closed. A database error on the readiness check refuses the accept:
// between "try again in a moment" and letting somebody work a task we may have
// no way to pay them for, the first is the kinder failure.
func TestPhase3AcceptFailsClosedWhenReadinessIsUnreadable(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	// A uid that is not a user at all: the readiness query finds no row.
	ready, err := supporterPayoutsReady(context.Background(), "00000000-0000-0000-0000-0000000000ff")
	if err == nil {
		t.Fatal("an unreadable readiness check reported success")
	}
	if ready {
		t.Error("failed OPEN: an unreadable readiness check said the supporter was payable")
	}

	// And with the flag off the same call is a no-op that never reads anything
	// — which is what makes the beta unaffected by any of this.
	t.Setenv("PAYMENTS_ENFORCED", "")
	if ready, err := supporterPayoutsReady(context.Background(), "00000000-0000-0000-0000-0000000000ff"); err != nil || !ready {
		t.Errorf("with the flag off: ready=%v err=%v, want true/nil", ready, err)
	}
}

// ── 4. The payout ledger ───────────────────────────────────────────────────

// The idempotency claim, which is a UNIQUE index and not a convention. This is
// what makes a retried settle, a redelivered webhook and a double-tapped admin
// retry all incapable of paying a supporter twice.
func TestPhase3OnePayoutPerPaymentEver(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_once")

	in := payoutInput{TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450}
	first, err := insertPayout(context.Background(), in, 2450)
	if err != nil {
		t.Fatalf("first payout: %v", err)
	}
	if first.Status != payoutStatusPending {
		t.Errorf("a fresh payout is %q, want pending", first.Status)
	}

	// The second attempt loses on the index, atomically, before any Stripe
	// call could be made.
	if _, err := insertPayout(context.Background(), in, 2450); err != errPayoutExists {
		t.Fatalf("second payout for the same payment: %v, want errPayoutExists", err)
	}
	if n := payoutCount(t, w.taskID); n != 1 {
		t.Fatalf("%d payout rows for one payment — the supporter would be paid twice", n)
	}

	// And the guard is not merely "the row exists": it survives the row being
	// marked failed, which is the state an admin retry operates on.
	mustExec(t, `update public.payouts set status = $2 where id = $1::uuid`, first.ID, payoutStatusFailed)
	if _, err := insertPayout(context.Background(), in, 2450); err != errPayoutExists {
		t.Errorf("a failed payout did not block a duplicate: %v", err)
	}
}

// The shortfall lands on the row, in the place an ops person will look. A gap
// that exists only as the difference between two tables is a gap nobody finds.
func TestPhase3ShortfallIsRecordedOnThePayout(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 4950, "ch_phase3_short")

	p, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID,
		AmountCents: 4950, ServiceCents: 4950, ShortfallCents: 1050,
	}, 4950)
	if err != nil {
		t.Fatalf("payout: %v", err)
	}

	meta := payoutMeta(t, p.ID)
	if got := num(meta["shortfall_cents"]); got != 1050 {
		t.Errorf("shortfall_cents = %v, want 1050", meta["shortfall_cents"])
	}
	if meta["shortfall_reason"] != "completion_balance_uncollected" {
		t.Errorf("shortfall_reason = %v", meta["shortfall_reason"])
	}

	// A healthy payout carries no shortfall keys at all, so their presence is
	// itself the signal.
	other := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_clean")
	clean, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: other, AmountCents: 2450, ServiceCents: 2450,
	}, 2450)
	if err != nil {
		t.Fatalf("clean payout: %v", err)
	}
	if m := payoutMeta(t, clean.ID); len(m) != 0 {
		t.Errorf("a healthy payout carries meta %v, want none", m)
	}
}

// A settlement whose supporter never onboarded. Normal for every task in the
// running beta, so it must not be an error — but somebody is still owed money,
// so it must not be silent either.
func TestPhase3SupporterWithNoAccountIsReportedNotPaid(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_noacct")

	withStripeConfigured(t)

	got := payoutForTask(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
	})
	if got != nil {
		t.Errorf("payout = %+v, want nil for a supporter with no account", got)
	}
	if n := payoutCount(t, w.taskID); n != 0 {
		t.Errorf("%d payout rows written for a transfer that could not happen", n)
	}
	if n := auditCount(t, "PAYOUT_SKIPPED_NO_ACCOUNT"); n != 1 {
		t.Errorf("PAYOUT_SKIPPED_NO_ACCOUNT rows = %d, want 1 — somebody is owed and nobody was told", n)
	}
}

// A transfer Stripe refused. The row goes to failed, the reason is kept, and
// ops get an audit row — the money is on the platform balance and a human has
// to move it.
func TestPhase3FailedTransferIsRecordedAndAudited(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_fail")
	p, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
	}, 2450)
	if err != nil {
		t.Fatalf("payout: %v", err)
	}

	markPayoutFailed(context.Background(), p, payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
	}, fmt.Errorf("account restricted"))

	if got := payoutStatusOf(t, p.ID); got != payoutStatusFailed {
		t.Errorf("payout is %q, want failed", got)
	}
	if m := payoutMeta(t, p.ID); !strings.Contains(fmt.Sprint(m["error"]), "account restricted") {
		t.Errorf("the failure reason was not kept: %v", m)
	}
	if n := auditCount(t, "PAYOUT_FAILED"); n != 1 {
		t.Errorf("PAYOUT_FAILED audit rows = %d, want 1", n)
	}
}

// The admin retry's two refusals, both of which fire before any Stripe call.
// This is the one admin action that moves money, so what it declines to do
// matters more than what it does.
func TestPhase3AdminRetryRefusesWhatItMustNotResend(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")

	t.Run("a payout that already has a transfer", func(t *testing.T) {
		paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_retry_done")
		p, err := insertPayout(context.Background(), payoutInput{
			TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
		}, 2450)
		if err != nil {
			t.Fatalf("payout: %v", err)
		}
		// Money moved, and then something marked the row failed — a reversal,
		// say. "Status is failed" would happily pay it again; "has a transfer
		// id" cannot be true of a row where nothing moved.
		mustExec(t, `update public.payouts set stripe_transfer_id = $2, status = $3 where id = $1::uuid`,
			p.ID, "tr_already_sent", payoutStatusFailed)

		code, body := callAdminRetry(t, p.ID, w.adminID)
		if code != http.StatusConflict {
			t.Fatalf("retry of an already-transferred payout: %d (%v), want 409", code, body)
		}
		if body["error"] != "already_transferred" {
			t.Errorf("error = %v, want already_transferred", body["error"])
		}
	})

	t.Run("a supporter with nowhere to send it", func(t *testing.T) {
		paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_retry_noacct")
		p, err := insertPayout(context.Background(), payoutInput{
			TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
		}, 2450)
		if err != nil {
			t.Fatalf("payout: %v", err)
		}
		code, body := callAdminRetry(t, p.ID, w.adminID)
		if code != http.StatusConflict {
			t.Fatalf("retry for a supporter with no account: %d (%v), want 409", code, body)
		}
		if body["error"] != "no_connect_account" {
			t.Errorf("error = %v, want no_connect_account", body["error"])
		}
	})

	t.Run("a payout that does not exist", func(t *testing.T) {
		code, _ := callAdminRetry(t, "00000000-0000-0000-0000-0000000000aa", w.adminID)
		if code != http.StatusNotFound {
			t.Errorf("retry of a missing payout: %d, want 404", code)
		}
	})

	t.Run("anybody who is not ops", func(t *testing.T) {
		paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_retry_authz")
		p, err := insertPayout(context.Background(), payoutInput{
			TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
		}, 2450)
		if err != nil {
			t.Fatalf("payout: %v", err)
		}
		// The real middleware, with a supporter's session on it.
		code, _ := callAdminRetryAs(t, p.ID, w.supporterID, oldSupporterEmail)
		if code != http.StatusForbidden {
			t.Errorf("a supporter could reach the payout retry: %d, want 403", code)
		}
	})
}

// The retry's idempotency key has to CHANGE between attempts, because Stripe
// caches a failed call's error against its key for 24 hours — reusing it would
// replay the first failure forever, and the operator would see a retry button
// that does nothing.
//
// Pure, so it is checked here rather than by watching a real transfer: what
// matters is the PROPERTY (distinct per attempt, stable within one), and a
// live call could satisfy it by accident.
func TestPhase3TransferKeyChangesPerAttempt(t *testing.T) {
	const payoutID = "3f1b0c4a-0000-4000-8000-000000000001"

	first := transferIdempotencyKey(payoutID, 1)
	second := transferIdempotencyKey(payoutID, 2)
	if first == second {
		t.Fatalf("attempt 1 and 2 share the key %q — a retry would replay the cached failure", first)
	}
	// Stable within an attempt: this is the half that makes a retried SETTLE
	// idempotent, and losing it would double-pay.
	if again := transferIdempotencyKey(payoutID, 1); again != first {
		t.Errorf("the same attempt produced two keys: %q then %q", first, again)
	}
	// Different payouts never collide, whatever their attempt numbers.
	if other := transferIdempotencyKey("3f1b0c4a-0000-4000-8000-000000000002", 1); other == first {
		t.Errorf("two payouts share the key %q", first)
	}
	if !strings.Contains(first, payoutID) {
		t.Errorf("key %q does not name its payout — untraceable in Stripe's logs", first)
	}
}

// A fresh payout starts at attempt 1, and the retry refuses to advance it when
// Stripe is not configured. Burning an attempt on a call that never had a
// chance would spend the one thing that makes the NEXT retry a real retry.
func TestPhase3RetryFailsClosedWithoutStripe(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_attempts")
	p, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
	}, 2450)
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	if p.AttemptCount != 1 {
		t.Fatalf("a fresh payout is at attempt %d, want 1", p.AttemptCount)
	}

	// A supporter Stripe HAS made payable — otherwise the retry refuses on
	// the account before it ever asks whether Stripe is configured, which is
	// the right order (our own data first) but not what this test is about.
	linkConnectAccount(t, w.supporterID, "acct_phase3_attempts")
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEvent(t, "acct_phase3_attempts", true, true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	code, body := callAdminRetry(t, p.ID, w.adminID)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("retry with Stripe unconfigured: %d (%v), want 503", code, body)
	}
	if got := payoutAttempts(t, p.ID); got != 1 {
		t.Errorf("attempt_count = %d after a refused retry, want 1", got)
	}
}

// ── 5. What each party is told ─────────────────────────────────────────────

// The supporter's half of a settled task: what they earned, split into the
// money they made and the money they are getting back.
func TestPhase3SupporterSeesTheirOwnCut(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 45, false)
	mustExec(t, `update public.tasks
	                set status='completed', completed_at=now(),
	                    shopping_budget_approved_cents=3000, receipt_amount_cents=1240
	              where id=$1::uuid`, w.taskID)

	body := getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	settlement, ok := body["settlement"].(map[string]any)
	if !ok {
		t.Fatalf("no settlement block: %v", body)
	}
	earned, ok := settlement["earned"].(map[string]any)
	if !ok {
		t.Fatalf("the supporter cannot see what they earned: %v", settlement)
	}

	// base $12.00 + 30 billable x $0.50 = $27.00 of service, less the 20%
	// platform fee = $21.60; $12.40 reimbursed in full (D-14).
	if got := num(earned["time_cents"]); got != 2160 {
		t.Errorf("time = %s, want $21.60 after the fee", formatCentsUSD(got))
	}
	if got := num(earned["service_gross_cents"]); got != 2700 {
		t.Errorf("service gross = %s, want $27.00", formatCentsUSD(got))
	}
	if got := num(earned["platform_fee_cents"]); got != 540 {
		t.Errorf("fee = %s, want $5.40", formatCentsUSD(got))
	}
	if got := num(earned["reimbursement_cents"]); got != 1240 {
		t.Errorf("reimbursement = %s, want $12.40", formatCentsUSD(got))
	}
	if got := num(earned["total_cents"]); got != 3400 {
		t.Errorf("total = %s, want $34.00", formatCentsUSD(got))
	}

	// The two halves are separate numbers on purpose: a supporter who sees one
	// total for a shopping task cannot tell what they MADE from what they are
	// being handed back.
	if num(earned["time_cents"])+num(earned["reimbursement_cents"]) != num(earned["total_cents"]) {
		t.Error("the split does not reconcile with the total the client renders")
	}
}

// The mirror of the Phase 2b hold test, and the property that matters most in
// this phase: the two views of one task's money are DISJOINT. The supporter's
// cut never reaches the requester, and the requester's hold and card never
// reach the supporter.
func TestPhase3EarningsAndHoldNeverCrossOver(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 45, false)
	paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)
	seedDisplayCard(t, paymentID, "visa", "4242")
	mustExec(t, `update public.tasks set status='completed', completed_at=now() where id=$1::uuid`, w.taskID)

	// The requester must not be shown the supporter's pay packet. Absent, not
	// zeroed — `earned: {}` would read as a supporter who made nothing.
	requesterView := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)
	rs, _ := requesterView["settlement"].(map[string]any)
	if _, present := rs["earned"]; present {
		t.Errorf("the requester can see the supporter's earnings: %v", rs["earned"])
	}

	// And the supporter must not be shown the hold or the card, on the task
	// payload or on the worklogs payload.
	code, supporterTask := getTaskAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("getTask as supporter: %d", code)
	}
	if _, present := supporterTask["payment"]; present {
		t.Fatalf("the supporter can see the requester's hold: %v", supporterTask["payment"])
	}

	supporterLogs := getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	for _, payload := range []map[string]any{supporterTask, supporterLogs} {
		raw, _ := json.Marshal(payload)
		// The hold, the card, and the field names that would carry either.
		for _, secret := range []string{
			"7675", "4242", "card_last4", "card_brand",
			"authorized_cents", "released_cents", "stripe_payment_intent_id",
		} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("supporter payload contains %q: %s", secret, raw)
			}
		}
	}

	// Symmetrically, the requester's payload must not carry the supporter's
	// connected account or transfer.
	linkConnectAccount(t, w.supporterID, "acct_phase3_leak")
	raw, _ := json.Marshal(requesterView)
	for _, secret := range []string{"acct_phase3_leak", "stripe_account_id", "payout_status"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("requester payload contains %q: %s", secret, raw)
		}
	}
}

// The Earnings payload is supporter-scoped: it carries what arrived, and
// nothing about where it came from.
func TestPhase3EarningsCarryNoRequesterData(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 2450, "ch_phase3_earnings")
	mustExec(t, `update public.payments
	                set time_cost_cents = 2450, shopping_receipt_cents = 0,
	                    card_brand = 'visa', card_last4 = '4242'
	              where id = $1::uuid`, paymentID)
	p, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 2450, ServiceCents: 2450,
	}, 2450)
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	mustExec(t, `update public.payouts set status=$2, stripe_transfer_id=$3 where id=$1::uuid`,
		p.ID, payoutStatusPaid, "tr_phase3_earnings")

	if got := lifetimePaidCents(context.Background(), w.supporterID); got != 2450 {
		t.Errorf("lifetime = %s, want $24.50", formatCentsUSD(got))
	}

	// A pending or failed transfer is money in flight, and must not be counted
	// as earned — a number a supporter can reconcile against their bank
	// statement is worth more than one that cannot be.
	other := seedCapturedPayment(t, w.taskID, w.requesterID, 1000, "ch_phase3_pending")
	if _, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: other, AmountCents: 1000, ServiceCents: 1000,
	}, 1000); err != nil {
		t.Fatalf("pending payout: %v", err)
	}
	if got := lifetimePaidCents(context.Background(), w.supporterID); got != 2450 {
		t.Errorf("lifetime = %s with a pending transfer, want still $24.50", formatCentsUSD(got))
	}
}

// ── 6. The database invariants ─────────────────────────────────────────────

// CLAUDE.md Rule 3, applied to the table this phase adds. payouts is the
// second money table in the product and the one a client-direct INSERT would
// be most profitable against — a row here is an instruction to move money to a
// bank account.
//
// Run against the migration itself, as the fixture applies it, so this is a
// test of the SHIPPED DDL rather than of a hand-copy of it.
func TestPhase3PayoutsTableIsDenyAll(t *testing.T) {
	setupStripeWebhookDB(t)
	ctx := context.Background()

	// 1. No policies on payouts. A policy here would open a client-direct path.
	var policies int
	if err := db.QueryRow(ctx, `
		select count(*) from pg_policies where schemaname='public' and tablename='payouts'
	`).Scan(&policies); err != nil {
		t.Fatalf("read policies: %v", err)
	}
	if policies != 0 {
		t.Errorf("payouts carries %d RLS policies, want zero (S-10)", policies)
	}

	// 2. No DML grants to any client role. This is the check that the two
	// months of exploitable permissive policies were missed by — RLS being ON
	// says nothing about what is permitted, and this schema's ALTER DEFAULT
	// PRIVILEGES hands anon and authenticated full DML on every new table
	// unless the migration revokes it.
	rows, err := db.Query(ctx, `
		select grantee, privilege_type from information_schema.role_table_grants
		 where table_schema='public' and table_name='payouts'
		   and grantee in ('anon','authenticated','PUBLIC')
		   and privilege_type in ('SELECT','INSERT','UPDATE','DELETE')
	`)
	if err != nil {
		t.Fatalf("read grants: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var grantee, priv string
		_ = rows.Scan(&grantee, &priv)
		t.Errorf("payouts grants %s to %s — a client-direct path to the payout ledger", priv, grantee)
	}

	// 3. RLS is on.
	var rls bool
	if err := db.QueryRow(ctx, `
		select c.relrowsecurity from pg_class c
		  join pg_namespace n on n.oid = c.relnamespace
		 where n.nspname='public' and c.relname='payouts'
	`).Scan(&rls); err != nil {
		t.Fatalf("read rls: %v", err)
	}
	if !rls {
		t.Error("payouts has RLS disabled")
	}

	// 4. The unique index that makes double-paying impossible actually exists.
	// Every idempotency claim in this phase rests on it, so its absence would
	// be silent until the day somebody was paid twice.
	var hasIndex bool
	if err := db.QueryRow(ctx, `
		select exists (
		  select 1 from pg_indexes
		   where schemaname='public' and tablename='payouts' and indexname='uq_payouts_payment_id'
		)
	`).Scan(&hasIndex); err != nil {
		t.Fatalf("read indexes: %v", err)
	}
	if !hasIndex {
		t.Error("uq_payouts_payment_id is missing — nothing stops two payouts for one payment")
	}
}

// ── Fixtures ───────────────────────────────────────────────────────────────

// acctWith builds an Account carrying just the requirements fields, which is
// all requirementsDue reads.
func acctWith(currentlyDue, pastDue []string) *stripe.Account {
	return &stripe.Account{
		Requirements: &stripe.AccountRequirements{
			CurrentlyDue: currentlyDue,
			PastDue:      pastDue,
		},
	}
}

// accountUpdatedEvent is a signed-shape account.updated, as Stripe delivers it
// for a connected account: the account in data.object, the connected account id
// in the event's top-level `account`.
//
// capabilities.transfers follows payouts_enabled here, which is how Stripe's
// v1 Account behaves in practice — the two flip together. The tests for the
// case where they DON'T use accountUpdatedEventWithTransfers.
func accountUpdatedEvent(t *testing.T, accountID string, payoutsEnabled, detailsSubmitted bool, due []string) *stripe.Event {
	t.Helper()
	transfers := "inactive"
	if payoutsEnabled {
		transfers = "active"
	}
	return accountUpdatedEventWithTransfers(t, accountID, payoutsEnabled, transfers, detailsSubmitted, due)
}

// accountUpdatedEventWithTransfers is accountUpdatedEvent with the transfers
// capability status spelled out: "active", "pending", "inactive".
func accountUpdatedEventWithTransfers(t *testing.T, accountID string, payoutsEnabled bool, transfers string, detailsSubmitted bool, due []string) *stripe.Event {
	t.Helper()
	if due == nil {
		due = []string{}
	}
	body, err := json.Marshal(map[string]any{
		"id":                accountID,
		"object":            "account",
		"payouts_enabled":   payoutsEnabled,
		"charges_enabled":   payoutsEnabled,
		"details_submitted": detailsSubmitted,
		"capabilities":      map[string]any{"transfers": transfers},
		"requirements": map[string]any{
			"currently_due": due,
			"past_due":      []string{},
		},
	})
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	return &stripe.Event{
		ID:      "evt_" + accountID,
		Type:    "account.updated",
		Account: accountID,
		Data:    &stripe.EventData{Raw: body},
	}
}

// callAdminRetry runs the real route chain — requireOpsAdmin, then the handler
// — with an ops admin's session on it.
func callAdminRetry(t *testing.T, payoutID, actorUID string) (int, map[string]any) {
	t.Helper()
	return callAdminRetryAs(t, payoutID, actorUID, adminEmail)
}

func callAdminRetryAs(t *testing.T, payoutID, uid, email string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/payouts/"+payoutID+"/retry",
		strings.NewReader("{}"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: payoutID}}
	c.Set("uid", uid)
	c.Set("email", email)

	requireOpsAdmin()(c)
	if !c.IsAborted() {
		adminRetryPayout(c)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func linkConnectAccount(t *testing.T, uid, accountID string) {
	t.Helper()
	mustExec(t, `update public.users set stripe_account_id = $2 where id = $1::uuid`, uid, accountID)
}

func cachedStatus(t *testing.T, uid string) ConnectStatus {
	t.Helper()
	st, err := cachedConnectStatus(context.Background(), uid)
	if err != nil {
		t.Fatalf("cached status: %v", err)
	}
	return st
}

// seedCapturedPayment is a payment whose money has already moved — the state a
// transfer is funded from.
func seedCapturedPayment(t *testing.T, taskID, requesterID string, cents int, chargeID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, stripe_charge_id,
		                             authorized_cents, captured_cents)
		values ($1::uuid, $2::uuid, $3, 'captured', $4, $5, $6, $6)
		returning id::text
	`, taskID, requesterID, paymentKindTaskPayment, "pi_"+chargeID, chargeID, cents).Scan(&id); err != nil {
		t.Fatalf("seed captured payment: %v", err)
	}
	return id
}

func payoutCount(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.payouts where task_id = $1::uuid`, taskID).Scan(&n); err != nil {
		t.Fatalf("count payouts: %v", err)
	}
	return n
}

func payoutStatusOf(t *testing.T, payoutID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(context.Background(),
		`select status from public.payouts where id = $1::uuid`, payoutID).Scan(&s); err != nil {
		t.Fatalf("read payout: %v", err)
	}
	return s
}

func payoutAttempts(t *testing.T, payoutID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select attempt_count from public.payouts where id = $1::uuid`, payoutID).Scan(&n); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	return n
}

func payoutMeta(t *testing.T, payoutID string) map[string]any {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(context.Background(),
		`select meta from public.payouts where id = $1::uuid`, payoutID).Scan(&raw); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.audit_logs where action = $1`, action).Scan(&n); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	return n
}

func assignedTo(t *testing.T, taskID string) string {
	t.Helper()
	var uid *string
	if err := db.QueryRow(context.Background(),
		`select assigned_to_id from public.tasks where id = $1::uuid`, taskID).Scan(&uid); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if uid == nil {
		return ""
	}
	return *uid
}

func taskStatusOf(t *testing.T, taskID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(context.Background(),
		`select status from public.tasks where id = $1::uuid`, taskID).Scan(&s); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func unassign(t *testing.T, taskID string) {
	t.Helper()
	mustExec(t, `update public.tasks set assigned_to_id = null, assigned_to = '' where id = $1::uuid`, taskID)
}

func mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// withStripeConfigured makes paymentsEnabled() answer true for one test.
//
// stripe.Key is set directly rather than through the environment because
// initStripe reads STRIPE_SECRET_KEY under a sync.Once — the first call in the
// test binary wins for the whole process, so t.Setenv cannot turn it on later.
// Only for tests of branches that run BEFORE any API call; anything that
// actually talks to Stripe belongs in the smoke file with a real key.
func withStripeConfigured(t *testing.T) {
	t.Helper()
	prev := stripe.Key
	stripe.Key = "sk_test_notarealkey"
	t.Cleanup(func() { stripe.Key = prev })
}

func acceptAs(t *testing.T, taskID, uid, email string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, acceptTask, http.MethodPost, "/tasks/"+taskID+"/accept",
		taskID, uid, email, "", nil)
	return code, decodeFirstJSON(t, w)
}

// ── 6. The transfers capability, separately from payouts_enabled ───────────

// What 2026-09-22 taught: payouts_enabled=true, details_submitted=true and an
// empty currently_due are not enough. The Transfer is checked against the
// transfers CAPABILITY, and the gate has to be too — a supporter whose account
// Stripe is still making transferable is refused with a message that says to
// wait, not one that says to set up payouts they have already set up.
func TestPhase3GateRefusesUntilTransfersCapabilityIsActive(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)
	const acctID = "acct_phase3_transfers"
	linkConnectAccount(t, w.supporterID, acctID)

	// Stripe's snapshot: payable by every measure the old gate read, and the
	// transfers capability not yet active.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "pending", true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	st := cachedStatus(t, w.supporterID)
	if !st.PayoutsEnabled || st.TransfersActive {
		t.Fatalf("cache = payouts:%v transfers:%v, want payouts on and transfers off", st.PayoutsEnabled, st.TransfersActive)
	}
	if st.State != onboardingVerifying {
		t.Errorf("state = %q, want verifying — 'complete' is what admitted the unpayable supporter", st.State)
	}

	code, body := acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusForbidden {
		t.Fatalf("accept with transfers pending: %d (%v), want 403", code, body)
	}
	if body["error"] != "payouts_onboarding_required" {
		t.Errorf("error = %v, want payouts_onboarding_required", body["error"])
	}
	// The copy is the wait, not the instruction: this supporter has nothing
	// left to set up.
	if msg, _ := body["message"].(string); !strings.Contains(msg, "Verification in progress") {
		t.Errorf("message = %q, want the verification-in-progress copy", msg)
	}
	if got := assignedTo(t, w.taskID); got != "" {
		t.Fatalf("a refused accept still claimed the task for %q", got)
	}

	// The webhook flips it — the capability activates — and the same accept
	// goes through.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "active", true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	if st = cachedStatus(t, w.supporterID); !st.TransfersActive || st.State != onboardingComplete {
		t.Fatalf("after activation: transfers=%v state=%q, want true/complete", st.TransfersActive, st.State)
	}
	code, body = acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("accept after the capability activated: %d (%v)", code, body)
	}
	if got := assignedTo(t, w.taskID); got != w.supporterID {
		t.Errorf("task assigned to %q, want the supporter", got)
	}
}

// A supporter who has not onboarded at all still gets the instruction, not
// the wait — the two refusals are different sentences for different people.
func TestPhase3GateCopyDistinguishesNotStartedFromVerifying(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)

	_, body := acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if msg, _ := body["message"].(string); !strings.Contains(msg, "Set up payouts") {
		t.Errorf("message for a supporter with no account = %q, want the set-up prompt", msg)
	}
}

// Losing the transfers capability while payouts_enabled stays true is the
// same transition as losing payouts_enabled: the supporter can no longer be
// paid, and is told so once.
func TestPhase3LosingTransfersCapabilityIsAudited(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	const acctID = "acct_phase3_transfers_lost"
	linkConnectAccount(t, w.supporterID, acctID)

	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "active", true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "inactive", true, []string{"individual.verification.document"})); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	if st := cachedStatus(t, w.supporterID); st.TransfersActive || st.State == onboardingComplete {
		t.Errorf("still complete after the capability was lost: %+v", st)
	}
	if n := auditCount(t, "PAYOUTS_DISABLED"); n != 1 {
		t.Errorf("PAYOUTS_DISABLED audit rows = %d, want exactly 1", n)
	}
	// And a supporter who was never ready does not get a "disabled" audit
	// when the capability moves between two not-ready states.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "pending", true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	if n := auditCount(t, "PAYOUTS_DISABLED"); n != 1 {
		t.Errorf("PAYOUTS_DISABLED audited %d times for one transition", n)
	}
}

// The admin retry refuses, without burning an attempt, while the account is
// not transferable — and gets past that guard the moment the webhook says it
// is. The transfer itself needs a real Stripe (the smoke file, and the live
// retry that closed the 2026-09-22 payout); what is under test here is that
// the retry no longer reaches Stripe with an account Stripe will refuse.
func TestPhase3AdminRetryWaitsForTransfersCapability(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 1200, "ch_phase3_retry_caps")
	p, err := insertPayout(context.Background(), payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 1200, ServiceCents: 1200,
	}, 1200)
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	markPayoutFailed(context.Background(), p, payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID, AmountCents: 1200, ServiceCents: 1200,
	}, fmt.Errorf("insufficient_capabilities_for_transfer"))

	const acctID = "acct_phase3_retry_caps"
	linkConnectAccount(t, w.supporterID, acctID)
	// The 2026-09-22 shape: payouts_enabled, nothing due, transfers not active.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "pending", true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}

	code, body := callAdminRetry(t, p.ID, w.adminID)
	if code != http.StatusConflict {
		t.Fatalf("retry into a non-transferable account: %d (%v), want 409", code, body)
	}
	if body["error"] != "supporter_not_payable" {
		t.Errorf("error = %v, want supporter_not_payable", body["error"])
	}
	if body["onboarding_state"] != onboardingVerifying {
		t.Errorf("onboarding_state = %v, want verifying", body["onboarding_state"])
	}
	if got := payoutAttempts(t, p.ID); got != 1 {
		t.Errorf("attempt_count = %d after a refused retry, want 1 — the refusal must not burn an attempt", got)
	}
	if got := payoutStatusOf(t, p.ID); got != payoutStatusFailed {
		t.Errorf("payout is %q after a refused retry, want still failed", got)
	}

	// The capability activates. The retry now gets past the readiness guard
	// and on to the next one — Stripe is unconfigured in this suite, so that
	// is the 503, which is exactly the proof: it was the account, and now it
	// is not.
	if err := onAccountUpdated(context.Background(),
		accountUpdatedEventWithTransfers(t, acctID, true, "active", true, nil)); err != nil {
		t.Fatalf("account.updated: %v", err)
	}
	code, body = callAdminRetry(t, p.ID, w.adminID)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("retry after activation with Stripe unconfigured: %d (%v), want 503 (past the readiness guard)", code, body)
	}
	if got := payoutAttempts(t, p.ID); got != 1 {
		t.Errorf("attempt_count = %d, want 1 — an unconfigured Stripe must not burn one either", got)
	}
}
