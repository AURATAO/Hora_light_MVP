package main

// Stripe Phase 2b: settlement, receipts, mid-task approvals, the time cap.
//
// Four layers, same split as Phase 2a and for the same reason:
//
//  1. Arithmetic (no DB, no network). The time ceiling, the receipt tolerance,
//     and the claim that a Phase 2a hold still covers a Phase 2b settlement.
//     Table-driven, because "40 minutes on a 30-minute estimate" is exactly
//     the kind of number a reader should be able to check by eye.
//
//  2. Settlement (DB, no network). What the completion path writes and what
//     the breakdown endpoint answers. Stripe is unconfigured, so capture is a
//     no-op and what is under test is the SHAPE of the settlement rather than
//     the money — which is the half that decides what two people are told.
//
//  3. Failure (DB, no network). The property the whole file is built around:
//     a capture that fails does not stop a task completing. Provoked without a
//     network call by putting the payment row in a status Capture refuses
//     before it ever reaches Stripe.
//
//  4. Approvals and the cap (DB, no network). The extension_requests flow end
//     to end, and the three latches firing once each.
//
// The fifth layer — a real card, a real incremental authorization, a real
// capture — is payments_phase2b_smoke_test.go, behind STRIPE_SMOKE=1.
//
//	docker run -d --rm --name hora-p2b-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Phase2b -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// ── 1. Arithmetic ──────────────────────────────────────────────────────────

// The ceiling, and the one thing about it that is easy to get wrong.
//
// The cap bounds TOTAL LOGGED MINUTES, not billable minutes. Capping billable
// minutes instead hands the requester the 15-minute inclusion a second time —
// the `wrongIfCappingBillable` column below is what that bug would produce,
// and every row where it differs is a row where the bug would have cost or
// saved somebody real money.
func TestPhase2bTimeCapArithmetic(t *testing.T) {
	// Fixed against BillingConfig rather than recomputed from it, so that a
	// change to IncludedMinutes or AutoExtendMinutes fails here loudly instead
	// of silently agreeing with itself.
	if Billing.IncludedMinutes != 15 || Billing.AutoExtendMinutes != 15 || Billing.PerMinuteRateCents != 50 {
		t.Fatalf("this table is written against 15 included / 15 auto-extend / 50c per minute; BillingConfig now says %d/%d/%d",
			Billing.IncludedMinutes, Billing.AutoExtendMinutes, Billing.PerMinuteRateCents)
	}

	cases := []struct {
		name string
		// The ceiling, as taskTimeCapMinutes would compute it.
		capMinutes int
		// Total closed minutes across every session on the task.
		logged int

		wantBilledMinutes   int
		wantBillableMinutes int
		wantTimeCostCents   int
	}{
		// Inside the estimate: the cap is not in play at all.
		{"well inside, all within the inclusion", 45, 10, 10, 0, 0},
		{"inside, past the inclusion", 45, 30, 30, 15, 750},
		{"exactly at the estimate", 45, 45, 45, 30, 1500},

		// Consent ON: estimate 30 + auto-extend 15 = 45. The 15 minutes past
		// the estimate bill normally, with no interruption — Layer 1.
		{"consent on, 10 min over the estimate", 45, 40, 40, 25, 1250},
		{"consent on, exactly at the ceiling", 45, 45, 45, 30, 1500},
		{"consent on, past the ceiling: accrual stops", 45, 60, 45, 30, 1500},
		{"consent on, far past the ceiling", 45, 240, 45, 30, 1500},

		// Consent OFF: the ceiling IS the estimate.
		{"consent off, at the estimate", 30, 30, 30, 15, 750},
		{"consent off, one minute over", 30, 31, 30, 15, 750},
		{"consent off, an hour over", 30, 90, 30, 15, 750},

		// An approved 15-minute extension on top of consent: 30 + 15 + 15.
		{"one approved extension", 60, 60, 60, 45, 2250},
		{"one approved extension, still overrun", 60, 75, 60, 45, 2250},

		// A ceiling inside the included block. Nothing is billable either way,
		// and the cap must not make the base fee negative or the minutes wrap.
		{"tiny ceiling, inside the inclusion", 10, 30, 10, 0, 0},

		// No recorded estimate: no ceiling, everything bills.
		{"no ceiling", 0, 200, 200, 185, 9250},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			billed := cappedMinutes(tc.logged, tc.capMinutes)
			if billed != tc.wantBilledMinutes {
				t.Errorf("billed minutes = %d, want %d", billed, tc.wantBilledMinutes)
			}
			if got := billableMinutes(billed); got != tc.wantBillableMinutes {
				t.Errorf("billable minutes = %d, want %d", got, tc.wantBillableMinutes)
			}
			if got := timeCostCentsCapped(tc.logged, tc.capMinutes, Billing.PerMinuteRateCents); got != tc.wantTimeCostCents {
				t.Errorf("time cost = %d, want %d", got, tc.wantTimeCostCents)
			}
		})
	}

	// The bug the ordering exists to prevent, made explicit on one case rather
	// than asserted obliquely across the table: with a 45-minute ceiling and 60
	// minutes logged, capping BILLABLE minutes would price 45 of them — more
	// than the ceiling itself is worth — because the 15-minute inclusion would
	// have been handed back a second time.
	const capMinutes, logged = 45, 60
	right := billableMinutes(cappedMinutes(logged, capMinutes))
	wrong := cappedMinutes(billableMinutes(logged), capMinutes)
	if right != 30 || wrong != 45 {
		t.Fatalf("the two cap orderings no longer differ as documented: right=%d wrong=%d", right, wrong)
	}
}

// The cap is consumed ONCE across every session, exactly like the 15-minute
// inclusion — a task clocked in and out three times is capped on the sum, not
// per session. Stated separately from the table above because "multi-session"
// is a property of how the total is assembled, not of the arithmetic applied
// to it, and a reader should be able to see that it was thought about.
func TestPhase2bTimeCapIsPerTaskNotPerSession(t *testing.T) {
	const capMinutes = 45

	// Three 20-minute sessions. Per-session capping would bill all 60 minutes
	// (none of them individually exceeds 45); per-task capping bills 45.
	sessions := []int{20, 20, 20}
	total := 0
	for _, m := range sessions {
		total += m
	}

	perTask := timeCostCentsCapped(total, capMinutes, Billing.PerMinuteRateCents)
	perSession := 0
	for _, m := range sessions {
		perSession += timeCostCentsCapped(m, capMinutes, Billing.PerMinuteRateCents)
	}

	// 45 capped -> 30 billable -> $15.00
	if perTask != 1500 {
		t.Errorf("per-task cap billed %s, want $15.00", formatCentsUSD(perTask))
	}
	if perSession == perTask {
		t.Fatal("this test cannot tell per-session capping from per-task capping — pick different sessions")
	}
	// And the inclusion is likewise consumed once: three sessions of 20 do not
	// get 15 free minutes each.
	if billableMinutes(total) != 45 {
		t.Errorf("inclusion applied more than once: %d billable from %d logged", billableMinutes(total), total)
	}
}

// Where Layer 2 fires, including the degenerate short task where the lead time
// is longer than the whole job. A warning that has already fired by the time
// the supporter clocks in tells them nothing.
//
// The argument is the AGREED minutes — estimate plus approved extensions — and
// not the ceiling. See TestPhase2bWarningAnchorsToTheEstimateNotTheCeiling for
// why that distinction is the point.
func TestPhase2bWarningThreshold(t *testing.T) {
	cases := []struct {
		agreedMinutes int
		want          int
	}{
		{0, 0},   // nothing agreed, no warning
		{1, 1},   // clamped: agreed - 5 would be negative
		{5, 1},   // clamped: agreed - 5 would be 0, i.e. "warn at the start"
		{6, 1},   // the first estimate where the lead fits exactly
		{30, 25}, // a 30-minute estimate, with or without consent
		{45, 40}, // a 30-minute estimate plus an approved 15
		{60, 55},
	}
	for _, tc := range cases {
		if got := timeCapWarningMinutes(tc.agreedMinutes); got != tc.want {
			t.Errorf("agreed=%d: warn at %d, want %d", tc.agreedMinutes, got, tc.want)
		}
	}
	// And it is always strictly before the time it is warning about, which is
	// the whole point of calling it a warning.
	for agreed := 1; agreed <= 600; agreed++ {
		if w := timeCapWarningMinutes(agreed); w > agreed {
			t.Fatalf("agreed=%d warns at %d — after the moment it warns about", agreed, w)
		}
	}
}

// THE BUILD 11 FINDING. With auto-extend on, the est-5 warning fired at
// ceiling-5 — 40 minutes into a 30-minute task — which announced the estimate
// ten minutes after it had gone past, with the fuse already a third burnt.
//
// The warning's job is "you are approaching YOUR ESTIMATE". Auto-extend is a
// fuse, not a new estimate, so it moves the CEILING and nothing else. The
// ceiling events — billing stop, the approval flow, the ops alert — stay where
// they were.
//
// Read as the spec's two acceptance rows: a 30-minute task warns at 25 either
// way, and stops at 45 with consent or 30 without.
func TestPhase2bWarningAnchorsToTheEstimateNotTheCeiling(t *testing.T) {
	cases := []struct {
		name string
		// The requester's estimate, their consent at post, and any minutes
		// they approved mid-task.
		estimate      int
		consent       bool
		approvedExtra int

		wantAgreed int
		wantWarnAt int
		wantCap    int
	}{
		{"30-min task, auto-extend ON", 30, true, 0, 30, 25, 45},
		{"30-min task, auto-extend OFF", 30, false, 0, 30, 25, 30},
		// An approval IS a new estimate — the requester has looked at the job
		// and named a bigger number — so it moves the anchor as well as the
		// ceiling. Without this, resolveExtension's latch reset would re-fire
		// the warning on the very next ping, since the task is already past
		// the original estimate.
		{"30-min task, +15 approved, auto-extend ON", 30, true, 15, 45, 40, 60},
		{"30-min task, +15 approved, auto-extend OFF", 30, false, 15, 45, 40, 45},
		// A five-minute task cannot warn at zero. The clamp holds whichever
		// side of the ceiling the anchor is on.
		{"5-min task, auto-extend ON", 5, true, 0, 5, 1, 20},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Per subtest, not per test: seedOpsWorld inserts the same fixed
			// emails every time, so it needs a fresh fixture to insert into.
			setupStripeWebhookDB(t)
			w := seedOpsWorld(t, "open")
			ctx := context.Background()
			if _, err := db.Exec(ctx, `
				update public.tasks set estimated_minutes = $2, auto_extend_consent = $3
				 where id = $1::uuid
			`, w.taskID, tc.estimate, tc.consent); err != nil {
				t.Fatalf("set estimate: %v", err)
			}
			if tc.approvedExtra > 0 {
				if _, err := db.Exec(ctx, `
					insert into public.extension_requests
						(task_id, supporter_id, kind, requested_minutes, status, resolved_at)
					values ($1::uuid, $2::uuid, 'time', $3, 'approved', now())
				`, w.taskID, w.supporterID, tc.approvedExtra); err != nil {
					t.Fatalf("seed approved extension: %v", err)
				}
			}

			capMinutes, detail := taskTimeCapMinutes(ctx, w.taskID)
			if capMinutes != tc.wantCap {
				t.Errorf("cap = %d, want %d — the ceiling moved", capMinutes, tc.wantCap)
			}
			if detail.AgreedMinutes != tc.wantAgreed {
				t.Errorf("agreed = %d, want %d", detail.AgreedMinutes, tc.wantAgreed)
			}
			if detail.WarnAtMinutes != tc.wantWarnAt {
				t.Errorf("warn at %d min, want %d — auto-extend moved the warning",
					detail.WarnAtMinutes, tc.wantWarnAt)
			}

			// And the state machine agrees: one minute before the anchor is
			// quiet, the anchor itself warns without stopping billing, and the
			// ceiling is where billing stops.
			seedWorklog(t, w.taskID, tc.wantWarnAt-1, false)
			if st := readTimeCapState(ctx, w.taskID); st.Warning || st.Reached {
				t.Errorf("at %d min: warning=%v reached=%v — fired early",
					tc.wantWarnAt-1, st.Warning, st.Reached)
			}
			setSingleSessionMinutes(t, w.taskID, tc.wantWarnAt)
			if st := readTimeCapState(ctx, w.taskID); !st.Warning || st.Reached {
				t.Errorf("at the %d-min anchor: warning=%v reached=%v, want warning only",
					tc.wantWarnAt, st.Warning, st.Reached)
			}
			setSingleSessionMinutes(t, w.taskID, tc.wantCap)
			if st := readTimeCapState(ctx, w.taskID); !st.Reached {
				t.Errorf("at the %d-min ceiling: reached=false — billing did not stop", tc.wantCap)
			}
		})
	}
}

// setSingleSessionMinutes rewrites the task's one seeded session to be exactly
// `minutes` long, so a test can walk a task up to a threshold without seeding
// a second session and changing the rounding.
func setSingleSessionMinutes(t *testing.T, taskID string, minutes int) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		update public.worklogs
		   set start_at = now() - make_interval(mins => $2), end_at = now()
		 where task_id = $1::uuid
	`, taskID, minutes); err != nil {
		t.Fatalf("set session length: %v", err)
	}
}

// The receipt, against the approved budget and the $5 auto-approved tolerance.
func TestPhase2bReceiptTolerance(t *testing.T) {
	if Billing.OverageToleranceCents != 500 {
		t.Fatalf("this table is written against a $5.00 tolerance; BillingConfig says %d",
			Billing.OverageToleranceCents)
	}

	cases := []struct {
		name           string
		receipt        int
		approvedBudget int
		wantWithin     bool
		wantReimbursed int
	}{
		{"nothing bought", 0, 2000, true, 0},
		{"well under budget", 1200, 2000, true, 1200},
		{"exactly the budget", 2000, 2000, true, 2000},
		{"a dollar over — inside the tolerance", 2100, 2000, true, 2100},
		{"exactly the tolerance", 2500, 2000, true, 2500},
		{"a cent past the tolerance", 2501, 2000, false, 2500},
		{"wildly over", 9000, 2000, false, 2500},
		{"no budget approved, nothing bought", 0, 0, true, 0},
		// A receipt on a task with no approved budget is over by definition
		// the moment it exceeds the bare tolerance.
		{"no budget approved, small receipt", 400, 0, true, 400},
		{"no budget approved, receipt past the tolerance", 600, 0, false, 500},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := receiptWithinTolerance(tc.receipt, tc.approvedBudget); got != tc.wantWithin {
				t.Errorf("within tolerance = %v, want %v", got, tc.wantWithin)
			}
			if got := reimbursableReceiptCents(tc.receipt, tc.approvedBudget); got != tc.wantReimbursed {
				t.Errorf("reimbursed = %d, want %d", got, tc.wantReimbursed)
			}
		})
	}
}

// The hold NO LONGER covers a worst-case settlement, and that is deliberate.
//
// This replaced a test asserting the opposite. The old hold was padded so a
// capture always fit inside it; the new one is exactly what the requester was
// shown, so a task that runs to its ceiling with a receipt at the top of the
// tolerance settles for MORE than was reserved. That difference is collected
// at completion (payments.kind = 'completion_balance').
//
// Pinned because it is a design decision that looks like a bug: anyone reading
// preAuthAmountCents and settlement side by side will notice the gap, and this
// is where they find out it is intended and what pays for it.
func TestPhase2bSettlementCanExceedTheHold(t *testing.T) {
	const category, estimate, budget = "delivery", 30, 2000
	rate := Billing.PerMinuteRateCents

	held := preAuthAmountCents(category, estimate, budget, rate)

	// The worst case: every consented minute worked, and a receipt at the very
	// top of what is reimbursable.
	capMinutes := estimate + Billing.AutoExtendMinutes
	settled := baseFeeCents(category) +
		timeCostCentsCapped(capMinutes, capMinutes, rate) +
		budget + Billing.OverageToleranceCents

	if settled <= held {
		t.Fatalf("settlement %s does not exceed the hold %s — this test no longer describes the model",
			formatCentsUSD(settled), formatCentsUSD(held))
	}
	t.Logf("worst case settles %s above a %s hold; the difference is charged at completion",
		formatCentsUSD(settled-held), formatCentsUSD(held))

	// And the on-estimate case still fits exactly, with nothing left over —
	// the common path takes no second charge and releases nothing.
	onEstimate := baseFeeCents(category) + timeCostCents(estimate, rate) + budget
	if onEstimate != held {
		t.Errorf("a task that runs exactly to estimate settles %s against a %s hold",
			formatCentsUSD(onEstimate), formatCentsUSD(held))
	}
}

// ── 2. Settlement (DB, no network) ─────────────────────────────────────────

// The whole point of extending /tasks/:id/worklogs rather than adding a
// parallel endpoint: the same call answers "what does this cost so far" and
// "what was I charged", and both sides read it.
func TestPhase2bSettlementBreakdownPayload(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 40, false) // 40 closed minutes against a 30-min estimate

	// In progress: no receipt yet, and the ceiling is already biting (consent
	// defaults true, so the cap is 30 + 15 = 45; 40 logged is inside it).
	body := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)
	cost := body["cost"].(map[string]any)
	if got := num(cost["billed_minutes"]); got != 40 {
		t.Errorf("in progress: billed_minutes = %d, want 40", got)
	}
	if got := num(cost["cap_minutes"]); got != 45 {
		t.Errorf("in progress: cap_minutes = %d, want 45 (30 estimate + 15 consented)", got)
	}
	// base $12.00 + 25 billable x $0.50 = $24.50
	if got := num(cost["total_cents"]); got != 2450 {
		t.Errorf("in progress: total = %s, want $24.50", formatCentsUSD(got))
	}
	settlement := body["settlement"].(map[string]any)
	if settlement["state"] != "not_charged" {
		// No payments row at all on this task: nothing was ever held, so the
		// figures are what WOULD be charged.
		if settlement["state"] != "estimated" {
			t.Errorf("state = %v, want estimated", settlement["state"])
		}
	}

	// Complete it with a receipt.
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 1740,
		"receipt_photo_url":    "https://example.test/receipt.jpg",
	}, http.StatusOK)

	body = getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)
	cost = body["cost"].(map[string]any)
	settlement = body["settlement"].(map[string]any)

	if got := num(cost["shopping_receipt_cents"]); got != 1740 {
		t.Errorf("settled: receipt = %s, want $17.40", formatCentsUSD(got))
	}
	// $12.00 base + $12.50 time + $17.40 receipt = $41.90
	if got := num(cost["total_cents"]); got != 4190 {
		t.Errorf("settled: total = %s, want $41.90", formatCentsUSD(got))
	}
	if got := num(settlement["time_cost_cents"]); got != 2450 {
		t.Errorf("settled: time half = %s, want $24.50", formatCentsUSD(got))
	}
	if got := num(settlement["approved_budget_cents"]); got != 2000 {
		t.Errorf("settled: approved budget = %s, want $20.00", formatCentsUSD(got))
	}
	if settlement["receipt_photo_url"] != "https://example.test/receipt.jpg" {
		t.Errorf("settled: receipt photo missing from the requester's settlement view: %v", settlement)
	}

	// And the supporter sees the same breakdown — a settlement only one party
	// can read is not transparency.
	supporterBody := getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	supporterCost := supporterBody["cost"].(map[string]any)
	if num(supporterCost["total_cents"]) != num(cost["total_cents"]) {
		t.Errorf("supporter sees %v, requester sees %v", supporterCost["total_cents"], cost["total_cents"])
	}
}

// The ceiling, at settlement rather than in the abstract: a supporter who runs
// 40 minutes past a 30-minute estimate with no approved extension is paid to
// the ceiling and no further.
func TestPhase2bOverrunSettlesAtTheCap(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 70, false) // 70 minutes against a 45-minute ceiling

	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusOK)

	body := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)
	cost := body["cost"].(map[string]any)

	if got := num(cost["total_minutes"]); got != 70 {
		t.Errorf("logged minutes = %d, want 70 — the clock is not what is capped", got)
	}
	if got := num(cost["billed_minutes"]); got != 45 {
		t.Errorf("billed minutes = %d, want 45", got)
	}
	// $12.00 + 30 x $0.50 = $27.00, not the $39.50 an uncapped 70 minutes
	// would have produced.
	if got := num(cost["total_cents"]); got != 2700 {
		t.Errorf("total = %s, want $27.00", formatCentsUSD(got))
	}
}

// A receipt past budget + tolerance is refused, and refused BEFORE the task is
// marked complete — the supporter's only way out is a budget increase, and a
// completed task offers neither that nor a correction.
func TestPhase2bReceiptOverBudgetIsRefused(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 30, false)

	body := completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 2600, // $26.00 against $20.00 + $5.00
		"receipt_photo_url":    "https://example.test/receipt.jpg",
	}, http.StatusBadRequest)

	if body["error"] != "receipt_exceeds_budget" {
		t.Fatalf("error = %v, want receipt_exceeds_budget", body["error"])
	}
	if got := num(body["max_receipt_cents"]); got != 2500 {
		t.Errorf("max receipt = %s, want $25.00", formatCentsUSD(got))
	}
	// The supporter is told what to do, not just what went wrong.
	if msg, _ := body["message"].(string); !strings.Contains(strings.ToLower(msg), "increase") {
		t.Errorf("message does not point at a budget increase: %q", msg)
	}
	if got := taskStatus(t, w.taskID); got != "open" {
		t.Errorf("task status = %q — a refused receipt must leave the task completable", got)
	}
}

// Nothing bought is a perfectly good outcome on a shopping task, and it must
// not be confused with "forgot to enter the receipt".
func TestPhase2bShoppingTaskRequiresAnAnswerButZeroIsFine(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 30, false)

	// Silence is refused: an old client that knows nothing about receipts must
	// not be read as declaring a $0 receipt on a task somebody shopped for.
	body := completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusBadRequest)
	if body["error"] != "receipt_required" {
		t.Fatalf("error = %v, want receipt_required", body["error"])
	}

	// An explicit zero is accepted, and needs no photo — there is nothing to
	// photograph.
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 0,
	}, http.StatusOK)

	cost := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["cost"].(map[string]any)
	if got := num(cost["shopping_receipt_cents"]); got != 0 {
		t.Errorf("receipt = %s on a task where nothing was bought", formatCentsUSD(got))
	}
	// $12.00 base + 15 billable x $0.50 = $19.50
	if got := num(cost["total_cents"]); got != 1950 {
		t.Errorf("total = %s, want $19.50", formatCentsUSD(got))
	}
}

// A receipt with money on it needs a photo. The requester is being asked to
// reimburse a number; the photo is the only thing behind it.
func TestPhase2bReceiptAmountRequiresAPhoto(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 30, false)

	body := completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 1200,
	}, http.StatusBadRequest)
	if body["error"] != "receipt_photo_required" {
		t.Fatalf("error = %v, want receipt_photo_required", body["error"])
	}
}

// ── 3. Failure ─────────────────────────────────────────────────────────────

// The property this entire phase is built around: a money problem is never a
// supporter's problem.
//
// Provoked without a network call. Capture refuses a payment row that is not
// 'authorized' before it ever reaches Stripe, so a row parked in requires_auth
// — which is what a hold that never came back from a 3DS challenge looks like
// — produces a real capture failure on a real code path, offline.
func TestPhase2bCaptureFailureStillCompletesTheTask(t *testing.T) {
	setupStripeWebhookDB(t)
	// A syntactically valid test key so paymentsEnabled() is true. Nothing in
	// this test reaches the network: Capture's status guard fires first.
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_offline_guard_only")
	forceStripeKey(t, "sk_test_offline_guard_only")

	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	var paymentID string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, authorized_cents)
		values ($1::uuid, $2::uuid, $3, 'requires_auth', $4, 7675)
		returning id::text
	`, w.taskID, w.requesterID, paymentKindTaskPayment, "pi_never_authorized").Scan(&paymentID); err != nil {
		t.Fatalf("seed stuck hold: %v", err)
	}

	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusOK)

	// The task completed. That is the whole assertion.
	if got := taskStatus(t, w.taskID); got != "completed" {
		t.Fatalf("task status = %q — a capture failure blocked a completion", got)
	}
	// And the failure is recorded where ops will find it, in its own status
	// rather than collapsed into 'failed' (which means the hold never landed).
	if got := paymentStatusByID(t, paymentID); got != paymentStatusCaptureFailed {
		t.Errorf("payment status = %q, want %q", got, paymentStatusCaptureFailed)
	}
	rows := auditRows(t, w.taskID, "PAYMENT_CAPTURE_FAILED")
	if len(rows) != 1 {
		t.Fatalf("%d PAYMENT_CAPTURE_FAILED audit rows, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Meta, "owed_cents") {
		t.Errorf("audit row does not say what is uncollected: %s", rows[0].Meta)
	}
	// Nothing was recorded as settled, because nothing was.
	var settledTotal *int
	_ = db.QueryRow(context.Background(),
		`select settled_total_cents from public.tasks where id=$1::uuid`, w.taskID).Scan(&settledTotal)
	if settledTotal != nil {
		t.Errorf("settled_total_cents = %d on a task nobody paid for", *settledTotal)
	}
}

// ── 4. Approvals ───────────────────────────────────────────────────────────

// The happy path: supporter asks, requester approves, the ceiling moves.
func TestPhase2bBudgetIncreaseApproved(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	code, body := requestBudgetAs(t, w.taskID, w.supporterID, `{
		"requested_cents": 800,
		"reason": "only the 500g jar was left",
		"fallback": "buy_alternative",
		"fallback_note": "the 500g jar instead"
	}`)
	if code != http.StatusCreated {
		t.Fatalf("request: %d (%v)", code, body)
	}
	extID, _ := body["id"].(string)
	if extID == "" {
		t.Fatal("no request id returned")
	}
	// The requester was actually asked. A flow whose notification is missing
	// is a flow that times out every time.
	if n := countNotifications(t, w.requesterID, "BUDGET_INCREASE_REQUESTED"); n != 1 {
		t.Errorf("%d BUDGET_INCREASE_REQUESTED notifications, want 1", n)
	}

	code, body = resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("approve: %d (%v)", code, body)
	}
	if got := num(body["approved_budget_cents"]); got != 2800 {
		t.Errorf("approved budget = %s, want $28.00", formatCentsUSD(got))
	}
	// Both sides are told how it ended. Announced on a background goroutine so
	// the requester's tap is not waiting on two mail round-trips, which is why
	// this waits rather than reads.
	waitFor(t, func() bool {
		return countNotifications(t, w.supporterID, "EXTENSION_RESOLVED") == 1 &&
			countNotifications(t, w.requesterID, "EXTENSION_RESOLVED") == 1
	}, "both parties notified of the approval")

	// And the raised ceiling is what the receipt is now checked against: $26
	// was refused before the increase and is fine after it.
	seedWorklog(t, w.taskID, 30, false)
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 2600,
		"receipt_photo_url":    "https://example.test/receipt.jpg",
	}, http.StatusOK)
}

// A denial writes nothing but the row: nothing was granted in advance, so
// there is nothing to undo — and the supporter is handed their own fallback
// back, in their own words.
func TestPhase2bBudgetIncreaseDenied(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	_, body := requestBudgetAs(t, w.taskID, w.supporterID, `{
		"requested_cents": 800, "fallback": "skip_item"
	}`)
	extID, _ := body["id"].(string)

	code, body := resolveExtensionAs(t, denyExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("deny: %d (%v)", code, body)
	}
	if got := num(body["approved_budget_cents"]); got != 2000 {
		t.Errorf("approved budget moved on a denial: %s", formatCentsUSD(got))
	}
	req := body["request"].(map[string]any)
	if req["status"] != extensionStatusDenied {
		t.Errorf("status = %v, want denied", req["status"])
	}
	if inst, _ := req["fallback_instruction"].(string); !strings.Contains(strings.ToLower(inst), "skip") {
		t.Errorf("supporter is not told their own fallback: %q", inst)
	}
}

// Silence is a denial, and the supporter learns it from their own screen: the
// expiry is applied by whoever reads the list next, which is them, polling.
func TestPhase2bBudgetIncreaseExpiresIntoTheFallback(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	_, body := requestBudgetAs(t, w.taskID, w.supporterID, `{
		"requested_cents": 800, "fallback": "buy_alternative", "fallback_note": "the 500g jar"
	}`)
	extID, _ := body["id"].(string)

	// Age it past the timeout. Nothing else moves — no sweep is run, no timer
	// fires; the next read is the whole mechanism.
	if _, err := db.Exec(context.Background(), `
		update public.extension_requests
		   set created_at = now() - make_interval(mins => $2)
		 where id = $1::uuid
	`, extID, Billing.ApprovalTimeoutMinutes+1); err != nil {
		t.Fatalf("age request: %v", err)
	}

	list := listExtensionsAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	items := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d requests listed, want 1", len(items))
	}
	item := items[0].(map[string]any)
	if item["status"] != extensionStatusExpired {
		t.Fatalf("status = %v, want expired — reading the list is what expires it", item["status"])
	}
	inst, _ := item["fallback_instruction"].(string)
	if !strings.Contains(inst, "500g jar") {
		t.Errorf("expired request does not read the supporter's fallback back to them: %q", inst)
	}

	// An expiry that nobody is told about is indistinguishable from a request
	// that vanished, so both sides get the resolution notification. It is
	// fired on a background goroutine, so give it a moment.
	waitFor(t, func() bool {
		return countNotifications(t, w.supporterID, "EXTENSION_RESOLVED") == 1 &&
			countNotifications(t, w.requesterID, "EXTENSION_RESOLVED") == 1
	}, "both parties notified of the expiry")

	// And an expired request is no longer answerable — a tap that lands a
	// second late must not charge the requester for something the supporter
	// has already worked around.
	code, resolveBody := resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusConflict {
		t.Fatalf("approving an expired request: %d (%v), want 409", code, resolveBody)
	}
	if resolveBody["status"] != extensionStatusExpired {
		t.Errorf("409 does not say why: %v", resolveBody)
	}
}

// One pending ask per kind. The partial unique index is what makes two
// concurrent taps impossible; this is the handler translating it into an
// answer a client can act on.
func TestPhase2bSecondPendingRequestIsRefused(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	if code, body := requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents": 800, "fallback": "skip_item"}`); code != http.StatusCreated {
		t.Fatalf("first request: %d (%v)", code, body)
	}
	code, body := requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents": 500, "fallback": "skip_item"}`)
	if code != http.StatusConflict {
		t.Fatalf("second request: %d (%v), want 409", code, body)
	}
	if body["error"] != "request_already_pending" {
		t.Errorf("error = %v, want request_already_pending", body["error"])
	}

	// A TIME request is a different question and must not be blocked by a
	// pending money question — the requester's silence about a budget must not
	// also stop the clock.
	if code, body := requestTimeAs(t, w.taskID, w.supporterID, 15); code != http.StatusCreated {
		t.Fatalf("time request blocked by a pending budget request: %d (%v)", code, body)
	}
}

// Only the assigned supporter can ask, and only the requester can answer.
func TestPhase2bExtensionAuthorization(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	// The requester cannot raise their own budget through this door — that is
	// an edit to their own task.
	if code, _ := requestBudgetAs(t, w.taskID, w.requesterID,
		`{"requested_cents": 800, "fallback": "skip_item"}`); code != http.StatusForbidden {
		t.Errorf("requester raised their own budget: %d", code)
	}

	_, body := requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents": 800, "fallback": "skip_item"}`)
	extID, _ := body["id"].(string)

	// And the supporter cannot approve their own ask.
	if code, _ := resolveExtensionAs(t, approveExtension, w.taskID, extID, w.supporterID); code != http.StatusForbidden {
		t.Errorf("supporter approved their own request: %d", code)
	}
}

// A budget request must name a fallback. The whole value of the five-minute
// timeout is that silence resolves to an action the supporter already chose.
func TestPhase2bBudgetRequestRequiresAFallback(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	code, body := requestBudgetAs(t, w.taskID, w.supporterID, `{"requested_cents": 800}`)
	if code != http.StatusBadRequest || body["error"] != "fallback_required" {
		t.Fatalf("a fallback-less request was accepted: %d (%v)", code, body)
	}
}

// An approved time extension raises the ceiling and re-arms the warnings, so
// the supporter is warned again as the NEW ceiling approaches rather than
// running silently into it having been warned about the old one.
func TestPhase2bApprovedTimeExtensionRaisesTheCapAndReArmsTheWarnings(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 50, false) // past the 45-minute ceiling

	// Evaluating fires Layers 1 and 2 and sets both latches.
	evaluateTimeCap(context.Background(), w.taskID)
	if !latchSet(t, w.taskID, "time_cap_warned_at") || !latchSet(t, w.taskID, "time_cap_reached_at") {
		t.Fatal("the cap was passed but no layer fired")
	}

	_, body := requestTimeAsBody(t, w.taskID, w.supporterID, 30)
	extID, _ := body["id"].(string)
	if n := countNotifications(t, w.requesterID, "TIME_EXTENSION_REQUESTED"); n != 1 {
		t.Errorf("%d TIME_EXTENSION_REQUESTED notifications, want 1", n)
	}

	code, body := resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("approve: %d (%v)", code, body)
	}
	capDetail := body["time_cap"].(map[string]any)
	if got := num(capDetail["cap_minutes"]); got != 75 {
		t.Errorf("cap = %d, want 75 (30 estimate + 15 consented + 30 approved)", got)
	}
	if latchSet(t, w.taskID, "time_cap_warned_at") || latchSet(t, w.taskID, "time_cap_reached_at") {
		t.Error("the latches were not re-armed — the supporter will hit the new ceiling unwarned")
	}

	// And the money follows: 50 logged minutes now bill in full.
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusOK)
	cost := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["cost"].(map[string]any)
	if got := num(cost["billed_minutes"]); got != 50 {
		t.Errorf("billed minutes = %d, want 50 — the approved extension did not reach billing", got)
	}
}

// Each layer fires exactly once, and "once" is the database's job rather than
// a flag in memory — two GPS pings landing in the same second must produce one
// notification, not two.
func TestPhase2bTimeCapLayersFireOnce(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 41, false) // past the 40-minute warning, inside the 45 ceiling

	for i := 0; i < 5; i++ {
		evaluateTimeCap(context.Background(), w.taskID)
	}
	if n := countNotifications(t, w.supporterID, "TIME_CAP_WARNING"); n != 1 {
		t.Errorf("%d supporter TIME_CAP_WARNING notifications after five evaluations, want 1", n)
	}
	if n := countNotifications(t, w.requesterID, "TIME_CAP_WARNING"); n != 1 {
		t.Errorf("%d requester TIME_CAP_WARNING notifications, want 1", n)
	}
	// Inside the ceiling: billing has not stopped and nobody is told it has.
	if n := countNotifications(t, w.supporterID, "TIME_CAP_REACHED"); n != 0 {
		t.Errorf("%d TIME_CAP_REACHED notifications before the ceiling", n)
	}

	// Push past the ceiling.
	if _, err := db.Exec(context.Background(), `
		update public.worklogs set start_at = now() - make_interval(mins => 60)
		 where task_id = $1::uuid
	`, w.taskID); err != nil {
		t.Fatalf("extend session: %v", err)
	}
	for i := 0; i < 5; i++ {
		evaluateTimeCap(context.Background(), w.taskID)
	}
	if n := countNotifications(t, w.supporterID, "TIME_CAP_REACHED"); n != 1 {
		t.Errorf("%d supporter TIME_CAP_REACHED notifications, want 1", n)
	}
	if n := countNotifications(t, w.requesterID, "TIME_CAP_REACHED"); n != 1 {
		t.Errorf("%d requester TIME_CAP_REACHED notifications, want 1", n)
	}
	if rows := auditRows(t, w.taskID, "TIME_CAP_REACHED"); len(rows) != 1 {
		t.Errorf("%d TIME_CAP_REACHED audit rows, want 1", len(rows))
	}
	// And no automatic cancellation, at any point.
	if got := taskStatus(t, w.taskID); got != "open" {
		t.Fatalf("task status = %q — the cap cancelled a task", got)
	}
}

// Layer 3. The grace period is measured from when the ceiling was reached, and
// a pending time request pauses it — the requester is being asked right now,
// which is not the same as ignoring it.
func TestPhase2bUnresponsiveRequesterReachesOps(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 60, false)

	evaluateTimeCap(context.Background(), w.taskID)
	if latchSet(t, w.taskID, "time_cap_ops_alerted_at") {
		t.Fatal("ops were alerted the instant the cap was reached — the grace period did nothing")
	}

	// Age the reached-at latch past the grace period.
	if _, err := db.Exec(context.Background(), `
		update public.tasks set time_cap_reached_at = now() - make_interval(mins => $2)
		 where id = $1::uuid
	`, w.taskID, Billing.GracePeriodMinutes+1); err != nil {
		t.Fatalf("age latch: %v", err)
	}

	// A pending time request holds it off: the requester has been asked and
	// their five minutes are still running.
	requestTimeAsBody(t, w.taskID, w.supporterID, 15)
	evaluateTimeCap(context.Background(), w.taskID)
	if latchSet(t, w.taskID, "time_cap_ops_alerted_at") {
		t.Fatal("ops were alerted while a request was still outstanding")
	}

	// Answer it — a denial is an answer, and the supporter is still working
	// unpaid past the cap.
	if _, err := db.Exec(context.Background(),
		`update public.extension_requests set status='denied', resolved_at=now() where task_id=$1::uuid`,
		w.taskID); err != nil {
		t.Fatalf("resolve request: %v", err)
	}
	for i := 0; i < 3; i++ {
		evaluateTimeCap(context.Background(), w.taskID)
	}
	if !latchSet(t, w.taskID, "time_cap_ops_alerted_at") {
		t.Fatal("the grace period elapsed with nobody answering and ops were never told")
	}
	rows := auditRows(t, w.taskID, "TIME_CAP_UNRESPONSIVE")
	if len(rows) != 1 {
		t.Fatalf("%d TIME_CAP_UNRESPONSIVE audit rows after three evaluations, want 1", len(rows))
	}
	if got := taskStatus(t, w.taskID); got != "open" {
		t.Errorf("task status = %q — Layer 3 cancelled a task", got)
	}
}

// ── 5. Cancel with closed sessions ─────────────────────────────────────────

// The branch Phase 1 built the settlement for and left unreachable.
func TestPhase2bCancelWithClosedSessionSettlesAndDetaches(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 40, false)

	code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed")
	if code != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", code, body)
	}
	// base $12.00 + 25 billable x $0.50 = $24.50. The supporter travelled and
	// worked; a cancel does not un-earn that.
	if got := num(body["bill_cents"]); got != 2450 {
		t.Errorf("bill = %s, want $24.50", formatCentsUSD(got))
	}
	if got := taskStatus(t, w.taskID); got != "cancelled" {
		t.Errorf("status = %q", got)
	}

	var assignedTo *string
	if err := db.QueryRow(context.Background(),
		`select assigned_to_id::text from public.tasks where id=$1::uuid`, w.taskID).Scan(&assignedTo); err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if assignedTo != nil {
		t.Errorf("supporter still attached to a cancelled task: %v", *assignedTo)
	}
	// Detached but not locked out: they were just paid for this task and have
	// to be able to see what for.
	if _, ok := getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)["settlement"]; !ok {
		t.Error("the detached supporter cannot read the settlement they were paid under")
	}
	// Told, and told what they earned — not just that it ended.
	if n := countNotifications(t, w.supporterID, "CANCELLED"); n != 1 {
		t.Errorf("%d supporter CANCELLED notifications, want 1", n)
	}
	if body := notificationBody(t, w.supporterID, "CANCELLED"); !strings.Contains(body, "$24.50") {
		t.Errorf("the supporter is not told what they were paid: %q", body)
	}
}

// Cancel before any clock-in is unchanged: $0, a full release, and — because
// there is nothing to settle and a supporter may already be travelling — an
// accepted task with nothing logged is still refused.
func TestPhase2bCancelBeforeAnyClockInIsUnchanged(t *testing.T) {
	t.Run("accepted, nothing logged", func(t *testing.T) {
		setupStripeWebhookDB(t)
		w := seedOpsWorld(t, "open")
		code, body := cancelAs(t, w.taskID, w.requesterID, "changed my mind")
		if code != http.StatusBadRequest {
			t.Fatalf("cancel: %d (%v), want 400", code, body)
		}
		if got := taskStatus(t, w.taskID); got != "open" {
			t.Errorf("status = %q", got)
		}
	})

	t.Run("unaccepted", func(t *testing.T) {
		setupStripeWebhookDB(t)
		w := seedOpsWorld(t, "open")
		if _, err := db.Exec(context.Background(),
			`update public.tasks set assigned_to_id = null, assigned_to = '' where id=$1::uuid`,
			w.taskID); err != nil {
			t.Fatalf("unassign: %v", err)
		}
		code, body := cancelAs(t, w.taskID, w.requesterID, "changed my mind")
		if code != http.StatusOK {
			t.Fatalf("cancel: %d (%v)", code, body)
		}
		if got := num(body["bill_cents"]); got != 0 {
			t.Errorf("bill = %s on a task nobody started, want $0.00", formatCentsUSD(got))
		}

		// And the settlement view agrees with the bill. quoteSettlement would
		// answer with the base fee here — right for a task that was worked,
		// wrong for one that was never begun — so the two surfaces have to be
		// checked against each other rather than each against itself.
		cost := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["cost"].(map[string]any)
		if got := num(cost["total_cents"]); got != 0 {
			t.Errorf("settlement shows %s on a task nobody started, want $0.00", formatCentsUSD(got))
		}
		if got := num(cost["base_fee_cents"]); got != 0 {
			t.Errorf("settlement shows a %s base fee on a task nobody started", formatCentsUSD(got))
		}
	})
}

// ── 6. Adjust time, before and after the money moves ───────────────────────

func TestPhase2bAdjustTimeBeforeCaptureRecomputes(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	before := num(getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["total_cost_cents"])

	code, body := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":10}`)
	if code != http.StatusOK {
		t.Fatalf("adjust: %d (%v)", code, body)
	}

	after := num(getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["total_cost_cents"])
	if after != before+500 { // 10 more billable minutes x $0.50
		t.Errorf("cost went %s → %s, want a $5.00 increase", formatCentsUSD(before), formatCentsUSD(after))
	}
}

func TestPhase2bAdjustTimeAfterCaptureIsRefused(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	if _, err := db.Exec(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, authorized_cents,
		                             captured_cents, time_cost_cents)
		values ($1::uuid, $2::uuid, $3, 'captured', $4, 7675, 1950, 1950)
	`, w.taskID, w.requesterID, paymentKindTaskPayment, "pi_captured_"+w.taskID[:8]); err != nil {
		t.Fatalf("seed capture: %v", err)
	}

	code, body := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":10}`)
	if code != http.StatusConflict {
		t.Fatalf("adjust after capture: %d (%v), want 409", code, body)
	}
	if body["error"] != "already_captured" {
		t.Errorf("error = %v, want already_captured", body["error"])
	}
	// The admin is pointed at the only thing that CAN fix it during beta.
	if msg, _ := body["message"].(string); !strings.Contains(strings.ToLower(msg), "stripe") {
		t.Errorf("message does not point at the Stripe dashboard: %q", msg)
	}
	// And nothing moved.
	if rows := auditRows(t, w.taskID, "TIME_ADJUSTED"); len(rows) != 0 {
		t.Errorf("%d TIME_ADJUSTED audit rows on a refused adjustment", len(rows))
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

// forceStripeKey sets stripe.Key directly. initStripe is a sync.Once, so a
// t.Setenv alone cannot turn payments on inside a process where it has already
// run — and these tests need paymentsEnabled() true without a network call.
func forceStripeKey(t *testing.T, key string) {
	t.Helper()
	initStripe() // burn the sync.Once so it cannot overwrite what is set below
	prev := stripe.Key
	stripe.Key = key
	t.Cleanup(func() { stripe.Key = prev })
}

func setTaskBudget(t *testing.T, taskID string, cents int) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		update public.tasks
		   set prepay_amount_cents = $2, shopping_budget_approved_cents = $2
		 where id = $1::uuid
	`, taskID, cents); err != nil {
		t.Fatalf("set budget: %v", err)
	}
}

func taskStatus(t *testing.T, taskID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(context.Background(),
		`select status from public.tasks where id=$1::uuid`, taskID).Scan(&s); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func latchSet(t *testing.T, taskID, column string) bool {
	t.Helper()
	var set bool
	if err := db.QueryRow(context.Background(),
		fmt.Sprintf(`select %s is not null from public.tasks where id=$1::uuid`, column),
		taskID).Scan(&set); err != nil {
		t.Fatalf("read latch %s: %v", column, err)
	}
	return set
}

func countNotifications(t *testing.T, userID, ntype string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.notifications where user_id=$1::uuid and type=$2`,
		userID, ntype).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

func notificationBody(t *testing.T, userID, ntype string) string {
	t.Helper()
	var body string
	if err := db.QueryRow(context.Background(),
		`select body from public.notifications where user_id=$1::uuid and type=$2 order by created_at desc limit 1`,
		userID, ntype).Scan(&body); err != nil {
		t.Fatalf("read notification: %v", err)
	}
	return body
}

// waitFor polls a condition that a background goroutine satisfies. Short and
// bounded: the alternative is a sleep long enough to be reliable, which is
// always longer than this.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// callTaskHandler runs one task-scoped handler with the session identity the
// auth middleware would have set.
func callTaskHandler(t *testing.T, h gin.HandlerFunc, method, path, taskID, uid, email, body string, extra gin.Params) (int, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("{}")
	} else {
		reader = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = append(gin.Params{{Key: "id", Value: taskID}}, extra...)
	c.Set("uid", uid)
	c.Set("email", email)
	h(c)
	return w.Code, w
}

func decodeFirstJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	// completeTask answers by delegating to getTask, so a success body is the
	// task; some handlers write one object. Decoding with a stream reader
	// takes the first object and ignores anything appended after it.
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return map[string]any{}
	}
	return out
}

func getWorklogsAs(t *testing.T, taskID, uid, email string) map[string]any {
	t.Helper()
	code, w := callTaskHandler(t, getWorklogs, http.MethodGet, "/tasks/"+taskID+"/worklogs",
		taskID, uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("worklogs: %d (%s)", code, w.Body.String())
	}
	return decodeFirstJSON(t, w)
}

func completeAs(t *testing.T, taskID, uid string, body map[string]any, wantCode int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	code, w := callTaskHandler(t, completeTask, http.MethodPost, "/tasks/"+taskID+"/complete",
		taskID, uid, oldSupporterEmail, string(raw), nil)
	if code != wantCode {
		t.Fatalf("complete: %d (%s), want %d", code, w.Body.String(), wantCode)
	}
	return decodeFirstJSON(t, w)
}

func cancelAs(t *testing.T, taskID, uid, reason string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, cancelTask, http.MethodPost, "/tasks/"+taskID+"/cancel",
		taskID, uid, requesterEmail, `{"reason":"`+reason+`"}`, nil)
	return code, decodeFirstJSON(t, w)
}

func requestBudgetAs(t *testing.T, taskID, uid, body string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, requestBudgetIncrease, http.MethodPost,
		"/tasks/"+taskID+"/budget-increase", taskID, uid, oldSupporterEmail, body, nil)
	return code, decodeFirstJSON(t, w)
}

func requestTimeAs(t *testing.T, taskID, uid string, minutes int) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, requestTimeExtension, http.MethodPost,
		"/tasks/"+taskID+"/time-extension", taskID, uid, oldSupporterEmail,
		fmt.Sprintf(`{"requested_minutes":%d}`, minutes), nil)
	return code, decodeFirstJSON(t, w)
}

func requestTimeAsBody(t *testing.T, taskID, uid string, minutes int) (int, map[string]any) {
	t.Helper()
	code, body := requestTimeAs(t, taskID, uid, minutes)
	if code != http.StatusCreated {
		t.Fatalf("time request: %d (%v)", code, body)
	}
	return code, body
}

func resolveExtensionAs(t *testing.T, h gin.HandlerFunc, taskID, extID, uid string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, h, http.MethodPost,
		"/tasks/"+taskID+"/extensions/"+extID+"/approve", taskID, uid, requesterEmail, "",
		gin.Params{{Key: "eid", Value: extID}})
	return code, decodeFirstJSON(t, w)
}

func listExtensionsAs(t *testing.T, taskID, uid, email string) map[string]any {
	t.Helper()
	code, w := callTaskHandler(t, listTaskExtensions, http.MethodGet,
		"/tasks/"+taskID+"/extensions", taskID, uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("extensions: %d (%s)", code, w.Body.String())
	}
	return decodeFirstJSON(t, w)
}

// ── 7. What the requester is told about their hold ─────────────────────────

// The property that matters most here, and the one no amount of careful copy
// substitutes for: what somebody reserved, and on which card, is REQUESTER
// ONLY. A supporter reading the same task must not receive it — not as a
// field they happen not to render, but absent from the payload.
func TestPhase2bHoldIsVisibleToTheRequesterOnly(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)
	seedDisplayCard(t, paymentID, "visa", "4242")

	// Requester: the whole point of the change.
	code, requesterView := getTaskAs(t, w.taskID, w.requesterID, requesterEmail)
	if code != http.StatusOK {
		t.Fatalf("getTask as requester: %d", code)
	}
	payment, ok := requesterView["payment"].(map[string]any)
	if !ok {
		t.Fatalf("the requester cannot see their own hold: %v", requesterView["payment"])
	}
	if got := num(payment["authorized_cents"]); got != 7675 {
		t.Errorf("authorized = %s, want $76.75", formatCentsUSD(got))
	}
	if payment["card_brand"] != "visa" || payment["card_last4"] != "4242" {
		t.Errorf("card not named: brand=%v last4=%v", payment["card_brand"], payment["card_last4"])
	}
	if payment["status"] != paymentStatusAuthorized {
		t.Errorf("status = %v, want authorized", payment["status"])
	}

	// Supporter: absent, not empty. `omitempty` is what makes this a missing
	// key rather than a zeroed object a careless client would render as
	// "$0.00 reserved on".
	code, supporterView := getTaskAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if code != http.StatusOK {
		t.Fatalf("getTask as supporter: %d", code)
	}
	if _, present := supporterView["payment"]; present {
		t.Fatalf("the supporter can see the requester's hold: %v", supporterView["payment"])
	}
	// And nothing leaked in under another name.
	raw, _ := json.Marshal(supporterView)
	for _, secret := range []string{"7675", "4242", "card_last4", "authorized_cents"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("supporter payload contains %q: %s", secret, raw)
		}
	}
}

// A task with no hold — every task in the running beta — says nothing about
// money at all. The key is absent, so a client cannot mistake it for
// "$0.00 reserved", which would be a confident lie about somebody's card.
func TestPhase2bTaskWithNoHoldCarriesNoPaymentBlock(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	_, view := getTaskAs(t, w.taskID, w.requesterID, requesterEmail)
	if _, present := view["payment"]; present {
		t.Fatalf("a task with no hold reported a payment: %v", view["payment"])
	}
}

// Cancel has to answer three questions with real numbers: how much was held,
// how much was taken, how much went back.
func TestPhase2bCancelReportsWhatWasReleased(t *testing.T) {
	t.Run("nothing worked — the whole hold goes back", func(t *testing.T) {
		setupStripeWebhookDB(t)
		w := seedOpsWorld(t, "open")
		if _, err := db.Exec(context.Background(),
			`update public.tasks set assigned_to_id = null, assigned_to = '' where id=$1::uuid`,
			w.taskID); err != nil {
			t.Fatalf("unassign: %v", err)
		}
		paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)
		seedDisplayCard(t, paymentID, "visa", "4242")

		code, body := cancelAs(t, w.taskID, w.requesterID, "changed my mind")
		if code != http.StatusOK {
			t.Fatalf("cancel: %d (%v)", code, body)
		}
		if got := num(body["authorized_cents"]); got != 7675 {
			t.Errorf("authorized = %s, want $76.75", formatCentsUSD(got))
		}
		if got := num(body["captured_cents"]); got != 0 {
			t.Errorf("captured %s on a task nobody started", formatCentsUSD(got))
		}
		if got := num(body["released_cents"]); got != 7675 {
			t.Errorf("released = %s, want the whole $76.75", formatCentsUSD(got))
		}
		if body["card_last4"] != "4242" {
			t.Errorf("the confirmation cannot name the card: %v", body["card_last4"])
		}
	})

	t.Run("work logged — charged some, released the rest", func(t *testing.T) {
		setupStripeWebhookDB(t)
		w := seedOpsWorld(t, "open")
		seedWorklog(t, w.taskID, 40, false)
		seedAuthorizedHold(t, w.taskID, w.requesterID)

		code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed")
		if code != http.StatusOK {
			t.Fatalf("cancel: %d (%v)", code, body)
		}
		// base $12.00 + 25 billable x $0.50 = $24.50 owed. Stripe is not
		// configured in this test, so nothing actually moves and captured is 0
		// — what is under test is that the three numbers are reported and add
		// up, not Stripe's behaviour (payments_phase2b_smoke_test.go covers
		// that against the real API).
		if got := num(body["bill_cents"]); got != 2450 {
			t.Errorf("bill = %s, want $24.50", formatCentsUSD(got))
		}
		held := num(body["authorized_cents"])
		captured := num(body["captured_cents"])
		released := num(body["released_cents"])
		if held != 7675 {
			t.Errorf("authorized = %s, want $76.75", formatCentsUSD(held))
		}
		if captured+released != held {
			t.Errorf("the money does not add up: captured %s + released %s != held %s",
				formatCentsUSD(captured), formatCentsUSD(released), formatCentsUSD(held))
		}
	})
}

// A hold that never happened must not produce a confident number. With
// payments off the cancel still answers, and answers zero — which the clients
// read as "say nothing", not as "$0.00 released".
func TestPhase2bCancelWithNoHoldReportsZeroes(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	if _, err := db.Exec(context.Background(),
		`update public.tasks set assigned_to_id = null, assigned_to = '' where id=$1::uuid`,
		w.taskID); err != nil {
		t.Fatalf("unassign: %v", err)
	}

	code, body := cancelAs(t, w.taskID, w.requesterID, "changed my mind")
	if code != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", code, body)
	}
	for _, key := range []string{"authorized_cents", "captured_cents", "released_cents"} {
		if got := num(body[key]); got != 0 {
			t.Errorf("%s = %s on a task that never had a hold", key, formatCentsUSD(got))
		}
	}
	if _, present := body["card_last4"]; present {
		t.Errorf("a card was named for a hold that never existed: %v", body["card_last4"])
	}
}

func seedDisplayCard(t *testing.T, paymentID, brand, last4 string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`update public.payments set card_brand=$2, card_last4=$3 where id=$1::uuid`,
		paymentID, brand, last4); err != nil {
		t.Fatalf("seed display card: %v", err)
	}
}
