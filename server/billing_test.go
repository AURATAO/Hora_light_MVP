package main

// Billing engine coverage.
//
// The pure-arithmetic tests need no database and always run. The settlement
// tests (multi-session summing, cancel, the budget cap) need Postgres and skip
// without TEST_DATABASE_URL, matching the existing ops-action suite:
//
//	docker run -d --rm --name hora-billing-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Billing -v

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// ── Pure pricing ───────────────────────────────────────────────────────────

func TestBillingQuoteTable(t *testing.T) {
	cases := []struct {
		name     string
		category string
		minutes  int
		budget   int

		wantBase     int
		wantBillable int
		wantTime     int
		wantTotal    int
	}{
		{
			// Under the included block: the base fee is the whole bill. This is
			// the case the old engine got wrong in the most expensive
			// direction — it charged $12 + 10x$0.50 = $17.00 for a ten-minute
			// errand whose comments claimed the first 15 were included.
			name: "10 min is base only", category: "quick_errand", minutes: 10,
			wantBase: 1200, wantBillable: 0, wantTime: 0, wantTotal: 1200,
		},
		{
			// The boundary. 15 is inclusive — the fifteenth minute is inside
			// the base fee, not the first billable one.
			name: "15 min is base only", category: "quick_errand", minutes: 15,
			wantBase: 1200, wantBillable: 0, wantTime: 0, wantTotal: 1200,
		},
		{
			name: "16 min bills one minute", category: "quick_errand", minutes: 16,
			wantBase: 1200, wantBillable: 1, wantTime: 50, wantTotal: 1250,
		},
		{
			name: "60 min bills 45", category: "standard", minutes: 60,
			wantBase: 1200, wantBillable: 45, wantTime: 2250, wantTotal: 3450,
		},
		{
			name: "companionship base", category: "companionship", minutes: 60,
			wantBase: 2500, wantBillable: 45, wantTime: 2250, wantTotal: 4750,
		},
		{
			// Both spellings price identically; the picker submits one and Post
			// Task normalizes to the other.
			name: "companion alias prices the same", category: "companion", minutes: 60,
			wantBase: 2500, wantBillable: 45, wantTime: 2250, wantTotal: 4750,
		},
		{
			// The removed tier. Under the old schedule this was $18.00 base;
			// a >90-minute task is now priced by its minutes alone.
			name: "over 90 min no longer changes the base fee", category: "standard", minutes: 120,
			wantBase: 1200, wantBillable: 105, wantTime: 5250, wantTotal: 6450,
		},
		{
			name: "budget adds at face value", category: "standard", minutes: 30, budget: 2000,
			wantBase: 1200, wantBillable: 15, wantTime: 750, wantTotal: 3950,
		},
		{
			name: "zero minutes", category: "standard", minutes: 0,
			wantBase: 1200, wantBillable: 0, wantTime: 0, wantTotal: 1200,
		},
		{
			name: "negative minutes clamp to zero", category: "standard", minutes: -30,
			wantBase: 1200, wantBillable: 0, wantTime: 0, wantTotal: 1200,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteTask(tc.category, tc.minutes, tc.budget)
			if got.BaseFeeCents != tc.wantBase {
				t.Errorf("base fee = %d, want %d", got.BaseFeeCents, tc.wantBase)
			}
			if got.BillableMinutes != tc.wantBillable {
				t.Errorf("billable minutes = %d, want %d", got.BillableMinutes, tc.wantBillable)
			}
			if got.TimeCostCents != tc.wantTime {
				t.Errorf("time cost = %d, want %d", got.TimeCostCents, tc.wantTime)
			}
			if got.TotalCents != tc.wantTotal {
				t.Errorf("total = %d, want %d", got.TotalCents, tc.wantTotal)
			}
			if got.IncludedMinutes != 15 {
				t.Errorf("included minutes = %d, want 15", got.IncludedMinutes)
			}
		})
	}
}

// The multi-session promise, at the arithmetic level: two sessions of 10 and
// 20 minutes are one 30-minute task, so the inclusion is consumed once and 15
// minutes bill — NOT once per session (which would bill 0 + 5 = 5 minutes and
// undercharge by $5.00).
func TestBillingMultiSessionConsumesInclusionOnce(t *testing.T) {
	const summed = 10 + 20

	got := quoteTask("standard", summed, 0)
	if got.BillableMinutes != 15 {
		t.Fatalf("billable minutes = %d, want 15", got.BillableMinutes)
	}
	if got.TotalCents != 1200+750 {
		t.Fatalf("total = %d, want %d (base $12.00 + $7.50)", got.TotalCents, 1200+750)
	}

	perSession := timeCostCents(10) + timeCostCents(20)
	if perSession == got.TimeCostCents {
		t.Fatal("per-session and summed inclusion agree — the test cannot detect the bug it exists for")
	}
	if perSession != 250 {
		t.Fatalf("per-session sanity = %d, want 250", perSession)
	}
}

func TestBillingCancelSettlement(t *testing.T) {
	cases := []struct {
		name       string
		category   string
		minutes    int
		hadSession bool
		want       int
	}{
		{
			// The whole point of the hadSession flag. Nobody showed up, so
			// nothing is owed — not even the base fee.
			name:     "cancel before any clock-in owes nothing",
			category: "standard", minutes: 0, hadSession: false, want: 0,
		},
		{
			// Supporter travelled and worked 10 minutes: base fee is earned,
			// the 10 minutes are inside it.
			name:     "cancel after a short session owes the base fee",
			category: "standard", minutes: 10, hadSession: true, want: 1200,
		},
		{
			name:     "cancel after a long session bills time past the inclusion",
			category: "standard", minutes: 45, hadSession: true, want: 1200 + 1500,
		},
		{
			name:     "companionship cancel uses its own base fee",
			category: "companion", minutes: 20, hadSession: true, want: 2500 + 250,
		},
		{
			// Defensive: minutes without a session is an impossible state, but
			// the flag is what decides, so it decides here too.
			name:     "no session wins over logged minutes",
			category: "standard", minutes: 45, hadSession: false, want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cancelSettlementCents(tc.category, tc.minutes, tc.hadSession); got != tc.want {
				t.Errorf("cancelSettlementCents = %d, want %d", got, tc.want)
			}
		})
	}
}

// The shopping budget must not appear in any settlement figure. It is an
// authorization ceiling; nothing has been charged against it.
func TestBillingSettlementIgnoresShoppingBudget(t *testing.T) {
	for _, budget := range []int{0, 500, 3000} {
		if got := cancelSettlementCents("standard", 45, true); got != 2700 {
			t.Errorf("cancel settlement moved with a %d budget in scope: got %d, want 2700", budget, got)
		}
	}
}

func TestBillingPreAuthCoversTheHappyPath(t *testing.T) {
	// A task that runs exactly to estimate must never capture more than was
	// held. This is the assertion that rules out reading "time_estimate_cost"
	// as the per-minute portion alone.
	for _, minutes := range []int{15, 30, 60, 90, 120, 240} {
		for _, category := range []string{"standard", "companionship"} {
			hold := preAuthAmountCents(category, minutes, 0)
			capture := quoteTask(category, minutes, 0).TotalCents
			if hold < capture {
				t.Errorf("%s %dmin: hold %d < on-estimate capture %d", category, minutes, hold, capture)
			}
		}
	}

	// The documented shape, spelled out once: 30 min standard is
	// ($12.00 + $7.50) x 1.5 = $29.25, + $20.00 budget + $5.00 buffer = $54.25.
	if got := preAuthAmountCents("standard", 30, 2000); got != 5425 {
		t.Errorf("preAuthAmountCents(standard, 30, 2000) = %d, want 5425", got)
	}
}

// ── The quote endpoint ─────────────────────────────────────────────────────

func callEstimate(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/estimate", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("uid", "11111111-1111-1111-1111-111111111111")

	estimateTaskCost(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestBillingEstimateBreakdownShape(t *testing.T) {
	code, out := callEstimate(t, `{"category":"standard","estimated_minutes":60,"prepay_amount_cents":1000}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", code, out)
	}

	// Every field a client needs to render an itemized quote without doing any
	// arithmetic of its own (S-05).
	want := map[string]float64{
		"base_fee_cents":        1200,
		"included_minutes":      15,
		"total_minutes":         60,
		"billable_minutes":      45,
		"time_cost_cents":       2250,
		"shopping_budget_cents": 1000,
		"total_cents":           4450,
	}
	for k, v := range want {
		got, ok := out[k]
		if !ok {
			t.Errorf("response is missing %q", k)
			continue
		}
		if got.(float64) != v {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
}

// Phase 1 deploys Go without a mobile build, and the shipped TaskForm reads
// `shopping_cents`. Dropping that key would render "$NaN" in every installed
// app the moment the backend went out.
func TestBillingEstimateKeepsLegacyShoppingKeyForShippedMobile(t *testing.T) {
	_, out := callEstimate(t, `{"category":"standard","estimated_minutes":60,"prepay_amount_cents":1000}`)

	legacy, ok := out["shopping_cents"]
	if !ok {
		t.Fatal("shopping_cents is gone — shipped mobile builds will render $NaN")
	}
	if legacy.(float64) != out["shopping_budget_cents"].(float64) {
		t.Errorf("shopping_cents %v != shopping_budget_cents %v", legacy, out["shopping_budget_cents"])
	}
}

func TestBillingEstimateRejectsOverCapBudget(t *testing.T) {
	code, out := callEstimate(t, `{"category":"standard","estimated_minutes":30,"prepay_amount_cents":3001}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a $30.01 budget", code)
	}
	if out["error"] != "shopping_budget_over_cap" {
		t.Errorf("error = %v, want shopping_budget_over_cap", out["error"])
	}
	if out["cap_cents"].(float64) != 3000 {
		t.Errorf("cap_cents = %v, want 3000", out["cap_cents"])
	}

	// Exactly at the cap is fine — the rejection is strictly above it.
	if code, _ := callEstimate(t, `{"category":"standard","estimated_minutes":30,"prepay_amount_cents":3000}`); code != http.StatusOK {
		t.Errorf("status = %d at exactly the cap, want 200", code)
	}
}

func TestBillingEstimateRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/estimate",
		strings.NewReader(`{"category":"standard","estimated_minutes":30}`))
	c.Request.Header.Set("Content-Type", "application/json")
	// No uid set — the middleware did not authenticate anyone.

	estimateTaskCost(c)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ── Settlement against real worklogs ───────────────────────────────────────

// The DB-backed half of the multi-session promise: two real sessions on one
// task, summed by SQL, then priced. This is what proves the -15 is applied to
// the sum rather than per session — the arithmetic test above can only prove
// the function does it, not that the handler feeds it the sum.
func TestBillingMultiSessionSettlementAgainstDB(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	ctx := context.Background()

	// 10 minutes, then 20. Summed: 30. Billable: 15. Cost: $12.00 + $7.50.
	seedClosedWorklog(t, w.taskID, 10)
	seedClosedWorklog(t, w.taskID, 20)

	totalMin, err := totalClosedMinutes(ctx, w.taskID)
	if err != nil {
		t.Fatalf("totalClosedMinutes: %v", err)
	}
	if totalMin != 30 {
		t.Fatalf("total minutes = %d, want 30 (sessions must sum)", totalMin)
	}

	got := calcTaskCostCents(ctx, w.taskID, totalMin)
	if got != 1950 {
		t.Errorf("cost = %d, want 1950 ($12.00 base + 15 billable min x $0.50)", got)
	}

	// The bug this guards: applying the inclusion per session would price the
	// same work at $12.00 + $2.50.
	if got == 1200+timeCostCents(10)+timeCostCents(20) {
		t.Error("inclusion is being applied per session, not once per task")
	}
}

// An open session is not billable time — the supporter is still on the clock.
func TestBillingOpenSessionIsNotBilled(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")

	seedClosedWorklog(t, w.taskID, 40)
	seedWorklog(t, w.taskID, 25, true) // still running

	totalMin, err := totalClosedMinutes(context.Background(), w.taskID)
	if err != nil {
		t.Fatalf("totalClosedMinutes: %v", err)
	}
	if totalMin != 40 {
		t.Errorf("total minutes = %d, want 40 — the open session must not count", totalMin)
	}
}

// seedClosedWorklog adds a session of exactly `minutes`, already clocked out.
// seedWorklog (admin_task_ops_test.go) always ends at now(), which makes two
// sessions on one task impossible to give distinct durations.
func seedClosedWorklog(t *testing.T, taskID string, minutes int) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO public.worklogs (task_id, "user", start_at, end_at)
		VALUES ($1::uuid, $2,
		        now() - make_interval(mins => $3),
		        now())
	`, taskID, oldSupporterEmail, minutes)
	if err != nil {
		t.Fatalf("seed closed worklog: %v", err)
	}
}
