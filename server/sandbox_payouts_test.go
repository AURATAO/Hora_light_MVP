package main

// The review sandbox and money (sandbox.go, recordSandboxPayout).
//
// A reviewer cannot pass Stripe's hosted onboarding — it asks for a date of
// birth, an SSN and a bank account — so the sandbox account is declared
// payable by fiat and paid on paper. These tests pin the three things that
// makes safe:
//
//  1. a sandbox supporter accepts with no connected account at all;
//  2. a sandbox settlement writes a paid payout row and NEVER calls Stripe,
//     and nothing (admin retry included) can later turn that row into a
//     Transfer;
//  3. every other account is gated and paid exactly as before.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func markSandbox(t *testing.T, uids ...string) {
	t.Helper()
	for _, uid := range uids {
		mustExec(t, `update public.users set is_sandbox = true where id = $1::uuid`, uid)
	}
}

func callEarnings(t *testing.T, uid string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/payments/earnings", nil)
	c.Set("uid", uid)
	earningsHandler(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func callConnectRoute(t *testing.T, h gin.HandlerFunc, path, uid, email string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("uid", uid)
	c.Set("email", email)
	h(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// ── 1. The accept gate ─────────────────────────────────────────────────────

func TestSandboxSupporterAcceptsWithoutPayoutSetup(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)

	// Control, before the flag: the same supporter, with no connected account,
	// is refused exactly as every real supporter is.
	code, body := acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusForbidden || body["error"] != "payouts_onboarding_required" {
		t.Fatalf("real supporter with no account: %d (%v), want 403 payouts_onboarding_required", code, body)
	}
	if got := assignedTo(t, w.taskID); got != "" {
		t.Fatalf("a refused accept claimed the task for %q", got)
	}

	// Both sides of the task on the sandbox side of the wall, as the review
	// login leaves them. Still no connected account, still no cached
	// payouts_enabled / transfers_active.
	markSandbox(t, w.requesterID, w.supporterID)

	ready, msg, err := supporterPayoutGate(context.Background(), w.supporterID)
	if err != nil || !ready || msg != "" {
		t.Fatalf("sandbox gate = ready:%v msg:%q err:%v, want ready with no message", ready, msg, err)
	}
	st := cachedStatus(t, w.supporterID)
	if !st.Sandbox || st.State != onboardingComplete || !st.PayoutsEnabled || !st.TransfersActive {
		t.Fatalf("sandbox cached status = %+v, want complete/payable/sandbox", st)
	}

	code, body = acceptAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("sandbox accept: %d (%v), want 200", code, body)
	}
	if got := assignedTo(t, w.taskID); got != w.supporterID {
		t.Errorf("task assigned to %q, want the sandbox supporter", got)
	}
}

// The two routes that would put a sandbox account in front of Stripe refuse
// it, so a reviewer who taps around the Earnings screen can never create or
// open a connected account.
func TestSandboxConnectLinksRefused(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	w := seedOpsWorld(t, "open")
	markSandbox(t, w.supporterID)

	for _, tc := range []struct {
		name string
		h    gin.HandlerFunc
		path string
	}{
		{"onboarding-link", connectOnboardingLinkHandler, "/payments/connect/onboarding-link"},
		{"login-link", connectLoginLinkHandler, "/payments/connect/login-link"},
	} {
		code, body := callConnectRoute(t, tc.h, tc.path, w.supporterID, oldSupporterEmail)
		if code != http.StatusConflict || body["error"] != "sandbox_account" {
			t.Errorf("%s for a sandbox account: %d (%v), want 409 sandbox_account", tc.name, code, body)
		}
	}
	// Nothing was created at Stripe or on the row.
	var acct *string
	if err := db.QueryRow(context.Background(),
		`select stripe_account_id from public.users where id = $1::uuid`, w.supporterID).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	if acct != nil && *acct != "" {
		t.Errorf("sandbox account got a connected account %q", *acct)
	}
}

// ── 2. Settlement: paid on paper, never at Stripe ──────────────────────────

func TestSandboxSettlementRecordsPayoutWithoutTransfer(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	f := stubStripe(t)
	w := seedOpsWorld(t, "completed")
	markSandbox(t, w.requesterID, w.supporterID)
	ctx := context.Background()

	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 1450, "ch_sandbox_1")
	in := payoutInput{
		TaskID:         w.taskID,
		SupporterID:    w.supporterID,
		PaymentID:      paymentID,
		AmountCents:    1160,
		ServiceCents:   1450,
		FeeCents:       290,
		SourceChargeID: "ch_sandbox_1",
	}

	p := payoutForTask(ctx, in)
	if p == nil {
		t.Fatal("sandbox settlement recorded no payout")
	}
	if p.Status != payoutStatusPaid || p.StripeTransferID != "" {
		t.Errorf("payout = status:%q transfer:%q, want paid with no transfer", p.Status, p.StripeTransferID)
	}
	if n := len(f.transfers); n != 0 {
		t.Fatalf("Stripe was asked for %d transfer(s) on a sandbox task; want 0", n)
	}
	if got := payoutStatusOf(t, p.ID); got != payoutStatusPaid {
		t.Errorf("row status = %q, want paid", got)
	}
	meta := payoutMeta(t, p.ID)
	if meta["sandbox"] != true {
		t.Errorf("row meta = %v, want sandbox: true", meta)
	}
	var transferID *string
	var bankPaid bool
	if err := db.QueryRow(ctx, `select stripe_transfer_id, bank_paid_at is not null from public.payouts where id = $1::uuid`,
		p.ID).Scan(&transferID, &bankPaid); err != nil {
		t.Fatal(err)
	}
	if transferID != nil {
		t.Errorf("row names a transfer %q; a sandbox row must name none", *transferID)
	}
	if !bankPaid {
		t.Errorf("row has no bank_paid_at; the Earnings totals would show it in transit forever")
	}

	// Idempotent like the real path: a second settle of the same payment
	// writes nothing and still calls nobody.
	if again := payoutForTask(ctx, in); again != nil {
		t.Errorf("second settle recorded a second payout %s", again.ID)
	}
	if n := payoutCount(t, w.taskID); n != 1 {
		t.Errorf("payout rows = %d, want exactly 1", n)
	}

	// The Earnings screen shows it as an ordinary paid entry, and tells the
	// client it is the sandbox so no Stripe surface is offered.
	code, body := callEarnings(t, w.supporterID)
	if code != http.StatusOK {
		t.Fatalf("earnings: %d (%v)", code, body)
	}
	onboarding, _ := body["onboarding"].(map[string]any)
	if onboarding["sandbox"] != true || onboarding["state"] != onboardingComplete {
		t.Errorf("earnings.onboarding = %v, want sandbox:true state:complete", onboarding)
	}
	transfers, _ := body["transfers"].([]any)
	if len(transfers) != 1 {
		t.Fatalf("earnings.transfers = %v, want the one sandbox payout", body["transfers"])
	}
	row, _ := transfers[0].(map[string]any)
	if row["status"] != payoutStatusPaid || row["amount_cents"] != float64(1160) {
		t.Errorf("earnings row = %v, want paid / 1160", row)
	}
	if earned, _ := body["lifetime_earned_cents"].(float64); earned != 1160 {
		t.Errorf("lifetime_earned_cents = %v, want 1160", body["lifetime_earned_cents"])
	}

	// An operator cannot turn it into a real transfer.
	code, body = callAdminRetry(t, p.ID, w.adminID)
	if code != http.StatusConflict || body["error"] != "sandbox_payout" {
		t.Errorf("admin retry of a sandbox payout: %d (%v), want 409 sandbox_payout", code, body)
	}
	if n := len(f.transfers); n != 0 {
		t.Fatalf("admin retry sent %d transfer(s) for a sandbox payout; want 0", n)
	}
	if got := payoutAttempts(t, p.ID); got != 1 {
		t.Errorf("attempt_count = %d after a refused retry, want unchanged (1)", got)
	}
}

// ── 3. Everyone else, exactly as before ────────────────────────────────────

func TestRealSupporterPayoutPathUnchangedBySandbox(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")
	f := stubStripe(t)
	w := seedOpsWorld(t, "completed")
	ctx := context.Background()

	// No connected account: nothing is written and nothing is sent, which is
	// the pre-existing "unpayable supporter" outcome — not a paper payout.
	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 1450, "ch_real_1")
	in := payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID,
		AmountCents: 1160, ServiceCents: 1450, FeeCents: 290, SourceChargeID: "ch_real_1",
	}
	if p := payoutForTask(ctx, in); p != nil {
		t.Fatalf("real supporter with no account got a payout row %s", p.ID)
	}
	if n := payoutCount(t, w.taskID); n != 0 {
		t.Fatalf("payout rows = %d for an unpayable real supporter, want 0", n)
	}
	if n := len(f.transfers); n != 0 {
		t.Fatalf("transfers = %d for an unpayable real supporter, want 0", n)
	}

	// With an account: a real Transfer, as ever.
	linkConnectAccount(t, w.supporterID, "acct_real_1")
	mustExec(t, `update public.users set stripe_payouts_enabled = true, stripe_transfers_active = true,
	             stripe_details_submitted = true where id = $1::uuid`, w.supporterID)
	p := payoutForTask(ctx, in)
	if p == nil || p.Status != payoutStatusPaid || p.StripeTransferID == "" {
		t.Fatalf("real payout = %+v, want paid with a transfer id", p)
	}
	if n := len(f.transfers); n != 1 {
		t.Fatalf("transfers = %d for a payable real supporter, want 1", n)
	}
	if meta := payoutMeta(t, p.ID); meta["sandbox"] != nil {
		t.Errorf("real payout row carries a sandbox mark: %v", meta)
	}
	st := cachedStatus(t, w.supporterID)
	if st.Sandbox {
		t.Errorf("real supporter's status reports sandbox")
	}
}
