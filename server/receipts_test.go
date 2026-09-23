package main

// The two payment notifications: the requester's receipt and the supporter's
// deposit push.
//
// The property under test for the receipt is not the wording but the WHEN: it
// is sent from the settlement path that captures, so a charge cannot happen
// without one, exactly one is sent per charge, and its numbers are the
// settlement's own. Stripe is the fixture from promo_test.go.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run 'Receipt|Deposit' -v -count=1

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func notificationTitle(t *testing.T, userID, ntype string) string {
	t.Helper()
	var title string
	if err := db.QueryRow(context.Background(),
		`select title from public.notifications where user_id=$1::uuid and type=$2 order by created_at desc limit 1`,
		userID, ntype).Scan(&title); err != nil {
		t.Fatalf("read notification title: %v", err)
	}
	return title
}

// One completion, one charge, one receipt — whose numbers are the settlement's.
func TestReceiptSentOncePerChargeAndMatchesSettlement(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	giveCard(t, w.requesterID)
	seedWorklog(t, w.taskID, 40, false)                                                      // $12.00 + 25 × $0.50 = $24.50
	paymentID := seedPayment(t, w.taskID, w.requesterID, "pi_hold", paymentStatusAuthorized) // $50.00 held
	setPaymentCard(t, paymentID, "mastercard", "5100")

	completeAs(t, w.taskID, w.supporterID, map[string]any{"completion_photo_url": "https://x/done.jpg"}, http.StatusOK)

	if len(f.captures) != 1 || f.captures[0] != 2450 {
		t.Fatalf("captured %v, want [2450]", f.captures)
	}
	if n := countNotifications(t, w.requesterID, "RECEIPT"); n != 1 {
		t.Fatalf("%d receipts after one charge, want exactly 1", n)
	}

	// The receipt's total is the settlement's captured total, and its lines
	// are the settlement's lines.
	s := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["settlement"].(map[string]any)
	captured := num(s["captured_cents"])
	if captured != 2450 {
		t.Fatalf("settlement captured %d", captured)
	}
	if title := notificationTitle(t, w.requesterID, "RECEIPT"); title != "Your HO:RA receipt — "+formatCentsUSD(captured) {
		t.Errorf("title = %q", title)
	}
	body := notificationBody(t, w.requesterID, "RECEIPT")
	for _, want := range []string{
		"Charged $24.50 to Mastercard ••5100",
		formatCentsUSD(Billing.BaseFeeDefaultCents) + " base",
		"25 min × $0.50",
		"$25.50 of the hold released", // $50.00 held − $24.50 taken
	} {
		if !strings.Contains(body, want) {
			t.Errorf("receipt body %q lacks %q", body, want)
		}
	}
	if strings.Contains(body, "promo") {
		t.Errorf("a receipt with no promo mentions one: %q", body)
	}
	// Both parties still get their completion notices; only the requester
	// gets a receipt.
	if n := countNotifications(t, w.supporterID, "RECEIPT"); n != 0 {
		t.Errorf("the supporter got %d receipt(s)", n)
	}

	// A second charge later — the outstanding-balance path — gets its own.
	balanceID := seedBalanceDueRowForTask(t, w.taskID, w.requesterID, 300)
	setPaymentCard(t, balanceID, "mastercard", "5100")
	out, err := runSettlePass(context.Background(), w.requesterID)
	if err != nil || out.settledCents != 300 {
		t.Fatalf("settle pass: %+v %v", out, err)
	}
	if len(f.charges) != 1 || f.charges[0] != 300 {
		t.Fatalf("balance charged %v, want [300]", f.charges)
	}
	if n := countNotifications(t, w.requesterID, "RECEIPT"); n != 2 {
		t.Fatalf("%d receipts after two charges, want exactly 2", n)
	}
	if body := notificationBody(t, w.requesterID, "RECEIPT"); !strings.Contains(body, "Charged $3.00") ||
		!strings.Contains(body, "outstanding balance") {
		t.Errorf("settle-balance receipt = %q", body)
	}
	if title := notificationTitle(t, w.requesterID, "RECEIPT"); title != "Your HO:RA receipt — $3.00" {
		t.Errorf("settle-balance title = %q", title)
	}
}

// A completion that took two charges — the hold and a balance — is one
// receipt naming both.
func TestReceiptNamesBothChargesOfOneCompletion(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	giveCard(t, w.requesterID)
	// A long task against a $50.00 hold: 200 logged min on a 200-min estimate
	// bills $12.00 + 185 × $0.50 = $104.50, so the hold is captured in full
	// and $54.50 is a balance charge.
	mustExec(t, `update public.tasks set estimated_minutes = 200 where id = $1::uuid`, w.taskID)
	seedWorklog(t, w.taskID, 200, false)
	paymentID := seedPayment(t, w.taskID, w.requesterID, "pi_hold", paymentStatusAuthorized)
	setPaymentCard(t, paymentID, "visa", "4242")

	completeAs(t, w.taskID, w.supporterID, map[string]any{"completion_photo_url": "https://x/done.jpg"}, http.StatusOK)

	if len(f.captures) != 1 || f.captures[0] != 5000 || len(f.charges) != 1 || f.charges[0] != 5450 {
		t.Fatalf("captures %v charges %v, want [5000] and [5450]", f.captures, f.charges)
	}
	if n := countNotifications(t, w.requesterID, "RECEIPT"); n != 1 {
		t.Fatalf("%d receipts for one completion with two charges, want 1", n)
	}
	body := notificationBody(t, w.requesterID, "RECEIPT")
	for _, want := range []string{"Charged $104.50", "Two charges: reserved amount $50.00 and balance $54.50"} {
		if !strings.Contains(body, want) {
			t.Errorf("receipt body %q lacks %q", body, want)
		}
	}
	if strings.Contains(body, "released") {
		t.Errorf("a fully captured hold released something: %q", body)
	}
	if title := notificationTitle(t, w.requesterID, "RECEIPT"); title != "Your HO:RA receipt — $104.50" {
		t.Errorf("title = %q", title)
	}
}

// No charge, no receipt: payments off, and a capture that failed.
func TestReceiptOnlyWhenSomethingWasCharged(t *testing.T) {
	setupStripeWebhookDB(t)

	// Payments not configured: the completion settles nothing.
	off := seedOpsWorld(t, "open")
	seedWorklog(t, off.taskID, 40, false)
	completeAs(t, off.taskID, off.supporterID, map[string]any{"completion_photo_url": "https://x/done.jpg"}, http.StatusOK)
	if n := countNotifications(t, off.requesterID, "RECEIPT"); n != 0 {
		t.Errorf("%d receipt(s) with payments off", n)
	}

	// Payments on, but the hold is in a state Capture refuses: the task
	// completes, the row goes capture_failed, ops are told — no receipt,
	// because no money moved.
	// A fresh fixture: seedOpsWorld inserts fixed emails.
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	failed := seedOpsWorld(t, "open")
	seedWorklog(t, failed.taskID, 40, false)
	seedPayment(t, failed.taskID, failed.requesterID, "pi_stuck", paymentStatusRequiresAuth)
	completeAs(t, failed.taskID, failed.supporterID, map[string]any{"completion_photo_url": "https://x/done.jpg"}, http.StatusOK)
	if len(f.captures) != 0 {
		t.Errorf("a requires_auth hold was captured: %v", f.captures)
	}
	if n := countNotifications(t, failed.requesterID, "RECEIPT"); n != 0 {
		t.Errorf("%d receipt(s) for a failed capture", n)
	}
	if s := paymentStatusOf(t, "pi_stuck"); s != paymentStatusCaptureFailed {
		t.Errorf("payment = %s, want capture_failed", s)
	}
}

// A charged cancel is a charge, and gets a receipt from the same path.
func TestReceiptForAChargedCancel(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	mustExec(t, `update public.tasks set accepted_at = now() - interval '1 hour' where id = $1::uuid`, w.taskID)
	seedWorklog(t, w.taskID, 20, false) // $12.00 + 5 × $0.50 = $14.50
	paymentID := seedPayment(t, w.taskID, w.requesterID, "pi_hold", paymentStatusAuthorized)
	setPaymentCard(t, paymentID, "visa", "4242")

	code, body := cancelAs(t, w.taskID, w.requesterID, "no longer needed")
	if code != http.StatusOK || num(body["bill_cents"]) != 1450 {
		t.Fatalf("cancel: %d %v", code, body)
	}
	if len(f.captures) != 1 || f.captures[0] != 1450 {
		t.Fatalf("captured %v", f.captures)
	}
	if n := countNotifications(t, w.requesterID, "RECEIPT"); n != 1 {
		t.Fatalf("%d receipts for a charged cancel, want 1", n)
	}
	if b := notificationBody(t, w.requesterID, "RECEIPT"); !strings.Contains(b, "Charged $14.50 to Visa ••4242") {
		t.Errorf("receipt = %q", b)
	}

	// And a free cancel is not a charge.
	setupStripeWebhookDB(t)
	free := seedOpsWorld(t, "open")
	unassign(t, free.taskID)
	seedPayment(t, free.taskID, free.requesterID, "pi_free", paymentStatusAuthorized)
	if code, _ := cancelAs(t, free.taskID, free.requesterID, "changed my mind"); code != http.StatusOK {
		t.Fatalf("free cancel: %d", code)
	}
	if n := countNotifications(t, free.requesterID, "RECEIPT"); n != 0 {
		t.Errorf("%d receipt(s) for a free cancel", n)
	}
}

// seedBalanceDueRowForTask is a completion balance the card refused, on a
// task that already exists — the state /payments/settle-balance collects.
func seedBalanceDueRowForTask(t *testing.T, taskID, requesterID string, cents int) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, $5) returning id::text
	`, taskID, requesterID, paymentKindCompletionBalance, paymentStatusBalanceDue, cents).Scan(&id); err != nil {
		t.Fatalf("seed balance_due: %v", err)
	}
	return id
}

// ── The deposit push ───────────────────────────────────────────────────────

// payout.paid stamps the rows it carried AND tells the supporter once: the
// amount the bank received, and the tasks it covered. A redelivery of the
// same event, and a second event for the same payout, both stay silent.
func TestDepositPushOncePerPayoutPaid(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	linkConnectAccount(t, w.supporterID, "acct_bank_1")

	a := seedPayoutFor(t, w, 0, 1200, payoutStatusPaid, 30)
	b := seedPayoutFor(t, w, 1, 2500, payoutStatusPaid, 20)
	c := seedPayoutFor(t, w, 2, 999, payoutStatusPaid, 10)
	setTransfer(t, a, "tr_a", "py_a")
	setTransfer(t, b, "tr_b", "py_b")
	setTransfer(t, c, "tr_c", "py_c")

	stubBankLeg(t, map[string][]string{"po_1": {"py_a", "py_b"}, "po_2": {"py_a", "py_b"}}, nil)
	event := payoutPaidEvent("acct_bank_1", "po_1", 3700)
	if code := postSignedStripeWebhook(t, event); code != http.StatusOK {
		t.Fatalf("payout.paid: %d", code)
	}

	if n := countNotifications(t, w.supporterID, "PAYOUT_DEPOSITED"); n != 1 {
		t.Fatalf("%d deposit notifications after one payout.paid, want 1", n)
	}
	if title := notificationTitle(t, w.supporterID, "PAYOUT_DEPOSITED"); title != "$37.00 has been deposited to your bank." {
		t.Errorf("title = %q", title)
	}
	body := notificationBody(t, w.supporterID, "PAYOUT_DEPOSITED")
	for _, want := range []string{`"Paid task 0"`, `"Paid task 1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q does not list %s", body, want)
		}
	}
	if strings.Contains(body, "Paid task 2") {
		t.Errorf("body names a task the payout did not carry: %q", body)
	}

	// Stripe redelivers the same event: deduped, silent.
	if code := postSignedStripeWebhook(t, event); code != http.StatusOK {
		t.Fatalf("redelivery: %d", code)
	}
	// A second payout event that carries rows already stamped: silent, because
	// it stamped nothing.
	if code := postSignedStripeWebhook(t, payoutPaidEvent("acct_bank_1", "po_2", 3700)); code != http.StatusOK {
		t.Fatalf("second payout: %d", code)
	}
	if n := countNotifications(t, w.supporterID, "PAYOUT_DEPOSITED"); n != 1 {
		t.Errorf("%d deposit notifications after a redelivery and a repeat, want still 1", n)
	}
	// The requester never hears about a supporter's bank.
	if n := countNotifications(t, w.requesterID, "PAYOUT_DEPOSITED"); n != 0 {
		t.Errorf("the requester got %d deposit notification(s)", n)
	}
}
