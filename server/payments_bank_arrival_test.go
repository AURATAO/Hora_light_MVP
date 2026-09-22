package main

// The bank leg, and what the Earnings screen says about it.
//
// Two rules, each pinned below:
//
//  1. "Earned all time" is the sum of SUCCESSFUL transfers. A failed transfer
//     is not earnings, however it renders. Beneath it, the split by where the
//     money is — in the bank, or still on its way — as two real numbers.
//  2. A row says one of three words: paid / on its way / failed. A failed
//     row used to render like a paid one, which is the worst possible thing
//     for a screen about somebody's money to do.
//
// Stripe is not on the network here: the two calls the reconciler makes are
// swapped for fixtures, and what is under test is the join and the rows.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run 'BankArrival|EarningsSplit|V2Webhook' -v

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// stubBankLeg swaps the two Stripe calls for fixtures for one test.
func stubBankLeg(t *testing.T, byPayout map[string][]string, byTransfer map[string]string) {
	t.Helper()
	prevP, prevT := payoutDestinationPayments, transferDestinationPayment
	payoutDestinationPayments = func(_ context.Context, _ string, payoutID string) ([]string, error) {
		return byPayout[payoutID], nil
	}
	transferDestinationPayment = func(_ context.Context, transferID string) (string, error) {
		return byTransfer[transferID], nil
	}
	t.Cleanup(func() { payoutDestinationPayments, transferDestinationPayment = prevP, prevT })
}

func setTransfer(t *testing.T, taskID, transferID, destinationPayment string) {
	t.Helper()
	var py *string
	if destinationPayment != "" {
		py = &destinationPayment
	}
	mustExec(t, `update public.payouts set stripe_transfer_id = $2, stripe_destination_payment = $3
	              where task_id = $1::uuid`, taskID, transferID, py)
}

func bankPaidAtOf(t *testing.T, taskID string) *time.Time {
	t.Helper()
	var at *time.Time
	if err := db.QueryRow(context.Background(),
		`select bank_paid_at from public.payouts where task_id = $1::uuid`, taskID).Scan(&at); err != nil {
		t.Fatalf("bank_paid_at: %v", err)
	}
	return at
}

func payoutPaidEvent(accountID, payoutID string, amount int) string {
	return fmt.Sprintf(`{"id":"evt_%s","object":"event","type":"payout.paid","account":"%s","api_version":%q,
	  "data":{"object":{"id":"%s","object":"payout","amount":%d,"arrival_date":1790000000,"status":"paid"}}}`,
		payoutID, accountID, stripe.APIVersion, payoutID, amount)
}

// payout.paid stamps exactly the rows the payout carried — matched on the
// py_ each transfer created — and nothing else: not another supporter's, not
// a failed row, not a row already stamped.
func TestBankArrivalPayoutPaidStampsTheRowsItCarried(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_bank_1")

	a := seedPayoutFor(t, w, 0, 1200, payoutStatusPaid, 30)
	b := seedPayoutFor(t, w, 1, 2500, payoutStatusPaid, 20)
	c := seedPayoutFor(t, w, 2, 999, payoutStatusPaid, 10)
	f := seedPayoutFor(t, w, 3, 777, payoutStatusFailed, 5)
	setTransfer(t, a, "tr_a", "py_a")
	setTransfer(t, b, "tr_b", "py_b")
	setTransfer(t, c, "tr_c", "py_c")
	setTransfer(t, f, "", "py_f") // a failed row that somehow has a py_ — still never "arrived"

	stubBankLeg(t, map[string][]string{"po_1": {"py_a", "py_b", "py_f"}}, nil)
	if code := postSignedStripeWebhook(t, payoutPaidEvent("acct_bank_1", "po_1", 3700)); code != http.StatusOK {
		t.Fatalf("payout.paid: %d", code)
	}

	if bankPaidAtOf(t, a) == nil || bankPaidAtOf(t, b) == nil {
		t.Error("the rows the payout carried were not stamped")
	}
	if bankPaidAtOf(t, c) != nil {
		t.Error("a row the payout did NOT carry was stamped")
	}
	if bankPaidAtOf(t, f) != nil {
		t.Error("a FAILED row was stamped as arrived")
	}

	// The earnings split follows the rows, and the words follow the rows.
	page := earningsPage(t, w.supporterID, "?limit=50")
	if page.LifetimeEarnedCents != 4699 {
		t.Errorf("earned = %s, want $46.99 (three successful transfers; the failed one never counts)",
			formatCentsUSD(page.LifetimeEarnedCents))
	}
	if page.PaidOutCents != 3700 || page.InTransitCents != 999 {
		t.Errorf("split = %s paid out / %s on its way, want $37.00 / $9.99",
			formatCentsUSD(page.PaidOutCents), formatCentsUSD(page.InTransitCents))
	}
	words := map[string]string{}
	for _, tr := range page.Transfers {
		words[tr.TaskID] = tr.DisplayStatus
	}
	if words[a] != transferDisplayPaid || words[b] != transferDisplayPaid {
		t.Errorf("arrived rows render as %q/%q, want paid", words[a], words[b])
	}
	if words[c] != transferDisplayInTransit {
		t.Errorf("an unswept row renders as %q, want on_its_way", words[c])
	}
	if words[f] != transferDisplayFailed {
		t.Errorf("a failed row renders as %q, want failed — it used to look paid", words[f])
	}

	// A second payout.paid for a payout already applied changes nothing.
	if code := postSignedStripeWebhook(t, payoutPaidEvent("acct_bank_1", "po_1", 3700)); code != http.StatusOK {
		t.Fatalf("replayed payout.paid: %d", code)
	}
	if again := earningsPage(t, w.supporterID, "?limit=50"); again.PaidOutCents != 3700 {
		t.Errorf("paid out drifted to %s on a replay", formatCentsUSD(again.PaidOutCents))
	}
}

// Rows written before stripe_destination_payment existed have a transfer id
// and no py_. The reconciler fills it from the Transfer on first contact, so
// history is not stuck "on its way" forever.
func TestBankArrivalBackfillsOlderRowsFromTheTransfer(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_bank_2")
	old := seedPayoutFor(t, w, 0, 1950, payoutStatusPaid, 60)
	setTransfer(t, old, "tr_old", "")

	stubBankLeg(t, map[string][]string{"po_2": {"py_old"}}, map[string]string{"tr_old": "py_old"})
	if code := postSignedStripeWebhook(t, payoutPaidEvent("acct_bank_2", "po_2", 1950)); code != http.StatusOK {
		t.Fatalf("payout.paid: %d", code)
	}
	if bankPaidAtOf(t, old) == nil {
		t.Error("an older row was not matched after its py_ was backfilled from the transfer")
	}
	var py *string
	_ = db.QueryRow(context.Background(),
		`select stripe_destination_payment from public.payouts where task_id = $1::uuid`, old).Scan(&py)
	if py == nil || *py != "py_old" {
		t.Errorf("stripe_destination_payment = %v after backfill, want py_old", py)
	}
}

// Nothing arrived yet: earned is the transfers, everything is on its way,
// paid out is zero — and those are the numbers, not a hedge.
func TestEarningsSplitBeforeAnyBankPayout(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedPayoutFor(t, w, 0, 1200, payoutStatusPaid, 10)
	seedPayoutFor(t, w, 1, 800, payoutStatusPending, 5)
	page := earningsPage(t, w.supporterID, "?limit=50")
	if page.LifetimeEarnedCents != 1200 || page.InTransitCents != 1200 || page.PaidOutCents != 0 {
		t.Errorf("earned/in transit/paid out = %d/%d/%d, want 1200/1200/0",
			page.LifetimeEarnedCents, page.InTransitCents, page.PaidOutCents)
	}
	for _, tr := range page.Transfers {
		if tr.DisplayStatus != transferDisplayInTransit {
			t.Errorf("row %s renders as %q before any bank payout, want on_its_way", tr.TaskID, tr.DisplayStatus)
		}
	}
}

// ── The v2 event destination ───────────────────────────────────────────────

const testStripeV2Secret = "whsec_v2_test_secret"

func postStripeV2Webhook(t *testing.T, payload, signature string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/webhooks/stripe/v2", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	if signature != "" {
		c.Request.Header.Set("Stripe-Signature", signature)
	}
	handleStripeV2Webhook(c)
	return w.Code
}

func v2CapabilityEvent(id, accountID string) string {
	return fmt.Sprintf(`{"id":"%s","object":"v2.core.event","type":"v2.core.account[configuration.recipient].capability_status_updated",
	  "created":"2026-09-23T00:00:00Z","livemode":false,"context":"%s",
	  "related_object":{"id":"%s","type":"v2.core.account","url":"/v2/core/accounts/%s"},
	  "data":{"updated_capability":"stripe_balance.stripe_transfers"}}`, id, accountID, accountID, accountID)
}

// A thin event about one of our accounts re-reads that account; the handler
// needs nothing from the payload but the id. With Stripe unconfigured the
// re-read answers from the cache, so what is provable here is the routing:
// verified, deduplicated, matched to a user, acknowledged.
func TestV2WebhookRefreshesTheAccountItNames(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("STRIPE_V2_WEBHOOK_SECRET", testStripeV2Secret)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_v2_ours")

	payload := v2CapabilityEvent("evt_v2_1", "acct_v2_ours")
	if code := postStripeV2Webhook(t, payload, signStripePayload(payload, testStripeV2Secret, time.Now())); code != http.StatusOK {
		t.Fatalf("v2 event for our account: %d, want 200", code)
	}
	var processed bool
	_ = db.QueryRow(context.Background(),
		`select processed_at is not null from public.stripe_webhook_events where event_id = 'evt_v2_1'`).Scan(&processed)
	if !processed {
		t.Error("the event was not recorded as processed")
	}

	// Replay: acknowledged, not reprocessed.
	if code := postStripeV2Webhook(t, payload, signStripePayload(payload, testStripeV2Secret, time.Now())); code != http.StatusOK {
		t.Errorf("replayed v2 event: %d, want 200", code)
	}

	// Somebody else's account, or a dashboard test event: acknowledged so
	// Stripe stops sending it, never retried.
	other := v2CapabilityEvent("evt_v2_2", "acct_v2_nobody")
	if code := postStripeV2Webhook(t, other, signStripePayload(other, testStripeV2Secret, time.Now())); code != http.StatusOK {
		t.Errorf("v2 event for an unknown account: %d, want 200", code)
	}
}

// The signature is the whole authentication of the endpoint. Wrong secret,
// missing header, or no secret configured at all: 401, and nothing is read.
func TestV2WebhookRejectsWhatItCannotVerify(t *testing.T) {
	setupStripeWebhookDB(t)
	payload := v2CapabilityEvent("evt_v2_bad", "acct_v2_ours")

	t.Setenv("STRIPE_V2_WEBHOOK_SECRET", testStripeV2Secret)
	if code := postStripeV2Webhook(t, payload, signStripePayload(payload, "whsec_wrong", time.Now())); code != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d, want 401", code)
	}
	if code := postStripeV2Webhook(t, payload, ""); code != http.StatusUnauthorized {
		t.Errorf("no signature: %d, want 401", code)
	}
	// Signed correctly but with a v1-style secret only — the v2 destination
	// has its own, and a deployment without it must not accept anything.
	t.Setenv("STRIPE_V2_WEBHOOK_SECRET", "")
	if code := postStripeV2Webhook(t, payload, signStripePayload(payload, testStripeV2Secret, time.Now())); code != http.StatusUnauthorized {
		t.Errorf("no v2 secret configured: %d, want 401", code)
	}
	var n int
	_ = db.QueryRow(context.Background(),
		`select count(*) from public.stripe_webhook_events where event_id = 'evt_v2_bad'`).Scan(&n)
	if n != 0 {
		t.Error("an unverified event was written to the dedupe table")
	}
}
