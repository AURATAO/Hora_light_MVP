package main

// Promo codes: the requester's discount, the supporter's untouched pay.
//
// Two layers, same split as the payments suites:
//
//  1. Arithmetic (no DB). What a discount does to a hold and a total, and how
//     a settlement splits into transfers when the platform owes the supporter
//     the part the requester never paid.
//
//  2. Rows (DB, no network). The validation rules against real promo_codes
//     rows, per-user-once at the unique index, the release on a free cancel,
//     and the whole money path — hold, capture, balance, payout, receipt —
//     with the four Stripe calls swapped for fixtures that record what they
//     were asked for.
//
//	docker run -d --rm --name hora-b13-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run 'Promo|Receipt|Deposit' -v -count=1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// ── 1. Arithmetic ──────────────────────────────────────────────────────────

func TestPromoArithmetic(t *testing.T) {
	cases := []struct {
		name                   string
		discount, total        int
		wantApplied, wantAfter int
	}{
		// $24.00 is the 30-minute estimate: $15.00 + 15 min × $0.60.
		{"ordinary: $10 off $24.00", 1000, 2400, 1000, 1400},
		{"exactly the total", 2400, 2400, 2400, 0},
		{"more than the total: never below $0", 5000, 2400, 2400, 0},
		{"no discount", 0, 2400, 0, 2400},
		{"nothing to discount", 1000, 0, 0, 0},
		{"negative inputs are ignored", -5, -5, 0, -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promoApplied(tc.discount, tc.total); got != tc.wantApplied {
				t.Errorf("applied = %d, want %d", got, tc.wantApplied)
			}
			if got := afterPromo(tc.total, tc.discount); got != tc.wantAfter {
				t.Errorf("after = %d, want %d", got, tc.wantAfter)
			}
		})
	}

	// The hold: base $15 + 15 billable min × $0.60 = $24.00, plus a $30 budget,
	// less the $10 promo.
	rate := Billing.PerMinuteRateCents
	if got := preAuthAfterPromoCents("delivery", 30, 3000, rate, 1000); got != 1500+15*60+3000-1000 {
		t.Errorf("hold with $10 promo = %s, want $44.00", formatCentsUSD(got))
	}
	if got := preAuthAfterPromoCents("delivery", 30, 0, rate, 5000); got != 0 {
		t.Errorf("hold under a promo larger than the estimate = %s, want $0.00", formatCentsUSD(got))
	}
	// Companionship bills its own base and the promo comes off that too.
	if got := preAuthAfterPromoCents("companion", 15, 0, rate, 1000); got != 1500 {
		t.Errorf("companionship hold with $10 promo = %s, want $15.00 ($25 base − $10)", formatCentsUSD(got))
	}
}

// The supporter is paid the UNDISCOUNTED settlement, and the part the
// requester's discount took off is a third transfer from the platform.
func TestPromoPayoutIsUndiscounted(t *testing.T) {
	// Fee off: these are the funding-shape cases. What the fee does to a
	// promo-subsidised task is pinned in platform_fee_test.go.
	withPlatformFeeBps(t, 0)
	cases := []struct {
		name                                          string
		total, mainCaptured, balanceCaptured, subsidy int
		want                                          []payoutSplit
	}{
		// $30.00 owed ($15.00 + 25 min × $0.60); the requester paid $20.00 (a
		// $10 promo); the platform pays the $10.
		{"promo inside the hold", 3000, 2000, 0, 1000,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 2000}, {Source: payoutSourcePromo, ServiceCents: 1000}}},
		// A zero hold: everything the requester owed was a balance charge,
		// and the platform pays the discount.
		{"zero hold, balance collected", 3000, 0, 500, 2500,
			[]payoutSplit{{Source: payoutSourceBalance, ServiceCents: 500}, {Source: payoutSourcePromo, ServiceCents: 2500}}},
		// A promo that covered the whole settlement: one transfer, all
		// platform money.
		{"fully discounted", 1500, 0, 0, 1500,
			[]payoutSplit{{Source: payoutSourcePromo, ServiceCents: 1500}}},
		// The balance charge failed. The subsidy is still paid — the
		// platform's promise does not depend on the requester's card — and
		// only the uncollected balance is a shortfall.
		{"balance failed, subsidy still paid", 3000, 0, 0, 2500,
			[]payoutSplit{{Source: payoutSourcePromo, ServiceCents: 2500, ShortfallCents: 500}}},
		{"balance failed after a partial hold", 3000, 1000, 0, 1000,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 1000, ShortfallCents: 1000}, {Source: payoutSourcePromo, ServiceCents: 1000}}},
		// No promo: exactly the Phase 3 shape.
		{"no promo", 6000, 4950, 1050, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 4950}, {Source: payoutSourceBalance, ServiceCents: 1050}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := settlementPayouts(supporterPayFor(tc.total, 0), tc.mainCaptured, tc.balanceCaptured, tc.subsidy)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			paid, short := 0, 0
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("split %d = %+v, want %+v", i, got[i], tc.want[i])
				}
				paid += got[i].AmountCents()
				short += got[i].ShortfallCents
			}
			// Never overpaid, never paid out of money nobody put in, and the
			// row's two numbers reconcile to what was owed.
			if paid > tc.total {
				t.Errorf("paid %d on a settlement of %d", paid, tc.total)
			}
			if funded := tc.mainCaptured + tc.balanceCaptured + tc.subsidy; paid > funded {
				t.Errorf("paid %d out of %d funded", paid, funded)
			}
			if paid+short != tc.total {
				t.Errorf("paid %d + shortfall %d != owed %d", paid, short, tc.total)
			}
		})
	}
}

// ── Stripe, replaced ───────────────────────────────────────────────────────

// stripeFixture records what the settlement path asked Stripe for, and
// answers as a working card and a working connected account would.
type stripeFixture struct {
	mu        sync.Mutex
	holds     []int64 // paymentintent.New amounts with manual capture
	captures  []int64 // paymentintent.Capture amounts
	charges   []int64 // paymentintent.New amounts with automatic capture
	transfers []struct {
		Cents  int64
		Source string
	}
}

func stubStripe(t *testing.T) *stripeFixture {
	t.Helper()
	forceStripeKey(t, "sk_test_fixture")
	f := &stripeFixture{}
	n := 0
	next := func(prefix string) string {
		n++
		return fmt.Sprintf("%s_%d", prefix, n)
	}
	prevNew, prevCap, prevTr, prevPM := stripeCreatePaymentIntent, stripeCapturePaymentIntent,
		stripeCreateTransfer, stripeDefaultPaymentMethodFor
	stripeCreatePaymentIntent = func(_ string, params *stripe.PaymentIntentParams) (*stripe.PaymentIntent, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := next("pi")
		amount := *params.Amount
		card := &stripe.Charge{ID: "ch_" + id, PaymentMethodDetails: &stripe.ChargePaymentMethodDetails{
			Card: &stripe.ChargePaymentMethodDetailsCard{Brand: "visa", Last4: "4242"},
		}}
		if params.CaptureMethod != nil && *params.CaptureMethod == string(stripe.PaymentIntentCaptureMethodManual) {
			f.holds = append(f.holds, amount)
			return &stripe.PaymentIntent{ID: id, Amount: amount, Status: stripe.PaymentIntentStatusRequiresCapture, LatestCharge: card}, nil
		}
		f.charges = append(f.charges, amount)
		return &stripe.PaymentIntent{ID: id, Amount: amount, AmountReceived: amount,
			Status: stripe.PaymentIntentStatusSucceeded, LatestCharge: card}, nil
	}
	stripeCapturePaymentIntent = func(_, id string, params *stripe.PaymentIntentCaptureParams) (*stripe.PaymentIntent, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		amount := *params.AmountToCapture
		f.captures = append(f.captures, amount)
		return &stripe.PaymentIntent{ID: id, AmountReceived: amount, Status: stripe.PaymentIntentStatusSucceeded,
			LatestCharge: &stripe.Charge{ID: "ch_" + id}}, nil
	}
	stripeCreateTransfer = func(_ string, params *stripe.TransferParams) (*stripe.Transfer, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		src := ""
		if params.SourceTransaction != nil {
			src = *params.SourceTransaction
		}
		f.transfers = append(f.transfers, struct {
			Cents  int64
			Source string
		}{*params.Amount, src})
		id := next("tr")
		return &stripe.Transfer{ID: id, DestinationPayment: &stripe.Charge{ID: "py_" + id}}, nil
	}
	stripeDefaultPaymentMethodFor = func(string, string) (string, error) { return "pm_fixture", nil }
	t.Cleanup(func() {
		stripeCreatePaymentIntent, stripeCapturePaymentIntent, stripeCreateTransfer,
			stripeDefaultPaymentMethodFor = prevNew, prevCap, prevTr, prevPM
	})
	return f
}

// ── Seeding ────────────────────────────────────────────────────────────────

type promoOpts struct {
	validFrom, validUntil   *time.Time
	maxRedemptions          *int
	firstTaskOnly, inactive bool
}

func seedPromoCode(t *testing.T, code string, cents int, o promoOpts) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.promo_codes (code, amount_cents, valid_from, valid_until, max_redemptions, first_task_only, active, deactivated_at)
		values ($1, $2, $3, $4, $5, $6, $7, case when $7 then null else now() end)
		returning id::text
	`, code, cents, o.validFrom, o.validUntil, o.maxRedemptions, o.firstTaskOnly, !o.inactive).Scan(&id); err != nil {
		t.Fatalf("seed promo %s: %v", code, err)
	}
	return id
}

func seedRedemption(t *testing.T, promoID, userID, taskID string, cents int) error {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		insert into public.promo_redemptions (promo_code_id, user_id, task_id, discount_cents)
		values ($1::uuid, $2::uuid, $3::uuid, $4)
	`, promoID, userID, taskID, cents)
	return err
}

func redemptionsFor(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.promo_redemptions where task_id = $1::uuid`, taskID).Scan(&n); err != nil {
		t.Fatalf("count redemptions: %v", err)
	}
	return n
}

func hoursAgo(h int) *time.Time {
	v := time.Now().Add(-time.Duration(h) * time.Hour)
	return &v
}

func hoursAhead(h int) *time.Time {
	v := time.Now().Add(time.Duration(h) * time.Hour)
	return &v
}

func intPtr(n int) *int { return &n }

// givesCard makes the requester chargeable without Stripe: a stored Customer
// id takes stripeCustomerFor's fast path, and the fixture answers the card.
func giveCard(t *testing.T, uid string) {
	t.Helper()
	mustExec(t, `update public.users set stripe_customer_id = 'cus_fixture' where id = $1::uuid`, uid)
}

func setPaymentCard(t *testing.T, paymentID, brand, last4 string) {
	t.Helper()
	mustExec(t, `update public.payments set card_brand = $2, card_last4 = $3 where id = $1::uuid`, paymentID, brand, last4)
}

func payoutRows(t *testing.T, taskID string) []struct {
	Funding string
	Cents   int
	Status  string
	Payment *string
} {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		select funding, amount_cents, status, payment_id::text from public.payouts
		 where task_id = $1::uuid order by created_at, funding
	`, taskID)
	if err != nil {
		t.Fatalf("payouts: %v", err)
	}
	defer rows.Close()
	var out []struct {
		Funding string
		Cents   int
		Status  string
		Payment *string
	}
	for rows.Next() {
		var r struct {
			Funding string
			Cents   int
			Status  string
			Payment *string
		}
		if err := rows.Scan(&r.Funding, &r.Cents, &r.Status, &r.Payment); err != nil {
			t.Fatalf("scan payout: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func promoCheck(t *testing.T, code, uid string) error {
	t.Helper()
	_, err := checkPromoForUser(context.Background(), sqldb, code, uid, "", time.Now())
	return err
}

func wantPromoError(t *testing.T, err error, code string) {
	t.Helper()
	var pe *PromoError
	if !errors.As(err, &pe) {
		t.Fatalf("got %v, want a PromoError %s", err, code)
	}
	if pe.Code != code {
		t.Fatalf("got %s (%q), want %s", pe.Code, pe.Message, code)
	}
	if pe.Message == "" || !strings.HasSuffix(pe.Message, ".") {
		t.Errorf("message %q should be a sentence the requester can act on", pe.Message)
	}
}

// ── 2. The rules ───────────────────────────────────────────────────────────

func TestPromoValidationRules(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	newUser := seedUser(t, "brand.new@example.com", "Nina New", false)

	seedPromoCode(t, "WELCOME10", 1000, promoOpts{})
	seedPromoCode(t, "GONE", 500, promoOpts{inactive: true})
	seedPromoCode(t, "LATER", 500, promoOpts{validFrom: hoursAhead(1)})
	seedPromoCode(t, "OVER", 500, promoOpts{validUntil: hoursAgo(1)})
	seedPromoCode(t, "LIVE", 500, promoOpts{validFrom: hoursAgo(1), validUntil: hoursAhead(1)})
	limited := seedPromoCode(t, "ONLYONE", 500, promoOpts{maxRedemptions: intPtr(1)})
	used := seedPromoCode(t, "MINE", 500, promoOpts{})
	seedPromoCode(t, "FIRST", 500, promoOpts{firstTaskOnly: true})

	// w.taskID is the requester's open task, so they have "posted before";
	// newUser has not.
	if err := seedRedemption(t, limited, newUser, w.taskID, 500); err != nil {
		t.Fatalf("seed limited redemption: %v", err)
	}
	other := seedReassignTask(t, "completed", w.requesterID, requesterEmail, "", "")
	if err := seedRedemption(t, used, w.requesterID, other, 500); err != nil {
		t.Fatalf("seed used redemption: %v", err)
	}

	// Happy paths: the plain code, case-insensitive with stray whitespace, a
	// window that is open, and first-task-only for somebody on their first.
	for _, code := range []string{"WELCOME10", "welcome10", "  Welcome10 ", "LIVE"} {
		if err := promoCheck(t, code, w.requesterID); err != nil {
			t.Errorf("%q refused: %v", code, err)
		}
	}
	if err := promoCheck(t, "FIRST", newUser); err != nil {
		t.Errorf("FIRST refused for a first-time requester: %v", err)
	}
	// A requester with no users row yet is a first-time requester with no
	// redemptions: every per-user check passes.
	if err := promoCheck(t, "FIRST", ""); err != nil {
		t.Errorf("FIRST refused for an account with no row yet: %v", err)
	}

	// Each refusal, with the code the client branches on.
	wantPromoError(t, promoCheck(t, "NOPE", w.requesterID), promoErrInvalid)
	wantPromoError(t, promoCheck(t, "", w.requesterID), promoErrInvalid)
	wantPromoError(t, promoCheck(t, "GONE", w.requesterID), promoErrInvalid)
	wantPromoError(t, promoCheck(t, "LATER", w.requesterID), promoErrExpired)
	wantPromoError(t, promoCheck(t, "OVER", w.requesterID), promoErrExpired)
	wantPromoError(t, promoCheck(t, "ONLYONE", w.requesterID), promoErrExhausted)
	wantPromoError(t, promoCheck(t, "MINE", w.requesterID), promoErrUsed)
	wantPromoError(t, promoCheck(t, "FIRST", w.requesterID), promoErrNotFirstTask)

	// The messages are distinct — four different situations, four different
	// sentences, or the requester cannot tell what to do.
	seen := map[string]bool{}
	for _, e := range []*PromoError{errPromoInvalid, errPromoExpired, errPromoUsed, errPromoNotFirstTask, errPromoExhausted} {
		if seen[e.Message] {
			t.Errorf("two refusals share the message %q", e.Message)
		}
		seen[e.Message] = true
	}
}

// Per-user-once is the unique index, whatever the handler checked.
func TestPromoPerUserOnceAtTheDatabase(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	promoID := seedPromoCode(t, "TWICE", 500, promoOpts{})
	second := seedReassignTask(t, "open", w.requesterID, requesterEmail, "", "")

	if err := seedRedemption(t, promoID, w.requesterID, w.taskID, 500); err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	err := seedRedemption(t, promoID, w.requesterID, second, 500)
	if err == nil || !strings.Contains(err.Error(), "uq_promo_redemptions_user_code") {
		t.Fatalf("second redemption by the same user: err=%v, want the unique index", err)
	}

	// And the in-transaction path maps that violation to the sentence.
	tx, err := sqldb.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	p, err := lookupPromoCode(context.Background(), tx, "TWICE")
	if err != nil || p == nil {
		t.Fatalf("lookup: %v", err)
	}
	wantPromoError(t, redeemPromo(context.Background(), tx, p, w.requesterID, second), promoErrUsed)
}

// A cancel that charges nothing gives the code back; one that charges does not.
func TestPromoReleasedOnFreeCancelOnly(t *testing.T) {
	setupStripeWebhookDB(t)
	promoID := seedPromoCode(t, "AGAIN", 500, promoOpts{})

	// Unaccepted: free, released.
	free := seedOpsWorld(t, "open")
	unassign(t, free.taskID)
	if err := seedRedemption(t, promoID, free.requesterID, free.taskID, 500); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if code, body := cancelAs(t, free.taskID, free.requesterID, "changed my mind"); code != http.StatusOK {
		t.Fatalf("cancel: %d %v", code, body)
	}
	if n := redemptionsFor(t, free.taskID); n != 0 {
		t.Errorf("free cancel kept the redemption (%d rows) — the code was never used", n)
	}

	// Accepted past the grace window with work logged: the base fee is
	// charged, the discount applied to it, the redemption stays.
	setupStripeWebhookDB(t) // fresh fixture: seedOpsWorld inserts fixed emails
	charged := seedOpsWorld(t, "open")
	mustExec(t, `update public.tasks set accepted_at = now() - interval '1 hour' where id = $1::uuid`, charged.taskID)
	seedWorklog(t, charged.taskID, 20, false)
	promoID2 := seedPromoCode(t, "KEPT", 500, promoOpts{})
	if err := seedRedemption(t, promoID2, charged.requesterID, charged.taskID, 500); err != nil {
		t.Fatalf("seed: %v", err)
	}
	code, body := cancelAs(t, charged.taskID, charged.requesterID, "no longer needed")
	if code != http.StatusOK || num(body["bill_cents"]) <= 0 {
		t.Fatalf("charged cancel: %d %v", code, body)
	}
	if n := redemptionsFor(t, charged.taskID); n != 1 {
		t.Errorf("a charged cancel released the redemption — the discount was applied to that charge")
	}
}

// The post form's quote: the discount shown beside the estimate, the hold
// discounted, the undiscounted total untouched — and a bad code as a message
// beside a still-correct quote rather than a failed quote.
func TestPromoEstimateShowsTheDiscount(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedPromoCode(t, "WELCOME10", 1000, promoOpts{})

	quote := func(promo string) map[string]any {
		// A daytime start, not ASAP: see daytimeStart in billing_test.go.
		body := fmt.Sprintf(`{"category":"delivery","estimated_minutes":30,"prepay_amount_cents":0,`+daytimeStart+`,"promo_code":%q}`, promo)
		code, rec := callTaskHandler(t, estimateTaskCost, http.MethodPost, "/tasks/estimate", "", w.requesterID, requesterEmail, body, nil)
		if code != http.StatusOK {
			t.Fatalf("estimate: %d %s", code, rec.Body.String())
		}
		return decodeFirstJSON(t, rec)
	}

	q := quote("welcome10")
	// $15.00 + 15 min × $0.60 = $24.00, less $10.00.
	if num(q["total_cents"]) != 1500+15*60 || num(q["promo_discount_cents"]) != 1000 ||
		num(q["total_after_promo_cents"]) != 1400 || num(q["hold_cents"]) != 1400 || q["promo_code"] != "WELCOME10" {
		t.Errorf("quote with promo = %v", q)
	}

	q = quote("NOPE")
	if num(q["total_cents"]) != 2400 || num(q["hold_cents"]) != 2400 || q["promo_error_code"] != promoErrInvalid {
		t.Errorf("quote with a bad code = %v", q)
	}
	if _, ok := q["promo_discount_cents"]; ok {
		t.Error("a refused code still produced a discount")
	}

	q = quote("")
	for _, k := range []string{"promo_code", "promo_discount_cents", "promo_error"} {
		if _, ok := q[k]; ok {
			t.Errorf("a quote with no code carries %s", k)
		}
	}
}

// ── 3. The money path ──────────────────────────────────────────────────────

// The hold is the discounted amount; a discount that covers the estimate
// places no hold at all and asks Stripe for nothing.
func TestPromoHoldIsDiscounted(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	promoID := seedPromoCode(t, "WELCOME10", 1000, promoOpts{})
	if err := seedRedemption(t, promoID, w.requesterID, w.taskID, 1000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mustExec(t, `update public.tasks set category = 'delivery', estimated_minutes = 30, rate_cents_per_min = $2
	              where id = $1::uuid`, w.taskID, Billing.PerMinuteRateCents)

	p, err := CreatePreAuth(context.Background(), PreAuthInput{
		TaskID: w.taskID, RequesterID: w.requesterID, Category: "delivery",
		EstimatedMinutes: 30, RateCents: Billing.PerMinuteRateCents, PromoDiscountCents: 1000,
	})
	if err != nil {
		t.Fatalf("pre-auth: %v", err)
	}
	// $15.00 + 15 min × $0.60 = $24.00, less the $10.00 promo.
	if len(f.holds) != 1 || f.holds[0] != 1400 {
		t.Fatalf("Stripe was asked to hold %v, want [1400] ($24.00 − $10.00)", f.holds)
	}
	if derefIntOr(p.AuthorizedCents, -1) != 1400 {
		t.Errorf("row authorized %d, want 1400", derefIntOr(p.AuthorizedCents, -1))
	}

	// The requester's view says all three numbers, and the base on its own
	// line — no subtraction left to a client.
	view := taskPaymentView(context.Background(), w.taskID)
	if view == nil {
		t.Fatal("no payment view")
	}
	if view.AuthorizedCents != 1400 || view.PromoDiscountCents != 1000 || view.PreDiscountCents != 2400 ||
		view.PromoCode != "WELCOME10" || view.BaseFeeCents != 1500 || view.MinutesCostCents != 15*60 ||
		view.CardLast4 != "4242" {
		t.Errorf("view = %+v", view)
	}

	// The zero hold.
	second := seedReassignTask(t, "open", w.requesterID, requesterEmail, "", "")
	p, err = CreatePreAuth(context.Background(), PreAuthInput{
		TaskID: second, RequesterID: w.requesterID, Category: "delivery",
		EstimatedMinutes: 30, RateCents: Billing.PerMinuteRateCents, PromoDiscountCents: 5000,
	})
	if err != nil {
		t.Fatalf("zero-hold pre-auth: %v", err)
	}
	if len(f.holds) != 1 {
		t.Errorf("a fully discounted hold still asked Stripe for %v", f.holds[1:])
	}
	if p.Status != paymentStatusAuthorized || derefIntOr(p.AuthorizedCents, -1) != 0 || p.StripePaymentIntentID != "" {
		t.Errorf("zero-hold row = %+v, want authorized for $0 with no intent", p)
	}
}

// The whole settlement under a promo: the capture is the discounted total,
// the supporter's payouts sum to the undiscounted one, the receipt carries the
// discount line, and the supporter's own view of the task never mentions it.
func TestPromoCaptureDiscountedPayoutUndiscounted(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_promo")
	giveCard(t, w.requesterID)

	promoID := seedPromoCode(t, "WELCOME10", 1000, promoOpts{})
	if err := seedRedemption(t, promoID, w.requesterID, w.taskID, 1000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// $15.00 base + 25 billable min × $0.60 = $30.00 owed to the supporter.
	seedWorklog(t, w.taskID, 40, false)
	paymentID := seedPayment(t, w.taskID, w.requesterID, "pi_hold", paymentStatusAuthorized)
	setPaymentCard(t, paymentID, "visa", "4242")

	completeAs(t, w.taskID, w.supporterID, map[string]any{"completion_photo_url": "https://x/done.jpg"}, http.StatusOK)

	if len(f.captures) != 1 || f.captures[0] != 2000 {
		t.Fatalf("captured %v, want [2000] ($30.00 − $10.00)", f.captures)
	}
	if len(f.charges) != 0 {
		t.Errorf("a balance was charged (%v) on a task the hold covered", f.charges)
	}

	rows := payoutRows(t, w.taskID)
	paid := map[string]int{}
	for _, r := range rows {
		paid[r.Funding] += r.Cents
		if r.Status != payoutStatusPaid {
			t.Errorf("payout %+v not paid", r)
		}
		if (r.Funding == payoutFundingCharge) != (r.Payment != nil) {
			t.Errorf("payout %+v: funding and payment_id disagree", r)
		}
	}
	// The supporter's 70% is of the UNDISCOUNTED $30.00 = $21.00 (D-14): the
	// charge funds $20.00 of it and the platform makes up the $1.00 — the
	// $10.00 discount less the $9.00 fee it would otherwise have kept.
	if paid[payoutFundingCharge] != 2000 || paid[payoutFundingPromoSubsidy] != 100 {
		t.Errorf("payouts = %+v, want $20.00 from the charge + $1.00 subsidy", rows)
	}
	// The subsidy transfer is unbound; the charge-funded one names its charge.
	for _, tr := range f.transfers {
		switch tr.Cents {
		case 2000:
			if tr.Source == "" {
				t.Error("the charge-funded transfer has no source_transaction")
			}
		case 100:
			if tr.Source != "" {
				t.Error("the subsidy transfer is bound to a charge it exceeds")
			}
		default:
			t.Errorf("unexpected transfer %+v", tr)
		}
	}

	// The requester's settlement view: discounted total, code named.
	s := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["settlement"].(map[string]any)
	if num(s["total_cents"]) != 3000 || num(s["promo_discount_cents"]) != 1000 ||
		num(s["total_after_promo_cents"]) != 2000 || s["promo_code"] != "WELCOME10" || num(s["captured_cents"]) != 2000 {
		t.Errorf("requester settlement = %v", s)
	}
	// The supporter's: no code, no discount, the full amount earned.
	s = getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)["settlement"].(map[string]any)
	for _, k := range []string{"promo_code", "promo_discount_cents", "total_after_promo_cents"} {
		if _, ok := s[k]; ok {
			t.Errorf("the supporter's settlement carries %s", k)
		}
	}
	if earned := s["earned"].(map[string]any); num(earned["total_cents"]) != 2100 || num(earned["service_gross_cents"]) != 3000 {
		t.Errorf("supporter earned %v, want $21.00: 70%% of the undiscounted $30.00", earned)
	}

	// The receipt: once, for the discounted charge, with the promo line.
	if n := countNotifications(t, w.requesterID, "RECEIPT"); n != 1 {
		t.Fatalf("%d receipts, want 1", n)
	}
	body := notificationBody(t, w.requesterID, "RECEIPT")
	// The fixture hold is $50.00, so $50.00 − $20.00 = $30.00 goes back.
	for _, want := range []string{"Charged $20.00 to Visa ••4242", "$15.00 base", "25 min × $0.60", "− $10.00 promo (WELCOME10)", "$30.00 of the hold released"} {
		if !strings.Contains(body, want) {
			t.Errorf("receipt body %q lacks %q", body, want)
		}
	}
	// The requester's completion email says the discounted figure too.
	if b := notificationBody(t, w.requesterID, "COMPLETED"); strings.Contains(b, "$30.00") {
		t.Errorf("the requester's completion notice quotes the undiscounted total: %q", b)
	}
	if n := countNotifications(t, w.supporterID, "RECEIPT"); n != 0 {
		t.Errorf("the supporter got %d receipt(s)", n)
	}
}

// A promo that covered the whole estimate: nothing was held, so what the task
// actually settled for above the discount is a balance charge, receipted as
// the one charge it was.
func TestPromoZeroHoldCollectsTheRestAsABalance(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_promo")
	giveCard(t, w.requesterID)

	// $24.00: exactly the 30-minute estimate ($15.00 + 15 min × $0.60).
	promoID := seedPromoCode(t, "FREEBIE", 2400, promoOpts{})
	if err := seedRedemption(t, promoID, w.requesterID, w.taskID, 2400); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The zero-hold row CreatePreAuth writes.
	var paymentID string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, 0) returning id::text
	`, w.taskID, w.requesterID, paymentKindTaskPayment, paymentStatusAuthorized).Scan(&paymentID); err != nil {
		t.Fatalf("seed zero hold: %v", err)
	}
	seedWorklog(t, w.taskID, 40, false) // $15.00 + 25 × $0.60 = $30.00 owed; $6.00 after the promo

	completeAs(t, w.taskID, w.supporterID, map[string]any{"completion_photo_url": "https://x/done.jpg"}, http.StatusOK)

	if len(f.captures) != 0 {
		t.Errorf("a zero hold was captured at Stripe: %v", f.captures)
	}
	if len(f.charges) != 1 || f.charges[0] != 600 {
		t.Fatalf("balance charged %v, want [600]", f.charges)
	}
	paid := map[string]int{}
	for _, r := range payoutRows(t, w.taskID) {
		paid[r.Funding] += r.Cents
	}
	// $30.00 of service nets $21.00 after the 30% fee; the balance charge
	// funds $6.00 of it and the platform the remaining $15.00.
	if paid[payoutFundingCharge] != 600 || paid[payoutFundingPromoSubsidy] != 1500 {
		t.Errorf("payouts = %v, want $6.00 from the balance charge + $15.00 subsidy", paid)
	}
	if n := countNotifications(t, w.requesterID, "RECEIPT"); n != 1 {
		t.Fatalf("%d receipts, want 1", n)
	}
	body := notificationBody(t, w.requesterID, "RECEIPT")
	if !strings.Contains(body, "Charged $6.00") || !strings.Contains(body, "− $24.00 promo (FREEBIE)") {
		t.Errorf("receipt body = %q", body)
	}
	if strings.Contains(body, "released") {
		t.Errorf("a zero hold released something: %q", body)
	}
	if bal := countNotifications(t, w.requesterID, "BALANCE_DUE"); bal != 0 {
		t.Errorf("a collected balance was reported as due")
	}
}

// ── 4. Ops ─────────────────────────────────────────────────────────────────

func TestPromoAdminEndpoints(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	create := func(body string) (int, map[string]any) {
		return callAdminOps(t, adminCreatePromoCode, "", "", w.adminID, adminEmail, body)
	}
	code, out := create(`{"code":" launch25 ","amount_cents":2500,"first_task_only":true,"max_redemptions":2,"note":"IG launch"}`)
	if code != http.StatusCreated || out["code"] != "launch25" || num(out["amount_cents"]) != 2500 || out["active"] != true {
		t.Fatalf("create: %d %v", code, out)
	}
	id := out["id"].(string)

	// Duplicate, case-insensitively.
	if code, out := create(`{"code":"LAUNCH25","amount_cents":100}`); code != http.StatusConflict {
		t.Errorf("duplicate: %d %v", code, out)
	}
	// Refusals, each with a sentence.
	for _, body := range []string{`{"code":"","amount_cents":100}`, `{"code":"X","amount_cents":0}`, `{"code":"X","amount_cents":1,"max_redemptions":0}`} {
		if code, out := create(body); code != http.StatusBadRequest || out["message"] == nil {
			t.Errorf("%s: %d %v", body, code, out)
		}
	}
	// Not an admin.
	if code, _ := callAdminOps(t, adminCreatePromoCode, "", "", w.requesterID, requesterEmail, `{"code":"Y","amount_cents":1}`); code != http.StatusForbidden {
		t.Errorf("non-admin create: %d", code)
	}

	// The requester can use it, and the list counts it.
	if err := promoCheck(t, "launch25", w.requesterID); err == nil {
		t.Errorf("first-task-only code accepted for a requester with an open task")
	}
	newUser := seedUser(t, "first.timer@example.com", "Fern First", false)
	if err := promoCheck(t, "LAUNCH25", newUser); err != nil {
		t.Errorf("new requester refused: %v", err)
	}
	task := seedReassignTask(t, "open", newUser, "first.timer@example.com", "", "")
	if err := seedRedemption(t, id, newUser, task, 2500); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	code, _ = callAdminOps(t, adminListPromoCodes, "", "", w.adminID, adminEmail, "")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	var list []PromoCode
	lc, lw := callTaskHandler(t, func(c *gin.Context) {
		c.Set("uid", w.adminID)
		adminListPromoCodes(c)
	}, http.MethodGet, "/admin/promo-codes", "", w.adminID, adminEmail, "", nil)
	if lc != http.StatusOK {
		t.Fatalf("list: %d", lc)
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Redemptions != 1 {
		t.Fatalf("list = %s (%v)", lw.Body.String(), err)
	}

	// Deactivate: no new redemptions, the existing task keeps its discount.
	if code, out := callAdminOps(t, adminDeactivatePromoCode, "deactivate", id, w.adminID, adminEmail, ""); code != http.StatusOK || out["active"] != false {
		t.Fatalf("deactivate: %d %v", code, out)
	}
	wantPromoError(t, promoCheck(t, "LAUNCH25", seedUser(t, "late@example.com", "Lee Late", false)), promoErrInvalid)
	if _, cents := promoDiscountForTask(context.Background(), task); cents != 2500 {
		t.Errorf("deactivating the code changed a posted task's discount to %d", cents)
	}
	if code, _ := callAdminOps(t, adminDeactivatePromoCode, "deactivate", "00000000-0000-0000-0000-000000000000", w.adminID, adminEmail, ""); code != http.StatusNotFound {
		t.Errorf("deactivate unknown: %d", code)
	}
}
