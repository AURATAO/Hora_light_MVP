package main

// The billing restructure: hold == what you were shown, overages collected at
// completion, evening rate resolved once.
//
// Three properties are worth more than the rest of this file put together:
//
//   1. The hold equals the quote. Covered next door in
//      TestPreAuthHoldIsExactlyTheEstimatePlusBudget.
//   2. The rate is decided ONCE. A price is a term of an agreement; a task
//      must not re-price itself because somebody opened a screen at 21:05.
//   3. An uncollectable completion blocks the requester and nobody else. The
//      supporter is paid and unaffected — the platform carries the float.
//
//	docker run -d --rm --name hora-p2b-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Restructure -v

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ── The rate ───────────────────────────────────────────────────────────────

// The boundary, to the minute, in the timezone that decides it.
func TestRestructureRateBoundary(t *testing.T) {
	ny, err := time.LoadLocation(Billing.SurgeTimezone)
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}

	cases := []struct {
		name  string
		at    time.Time
		want  int
		surge bool
	}{
		{"08:00 — standard", time.Date(2026, 9, 15, 8, 0, 0, 0, ny), Billing.PerMinuteRateCents, false},
		{"20:00 — standard", time.Date(2026, 9, 15, 20, 0, 0, 0, ny), Billing.PerMinuteRateCents, false},
		// The two that matter. 20:59 is the last standard minute; a task
		// starting then bills at $0.50 for its whole run, including the part
		// after 21:00 — see the immutability test below.
		{"20:59 — still standard", time.Date(2026, 9, 15, 20, 59, 59, 0, ny), Billing.PerMinuteRateCents, false},
		{"21:00 — evening", time.Date(2026, 9, 15, 21, 0, 0, 0, ny), Billing.SurgeRateCentsPerMin, true},
		{"23:59 — evening", time.Date(2026, 9, 15, 23, 59, 0, 0, ny), Billing.SurgeRateCentsPerMin, true},
		{"00:30 — back to standard", time.Date(2026, 9, 16, 0, 30, 0, 0, ny), Billing.PerMinuteRateCents, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRateCentsPerMin(tc.at); got != tc.want {
				t.Errorf("rate = %d, want %d", got, tc.want)
			}
			if got := isSurgeRate(tc.want); got != tc.surge {
				t.Errorf("isSurgeRate = %v, want %v", got, tc.surge)
			}
		})
	}

	// The hour is read in NEW YORK, not in whatever zone the server or the
	// caller happens to be in. 21:00 New York is 01:00 UTC the next day; a
	// naive UTC read would call it standard.
	utcEvening := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	if got := resolveRateCentsPerMin(utcEvening); got != Billing.SurgeRateCentsPerMin {
		t.Errorf("01:00 UTC (= 21:00 New York) resolved to %d — the zone is being ignored", got)
	}
}

// A zero or missing stored rate bills at the standard rate, never at zero.
// Rows written before the column existed read as 0, and a pricing path that
// can return "free" is worse than one that can return "cheap".
func TestRestructureMissingRateFallsBackToStandard(t *testing.T) {
	for _, stored := range []int{0, -1} {
		if got := normalizeRate(stored); got != Billing.PerMinuteRateCents {
			t.Errorf("stored rate %d normalized to %d, want %d", stored, got, Billing.PerMinuteRateCents)
		}
		if got := timeCostCents(60, stored); got != billableMinutes(60)*Billing.PerMinuteRateCents {
			t.Errorf("stored rate %d priced 60 min at %s", stored, formatCentsUSD(got))
		}
	}
}

// The rate is a stored term of the task, not a live lookup: a 20:50 task keeps
// billing at $0.50 however long it runs, and an evening task keeps billing at
// $1.00 even when settled the next morning.
func TestRestructureStoredRateIsImmutable(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	for _, tc := range []struct{ stored, wantCost int }{
		// 60 logged minutes, 45 of them billable after the included block.
		{Billing.PerMinuteRateCents, 1200 + 45*50},
		{Billing.SurgeRateCentsPerMin, 1200 + 45*100},
	} {
		if _, err := db.Exec(context.Background(),
			`update public.tasks set rate_cents_per_min = $2, estimated_minutes = 120 where id = $1::uuid`,
			w.taskID, tc.stored); err != nil {
			t.Fatalf("store rate: %v", err)
		}
		if got := taskRateCentsPerMin(context.Background(), w.taskID); got != tc.stored {
			t.Fatalf("read back %d, want %d", got, tc.stored)
		}
		// calcTaskCostCents must use the STORED rate — not one resolved from
		// the clock at the moment of settlement.
		if got := calcTaskCostCents(context.Background(), w.taskID, 60); got != tc.wantCost {
			t.Errorf("stored %d: settled at %s, want %s",
				tc.stored, formatCentsUSD(got), formatCentsUSD(tc.wantCost))
		}
	}
}

// The quote a client renders carries the rate and says whether it is the
// evening one, so the form can explain itself without knowing the schedule.
func TestRestructureEstimateCarriesTheRate(t *testing.T) {
	ny, err := time.LoadLocation(Billing.SurgeTimezone)
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	evening := time.Date(2026, 9, 15, 21, 30, 0, 0, ny).Format(time.RFC3339)
	daytime := time.Date(2026, 9, 15, 14, 0, 0, 0, ny).Format(time.RFC3339)

	_, out := callEstimate(t, fmt.Sprintf(
		`{"category":"delivery","estimated_minutes":45,"scheduled_at":%q}`, evening))
	if out["per_minute_rate_cents"] != float64(Billing.SurgeRateCentsPerMin) {
		t.Errorf("evening quote rate = %v, want %d", out["per_minute_rate_cents"], Billing.SurgeRateCentsPerMin)
	}
	if out["surge_rate"] != true {
		t.Errorf("evening quote does not flag surge: %v", out["surge_rate"])
	}
	// 30 billable min x $1.00 + $12.00 base
	if out["total_cents"] != float64(1200+30*100) {
		t.Errorf("evening total = %v, want %d", out["total_cents"], 1200+30*100)
	}

	_, out = callEstimate(t, fmt.Sprintf(
		`{"category":"delivery","estimated_minutes":45,"scheduled_at":%q}`, daytime))
	if out["surge_rate"] != false {
		t.Errorf("a 14:00 task was quoted at the evening rate: %v", out)
	}
}

// ── No cap, and a task posted at a big budget holds all of it ─────────────

func TestRestructureLargeBudgetPostsAndHoldsInFull(t *testing.T) {
	setupCreatedViaDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	// Well past the old $30 cap, and past the warning threshold too — the
	// warning is the form's job and refuses nothing.
	code, body := postTaskJSON(t, `{"title":"Big shop","category":"grocery",
		"estimated_minutes":30,"is_immediate":true,"prepay_amount_cents":120000}`)
	if code != 201 {
		t.Fatalf("expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)

	var prepay, approved int
	if err := db.QueryRow(context.Background(),
		`select prepay_amount_cents, shopping_budget_approved_cents from public.tasks where id=$1::uuid`,
		id).Scan(&prepay, &approved); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if prepay != 120000 || approved != 120000 {
		t.Errorf("budget stored as %d/%d, want 120000 — it was clamped somewhere",
			prepay, approved)
	}
}

// ── Outstanding balance ────────────────────────────────────────────────────

// The wall that makes the balance notifications worth reading.
func TestRestructureOutstandingBalanceBlocksPosting(t *testing.T) {
	setupCreatedViaDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "1")

	// A completed task whose overage could not be charged.
	code, body := func() (int, map[string]any) {
		t.Setenv("PAYMENTS_ENFORCED", "")
		return postTaskJSON(t, `{"title":"Earlier task","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	}()
	if code != 201 {
		t.Fatalf("seed post: %d (%v)", code, body)
	}
	taskID, _ := body["id"].(string)
	requesterID, _ := body["requester_id"].(string)
	seedBalanceDue(t, taskID, requesterID, 1250)

	// Nothing owed by anyone else.
	if owed := outstandingBalanceFor(context.Background(), requesterID); owed == nil || owed.TotalCents != 1250 {
		t.Fatalf("balance not seen: %+v", owed)
	}

	t.Setenv("PAYMENTS_ENFORCED", "1")
	code, body = postTaskJSON(t, `{"title":"Next task","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != http.StatusForbidden {
		t.Fatalf("posting with a balance owed: %d (%v), want 403", code, body)
	}
	if body["error"] != "outstanding_balance" {
		t.Errorf("error = %v, want outstanding_balance", body["error"])
	}
	// The message names the amount and the task, because "you have a balance"
	// with no number is exactly the vagueness this whole area is fixing.
	msg, _ := body["message"].(string)
	if !containsAll(msg, "$12.50", "Earlier task") {
		t.Errorf("message does not name the amount and the task: %q", msg)
	}

	// Settling clears it.
	if _, err := db.Exec(context.Background(),
		`update public.payments set status='captured' where status='balance_due'`); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if owed := outstandingBalanceFor(context.Background(), requesterID); owed != nil {
		t.Errorf("balance survives settlement: %+v", owed)
	}
	code, _ = postTaskJSON(t, `{"title":"Now allowed","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code == http.StatusForbidden {
		t.Error("posting still blocked after the balance was settled")
	}
}

// A balance belongs to the requester who owes it, and to nobody else — above
// all not to the supporter, whose payout is deliberately not gated on it.
func TestRestructureBalanceIsPerRequester(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedBalanceDue(t, w.taskID, w.requesterID, 900)

	if owed := outstandingBalanceFor(context.Background(), w.requesterID); owed == nil {
		t.Fatal("the requester who owes it cannot see it")
	}
	if owed := outstandingBalanceFor(context.Background(), w.supporterID); owed != nil {
		t.Errorf("the SUPPORTER is carrying the requester's balance: %+v", owed)
	}
	if owed := outstandingBalanceFor(context.Background(), ""); owed != nil {
		t.Errorf("an empty uid owes something: %+v", owed)
	}
}

// ── Approvals authorize nothing ────────────────────────────────────────────

// An approval raises a ceiling. It does not touch money — no incremental
// authorization, no second hold, nothing inside the requester's one tap. All
// of it is collected at completion.
func TestRestructureApprovalsCreateNoPayments(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	before := countPayments(t, w.taskID)

	_, body := requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents": 800, "fallback": "skip_item"}`)
	extID, _ := body["id"].(string)
	code, body := resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("approve: %d (%v)", code, body)
	}
	if got := num(body["approved_budget_cents"]); got != 2800 {
		t.Errorf("ceiling = %s, want $28.00", formatCentsUSD(got))
	}
	// The response no longer carries a hold adjustment, because there isn't one.
	if _, present := body["hold"]; present {
		t.Errorf("approval reported a hold adjustment: %v", body["hold"])
	}
	if after := countPayments(t, w.taskID); after != before {
		t.Errorf("approving created %d payments row(s)", after-before)
	}
	// The resolution is announced on a background goroutine; wait for it
	// rather than letting it outlive this test's database handle.
	waitFor(t, func() bool {
		return countNotifications(t, w.supporterID, "EXTENSION_RESOLVED") >= 1
	}, "the budget resolution was announced")

	// Same for time.
	requestTimeAsBody(t, w.taskID, w.supporterID, 30)
	var timeExtID string
	_ = db.QueryRow(context.Background(),
		`select id::text from public.extension_requests where task_id=$1::uuid and kind='time' and status='pending'`,
		w.taskID).Scan(&timeExtID)
	if code, body := resolveExtensionAs(t, approveExtension, w.taskID, timeExtID, w.requesterID); code != http.StatusOK {
		t.Fatalf("approve time: %d (%v)", code, body)
	}
	if after := countPayments(t, w.taskID); after != before {
		t.Errorf("approving a time extension created %d payments row(s)", after-before)
	}
	waitFor(t, func() bool {
		return countNotifications(t, w.supporterID, "EXTENSION_RESOLVED") >= 2
	}, "the time resolution was announced")
}

// ── Helpers ────────────────────────────────────────────────────────────────

func seedBalanceDue(t *testing.T, taskID, requesterID string, cents int) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, $5)
	`, taskID, requesterID, paymentKindCompletionBalance, paymentStatusBalanceDue, cents); err != nil {
		t.Fatalf("seed balance_due: %v", err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
