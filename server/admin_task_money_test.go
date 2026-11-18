package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The ops money view against a real Postgres (the payments fixture
// carries tasks, payments and payouts as the migrations define them).

func callTaskMoney(t *testing.T, taskID, uid, email string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/tasks/"+taskID+"/money", nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)
	requireOpsAdmin()(c)
	if !c.IsAborted() {
		adminTaskMoney(c)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestAdminTaskMoneyShowsReceiptAndTransfer(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedWorld(t)
	ctx := context.Background()
	taskID := seedReassignTask(t, "completed", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	if _, err := db.Exec(ctx, `
		update public.tasks
		set shopping_budget_approved_cents = 1500, receipt_amount_cents = 1240,
		    receipt_photo_url = 'https://example.com/receipt.jpg'
		where id = $1::uuid`, taskID); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	var paymentID string
	if err := db.QueryRow(ctx, `
		insert into public.payments (task_id, requester_id, kind, status, authorized_cents, captured_cents)
		values ($1::uuid, $2::uuid, 'task_payment', 'captured', 4000, 3190)
		returning id::text`, taskID, w.requesterID).Scan(&paymentID); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	if _, err := db.Exec(ctx, `
		insert into public.payouts (task_id, supporter_id, payment_id, amount_cents, status,
		                            service_cents, fee_cents, reimbursement_cents, fee_bps)
		values ($1::uuid, $2::uuid, $3::uuid, 2605, 'paid', 1950, 585, 1240, 3000)`,
		taskID, w.oldID, paymentID); err != nil {
		t.Fatalf("seed payout: %v", err)
	}

	code, out := callTaskMoney(t, taskID, w.adminID, adminEmail)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%v)", code, out)
	}
	if out["receipt_cents"] != float64(1240) || out["budget_cents"] != float64(1500) {
		t.Errorf("receipt/budget = %v/%v, want 1240/1500", out["receipt_cents"], out["budget_cents"])
	}
	// Storage is unconfigured in tests, so the link comes back as stored —
	// what matters is that the photo reaches the panel at all.
	if out["receipt_photo_url"] != "https://example.com/receipt.jpg" {
		t.Errorf("receipt_photo_url = %v", out["receipt_photo_url"])
	}
	// Sent to Stripe, bank leg not confirmed: the supporter's "on its way".
	if out["payout_state"] != "on_its_way" {
		t.Errorf("payout_state = %v, want on_its_way", out["payout_state"])
	}
	payouts, _ := out["payouts"].([]any)
	if len(payouts) != 1 {
		t.Fatalf("payouts = %v", out["payouts"])
	}
	p := payouts[0].(map[string]any)
	if p["amount_cents"] != float64(2605) || p["reimbursement_cents"] != float64(1240) || p["fee_cents"] != float64(585) {
		t.Errorf("payout breakdown wrong: %v", p)
	}
	payments, _ := out["payments"].([]any)
	if len(payments) != 1 || payments[0].(map[string]any)["captured_cents"] != float64(3190) {
		t.Errorf("payments = %v", out["payments"])
	}

	// The bank leg lands.
	if _, err := db.Exec(ctx, `update public.payouts set bank_paid_at = now() where task_id = $1::uuid`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, out = callTaskMoney(t, taskID, w.adminID, adminEmail); out["payout_state"] != "paid_out" {
		t.Errorf("payout_state after bank_paid_at = %v, want paid_out", out["payout_state"])
	}
}

func TestAdminTaskMoneyWithNothingYet(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	code, out := callTaskMoney(t, taskID, w.adminID, adminEmail)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%v)", code, out)
	}
	if out["receipt_cents"] != nil || out["receipt_photo_url"] != "" || out["payout_state"] != "none" {
		t.Errorf("empty task should report nothing: %v", out)
	}
}

func TestAdminTaskMoneyIsAdminOnly(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	// The supporter on the task is still not ops.
	if code, _ := callTaskMoney(t, taskID, w.oldID, oldSupporterEmail); code != http.StatusForbidden {
		t.Errorf("non-admin got %d, want 403", code)
	}
}

// One failed transfer is never hidden behind a paid one.
func TestPayoutOverallStateTakesTheWorst(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "none"},
		{[]string{"paid_out"}, "paid_out"},
		{[]string{"paid_out", "on_its_way"}, "on_its_way"},
		{[]string{"on_its_way", "pending"}, "pending"},
		{[]string{"paid_out", "failed", "on_its_way"}, "failed"},
	}
	for _, c := range cases {
		if got := payoutOverallState(c.in); got != c.want {
			t.Errorf("payoutOverallState(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
