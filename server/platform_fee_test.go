package main

// The platform fee (D-14): 20% of a task's SERVICE revenue, deducted from the
// supporter's pay; a receipt reimbursement is never commissioned.
//
// Two layers, same as the payments suites:
//
//  1. Arithmetic (no DB). What the fee is, how it rounds, and how it lands on
//     the transfers a settlement produces — including a promo-subsidised
//     task, where it comes off the UNDISCOUNTED service.
//
//  2. Rows (DB, no network). The breakdown is persisted and reconciles at the
//     database; rows paid before the fee are untouched; the supporter is told
//     in every surface — settlement, earnings, the deposit notice — never
//     silently.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run 'PlatformFee' -v -count=1

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// ── 1. Arithmetic ──────────────────────────────────────────────────────────

// The rate as shipped. Pinned against the config rather than recomputed from
// it, so a change to the number fails here loudly.
func TestPlatformFeeIsTwentyPercentOfService(t *testing.T) {
	if Billing.PlatformFeeBps != 2000 {
		t.Fatalf("PlatformFeeBps = %d, want 2000 (20%%)", Billing.PlatformFeeBps)
	}
	if got := platformFeePercentLabel(); got != "20%" {
		t.Errorf("label = %q, want 20%%", got)
	}
}

// Fee on service only; the reimbursement passes through whole.
func TestPlatformFeeOnServiceOnlyReimbursementUntouched(t *testing.T) {
	// $12.00 base + 30 min × $0.50 = $27.00 service; $12.40 receipt.
	pay := supporterPayFor(2700, 1240)
	if pay.FeeCents != 540 {
		t.Errorf("fee = %s, want $5.40 (20%% of $27.00)", formatCentsUSD(pay.FeeCents))
	}
	if pay.ServiceNetCents() != 2160 {
		t.Errorf("service after fee = %s, want $21.60", formatCentsUSD(pay.ServiceNetCents()))
	}
	if pay.ReimbursementCents != 1240 {
		t.Errorf("reimbursement = %s, want $12.40 untouched", formatCentsUSD(pay.ReimbursementCents))
	}
	if pay.NetCents() != 3400 {
		t.Errorf("net = %s, want $34.00 ($21.60 + $12.40)", formatCentsUSD(pay.NetCents()))
	}

	// A reimbursement-only settlement (a cancelled shopping run paid for the
	// receipt and nothing else) is commissioned at zero.
	if pay := supporterPayFor(0, 1240); pay.FeeCents != 0 || pay.NetCents() != 1240 {
		t.Errorf("reimbursement-only pay = %+v, want fee 0 and net $12.40", pay)
	}
	// Approved time extensions are service: they arrive inside the time cost
	// and are commissioned like the rest of it. 60 billable min = $30 + $12.
	if pay := supporterPayFor(4200, 0); pay.FeeCents != 840 {
		t.Errorf("fee on $42.00 of service incl. an extension = %s, want $8.40", formatCentsUSD(pay.FeeCents))
	}
}

// Round half up, to the cent, once per task. The platform absorbs whatever a
// per-transfer rounding would have taken: the fee is computed on the whole
// service figure and stamped on one row, never re-rounded per row.
func TestPlatformFeeRounding(t *testing.T) {
	cases := []struct{ service, fee int }{
		{1200, 240}, // exact
		{1201, 240}, // 240.2 → down
		{1202, 240}, // 240.4 → down
		{1203, 241}, // 240.6 → up
		{1204, 241}, // 240.8 → up
		{1, 0},      // 0.2 → 0: a one-cent service is not commissioned
		{3, 1},      // 0.6 → 1
		{0, 0},
		{-500, 0}, // clamped
	}
	for _, tc := range cases {
		if got := platformFeeCents(tc.service); got != tc.fee {
			t.Errorf("fee(%d) = %d, want %d", tc.service, got, tc.fee)
		}
	}

	// The half-up rule itself, at a rate where .5 can occur: 250bp of $49.50
	// is $1.2375 → $1.24; 1000bp of 5¢ is exactly 0.5¢ → 1¢.
	withPlatformFeeBps(t, 250)
	if got := platformFeeCents(4950); got != 124 {
		t.Errorf("250bp of $49.50 = %d, want 124 (half up)", got)
	}
	if got := platformFeePercentLabel(); got != "2.5%" {
		t.Errorf("label = %q, want 2.5%%", got)
	}
	withPlatformFeeBps(t, 1000)
	if got := platformFeeCents(5); got != 1 {
		t.Errorf("1000bp of 5¢ = %d, want 1 (half up)", got)
	}
	withPlatformFeeBps(t, 0)
	if got := platformFeeCents(4950); got != 0 {
		t.Errorf("fee at 0bp = %d", got)
	}

	// Across two transfers the fee is still the task's single rounded figure
	// — not the sum of two roundings, which for $12.03 split $10.00/$2.03
	// would be 200 + 41 = 241 either way here but 200 + 40 = 240 at a rate
	// where the parts round down and the whole rounds up.
	withPlatformFeeBps(t, 2000)
	splits := settlementPayouts(supporterPayFor(1203, 0), 1000, 203, 0)
	fee := 0
	for _, s := range splits {
		fee += s.FeeCents
	}
	if fee != 241 {
		t.Errorf("fee across transfers = %d, want the task's 241", fee)
	}
}

// How the fee lands on the transfers, at the shipped rate.
func TestPlatformFeeOnSettlementSplits(t *testing.T) {
	withPlatformFeeBps(t, 2000)
	cases := []struct {
		name                                   string
		service, reimb                         int
		mainCaptured, balanceCaptured, subsidy int
		want                                   []payoutSplit
	}{
		// The ordinary shopping task, inside the hold: one transfer, and the
		// row reads "service $24.50, fee $4.90, reimbursement $12.40" = $32.00.
		{"inside the hold", 2450, 1240, 5000, 0, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 2450, FeeCents: 490, ReimbursementCents: 1240}}},
		// A task that overran its hold by less than the fee no longer needs
		// the balance charge's transfer at all: the hold covers the NET.
		{"overran, but the net fits the hold", 6000, 0, 4950, 1050, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 6000, FeeCents: 1200}}},
		// Two transfers. Reimbursement rides first (it is the supporter's own
		// cash), the fee sits on the first row that carries service, and the
		// balance row is plain service. Amounts: 4500 + 1700 = 6200 = net.
		{"overran, balance collected", 4000, 3000, 4500, 2500, 0,
			[]payoutSplit{
				{Source: payoutSourceHold, ServiceCents: 2300, FeeCents: 800, ReimbursementCents: 3000},
				{Source: payoutSourceBalance, ServiceCents: 1700},
			}},
		// Balance charge failed. The reimbursement is whole; what is short is
		// earnings, and the shortfall is the NET gap — the fee was never the
		// supporter's to be short of.
		{"balance failed: reimbursement first, service short", 2000, 3000, 3500, 0, 0,
			[]payoutSplit{{Source: payoutSourceHold, ServiceCents: 900, FeeCents: 400, ReimbursementCents: 3000, ShortfallCents: 1100}}},
		// PROMO. $24.50 of service, a $10 code: the requester paid $14.50.
		// The supporter's 80% is of the UNDISCOUNTED $24.50 = $19.60, so the
		// platform's subsidy is $5.10 — the discount less the fee it would
		// otherwise have kept — not the whole $10.
		{"promo: 80% of the undiscounted service", 2450, 0, 1450, 0, 1000,
			[]payoutSplit{
				{Source: payoutSourceHold, ServiceCents: 1940, FeeCents: 490},
				{Source: payoutSourcePromo, ServiceCents: 510},
			}},
		// A promo that covered the whole estimate: nothing held, the rest was
		// a balance charge, and the subsidy makes up the net.
		{"promo: zero hold", 2450, 0, 0, 500, 1950,
			[]payoutSplit{
				{Source: payoutSourceBalance, ServiceCents: 990, FeeCents: 490},
				{Source: payoutSourcePromo, ServiceCents: 1460},
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pay := supporterPayFor(tc.service, tc.reimb)
			got := settlementPayouts(pay, tc.mainCaptured, tc.balanceCaptured, tc.subsidy)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			var service, fee, reimb, paid, short int
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("split %d = %+v, want %+v", i, got[i], tc.want[i])
				}
				service += got[i].ServiceCents
				fee += got[i].FeeCents
				reimb += got[i].ReimbursementCents
				paid += got[i].AmountCents()
				short += got[i].ShortfallCents
			}
			// The rows reconcile to the task. Fully collected: gross service,
			// the task's one fee, the whole reimbursement. Short: the rows
			// record what was PAID, so the uncollected net service is in the
			// shortfall instead — and the fee is still the task's whole fee,
			// on the row that carried the service that was collected.
			if short == 0 {
				if service != pay.ServiceCents || fee != pay.FeeCents || reimb != pay.ReimbursementCents {
					t.Errorf("rows sum to service %d fee %d reimb %d, task is %+v", service, fee, reimb, pay)
				}
			} else if fee != pay.FeeCents || service-fee+reimb+short != pay.NetCents() {
				t.Errorf("short rows: service %d fee %d reimb %d short %d, task is %+v", service, fee, reimb, short, pay)
			}
			if paid+short != pay.NetCents() {
				t.Errorf("paid %d + short %d != net %d", paid, short, pay.NetCents())
			}
			if paid > pay.NetCents() {
				t.Errorf("paid %d on a net of %d", paid, pay.NetCents())
			}
			if funded := tc.mainCaptured + tc.balanceCaptured + tc.subsidy; paid > funded {
				t.Errorf("paid %d out of %d funded", paid, funded)
			}
			// Every row's own numbers add up to what it sends.
			for _, s := range got {
				if s.AmountCents() != s.ServiceCents-s.FeeCents+s.ReimbursementCents {
					t.Errorf("row %+v does not reconcile", s)
				}
			}
		})
	}
}

// The sentence behind a net figure. Worded once (payoutBreakdownLine) for the
// deposit push, the completion email and the settlement, so a deduction is
// never silent and never described three ways.
func TestPlatformFeeBreakdownLine(t *testing.T) {
	withPlatformFeeBps(t, 2000)
	cases := []struct {
		service, fee, reimb int
		want                string
	}{
		{2450, 490, 1240, "$19.60 service (after the 20% platform fee) + $12.40 reimbursement"},
		{2450, 490, 0, "$19.60 service (after the 20% platform fee)"},
		{0, 0, 1240, "$12.40 reimbursement"},
		{2450, 0, 0, "$24.50 service"}, // a pre-fee row: no fee to mention
		{0, 0, 0, ""},
	}
	for _, tc := range cases {
		if got := payoutBreakdownLine(tc.service, tc.fee, tc.reimb); got != tc.want {
			t.Errorf("line(%d,%d,%d) = %q, want %q", tc.service, tc.fee, tc.reimb, got, tc.want)
		}
	}
}

// ── 2. Rows ────────────────────────────────────────────────────────────────

// The breakdown is on the row, and the database refuses one that does not
// add up.
func TestPlatformFeeBreakdownPersistedAndReconciled(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	ctx := context.Background()

	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 5000, "ch_fee_row")
	p, err := insertPayout(ctx, payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: paymentID,
		AmountCents: 3200, ServiceCents: 2450, FeeCents: 490, ReimbursementCents: 1240,
	}, 3200)
	if err != nil {
		t.Fatalf("payout: %v", err)
	}
	var service, fee, reimb, bps, amount int
	if err := db.QueryRow(ctx, `
		select service_cents, fee_cents, reimbursement_cents, fee_bps, amount_cents
		  from public.payouts where id = $1::uuid`, p.ID,
	).Scan(&service, &fee, &reimb, &bps, &amount); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if service != 2450 || fee != 490 || reimb != 1240 || bps != Billing.PlatformFeeBps || amount != 3200 {
		t.Errorf("row = service %d fee %d reimb %d bps %d amount %d", service, fee, reimb, bps, amount)
	}

	// A caller whose numbers do not add up is refused before the row exists.
	other := seedCapturedPayment(t, w.taskID, w.requesterID, 1000, "ch_fee_bad")
	if _, err := insertPayout(ctx, payoutInput{
		TaskID: w.taskID, SupporterID: w.supporterID, PaymentID: other,
		AmountCents: 1000, ServiceCents: 1000, FeeCents: 200,
	}, 1000); err == nil {
		t.Error("a payout whose breakdown does not reconcile was written")
	}
	// And the database itself refuses a partial breakdown or a bad sum, so
	// no other writer can produce an unauditable row either.
	if _, err := db.Exec(ctx, `
		insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status, funding, service_cents)
		values ($1::uuid, $2::uuid, $3::uuid, 1000, 'pending', 'charge', 1000)`,
		w.taskID, w.supporterID, other); err == nil || !strings.Contains(err.Error(), "payouts_breakdown_reconciles") {
		t.Errorf("partial breakdown accepted: %v", err)
	}
	if _, err := db.Exec(ctx, `
		insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status, funding,
		                            service_cents, fee_cents, reimbursement_cents, fee_bps)
		values ($1::uuid, $2::uuid, $3::uuid, 1000, 'pending', 'charge', 1000, 200, 0, 2000)`,
		w.taskID, w.supporterID, other); err == nil || !strings.Contains(err.Error(), "payouts_breakdown_reconciles") {
		t.Errorf("non-reconciling breakdown accepted: %v", err)
	}
}

// Rows paid before the fee existed carry no breakdown and are read exactly as
// they were: the amount is the amount, the time is the payment's time cost,
// and no fee is shown or implied.
func TestPlatformFeeExistingPaidRowsUnaffected(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "completed")
	linkConnectAccount(t, w.supporterID, "acct_legacy")
	ctx := context.Background()

	paymentID := seedCapturedPayment(t, w.taskID, w.requesterID, 3940, "ch_legacy")
	mustExec(t, `update public.payments set time_cost_cents=2700, shopping_receipt_cents=1240 where id=$1::uuid`, paymentID)
	// The pre-fee shape: amount only, every breakdown column NULL.
	var legacyID string
	if err := db.QueryRow(ctx, `
		insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status, funding,
		                            attempt_count, stripe_transfer_id)
		values ($1::uuid, $2::uuid, $3::uuid, 3940, 'paid', 'charge', 1, 'tr_legacy')
		returning id::text`, w.taskID, w.supporterID, paymentID).Scan(&legacyID); err != nil {
		t.Fatalf("seed legacy payout: %v", err)
	}

	code, rec := callTaskHandler(t, earningsHandler, http.MethodGet, "/payments/earnings",
		"", w.supporterID, oldSupporterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("earnings: %d (%s)", code, rec.Body.String())
	}
	body := decodeFirstJSON(t, rec)
	transfers, _ := body["transfers"].([]any)
	if len(transfers) != 1 {
		t.Fatalf("transfers = %v", body["transfers"])
	}
	row := transfers[0].(map[string]any)
	if num(row["amount_cents"]) != 3940 || num(row["time_cents"]) != 2700 || num(row["receipt_cents"]) != 1240 {
		t.Errorf("legacy row = %v, want $39.40 = $27.00 time + $12.40 receipt, as paid", row)
	}
	if num(row["platform_fee_cents"]) != 0 || num(row["service_gross_cents"]) != 0 {
		t.Errorf("a pre-fee row reports a fee: %v", row)
	}
	if num(body["lifetime_earned_cents"]) != 3940 {
		t.Errorf("lifetime = %v, want $39.40", body["lifetime_earned_cents"])
	}
	// The explainer's rate rides on the envelope.
	if num(body["platform_fee_bps"]) != Billing.PlatformFeeBps {
		t.Errorf("platform_fee_bps = %v", body["platform_fee_bps"])
	}

	// The row still has no breakdown — nothing backfilled or reinterpreted it.
	var service *int
	if err := db.QueryRow(ctx, `select service_cents from public.payouts where id=$1::uuid`, legacyID).Scan(&service); err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if service != nil {
		t.Errorf("legacy row gained a breakdown: %d", *service)
	}
}

// The whole path at 20%: a shopping task completes, the requester is charged
// the full undiscounted price, the supporter is paid 80% of the service plus
// the whole receipt, the row says so, and so does every message they get.
func TestPlatformFeeEndToEndSettlement(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_fee")
	giveCard(t, w.requesterID)

	// $12.00 base + 25 billable min × $0.50 = $24.50 service; a $30 budget
	// with a $12.40 receipt.
	seedWorklog(t, w.taskID, 40, false)
	mustExec(t, `update public.tasks set shopping_budget_approved_cents=3000 where id=$1::uuid`, w.taskID)
	paymentID := seedPayment(t, w.taskID, w.requesterID, "pi_fee_hold", paymentStatusAuthorized)
	mustExec(t, `update public.payments set authorized_cents=4950 where id=$1::uuid`, paymentID)

	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://x/done.jpg",
		"receipt_amount_cents": 1240,
		"receipt_photo_url":    "https://x/receipt.jpg",
	}, http.StatusOK)

	// The REQUESTER pays the undiscounted $36.90: the fee is not a repricing.
	if len(f.captures) != 1 || f.captures[0] != 3690 {
		t.Fatalf("captured %v, want [3690]", f.captures)
	}
	// The SUPPORTER receives $19.60 + $12.40 = $32.00, in one transfer.
	if len(f.transfers) != 1 || f.transfers[0].Cents != 3200 {
		t.Fatalf("transferred %+v, want one transfer of $32.00", f.transfers)
	}
	rows := payoutRows(t, w.taskID)
	if len(rows) != 1 || rows[0].Cents != 3200 || rows[0].Status != payoutStatusPaid {
		t.Fatalf("payout rows = %+v", rows)
	}
	var service, fee, reimb int
	if err := db.QueryRow(context.Background(), `
		select service_cents, fee_cents, reimbursement_cents from public.payouts where task_id=$1::uuid`,
		w.taskID).Scan(&service, &fee, &reimb); err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	if service != 2450 || fee != 490 || reimb != 1240 {
		t.Errorf("row breakdown = service %d fee %d reimb %d, want 2450/490/1240", service, fee, reimb)
	}

	// The supporter's settlement: itemized, net, and reconciling.
	s := getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)["settlement"].(map[string]any)
	earned, ok := s["earned"].(map[string]any)
	if !ok {
		t.Fatalf("no earned block: %v", s)
	}
	if num(earned["service_gross_cents"]) != 2450 || num(earned["platform_fee_cents"]) != 490 ||
		num(earned["time_cents"]) != 1960 || num(earned["reimbursement_cents"]) != 1240 ||
		num(earned["total_cents"]) != 3200 || num(earned["platform_fee_bps"]) != 2000 {
		t.Errorf("earned = %v", earned)
	}
	if earned["payout_status"] != payoutStatusPaid {
		t.Errorf("payout_status = %v", earned["payout_status"])
	}
	// The requester's copy: their price, no fee keys — the commission is not
	// their business and not their charge.
	r := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["settlement"].(map[string]any)
	if num(r["total_cents"]) != 3690 || num(r["captured_cents"]) != 3690 {
		t.Errorf("requester settlement = %v, want $36.90 charged", r)
	}
	if _, leaked := r["earned"]; leaked {
		t.Error("the requester can see the supporter's pay packet")
	}
	// The requester's receipt is the price, and says nothing of the fee.
	if body := notificationBody(t, w.requesterID, "RECEIPT"); strings.Contains(body, "platform fee") || !strings.Contains(body, "Charged $36.90") {
		t.Errorf("requester receipt = %q", body)
	}

	// Never a silent deduction: the deposit notice carries the net figure and
	// the breakdown, and so does the completion notice.
	paid := notificationBody(t, w.supporterID, "PAYOUT_SENT")
	if !strings.Contains(paid, "$19.60 service (after the 20% platform fee) + $12.40 reimbursement") {
		t.Errorf("PAYOUT_SENT body = %q", paid)
	}
	done := notificationBody(t, w.supporterID, "COMPLETED_SUPPORTER")
	if !strings.Contains(done, "You earned $32.00") || !strings.Contains(done, "after the 20% platform fee") {
		t.Errorf("COMPLETED_SUPPORTER body = %q", done)
	}

	// And the Earnings row reads the same way.
	code, rec := callTaskHandler(t, earningsHandler, http.MethodGet, "/payments/earnings",
		"", w.supporterID, oldSupporterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("earnings: %d", code)
	}
	transfers := decodeFirstJSON(t, rec)["transfers"].([]any)
	row := transfers[0].(map[string]any)
	if num(row["amount_cents"]) != 3200 || num(row["time_cents"]) != 1960 || num(row["receipt_cents"]) != 1240 ||
		num(row["service_gross_cents"]) != 2450 || num(row["platform_fee_cents"]) != 490 {
		t.Errorf("earnings row = %v", row)
	}
}
