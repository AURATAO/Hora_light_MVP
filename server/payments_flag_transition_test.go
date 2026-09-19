package main

// A task's payment obligations across a PAYMENTS_ENFORCED transition.
//
// THE BUG THIS SUITE EXISTS FOR, found on a device on 2026-09-19. A task
// posted while the flag was ON and completed after it went OFF demanded a
// receipt the app gave no way to enter, and completion became unreachable.
//
// THE RULE. The flag governs ONE thing: whether posting a NEW task requires a
// card and a hold. Every per-task decision after that — is a receipt owed, up
// to how much, what was held, what was settled — is a fact about the TASK, and
// must be answerable from the task's own state. A task in flight cannot have
// its obligations changed underneath it by an operator toggling an env var.
//
// These tests pin the server half of that rule: every source a client reads to
// decide whether to show the receipt step must agree with what completeTask
// enforces, on both sides of the transition. The client half is pinned in
// app/src/lib/taskBudget.test.mjs.
//
//	docker run -d --rm --name hora-race-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55437:5432 supabase/postgres:15.8.1.060
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55437/horatest' \
//	  go test ./ -run FlagTransition -v -count=1

import (
	"net/http"
	"os"
	"testing"
)

// taskBudgetSources reads every place a client can learn the approved budget,
// as the supporter — the party who has to produce the receipt.
type taskBudgetSources struct {
	extensions int
	settlement int
	task       int
}

func readTaskBudgetSources(t *testing.T, taskID, uid, email string) taskBudgetSources {
	t.Helper()
	var out taskBudgetSources

	code, rec := callTaskHandler(t, listTaskExtensions, http.MethodGet,
		"/tasks/"+taskID+"/extensions", taskID, uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("extensions: %d (%s)", code, rec.Body.String())
	}
	out.extensions = num(decodeFirstJSON(t, rec)["approved_budget_cents"])

	wl := getWorklogsAs(t, taskID, uid, email)
	st, _ := wl["settlement"].(map[string]any)
	out.settlement = num(st["approved_budget_cents"])

	code, rec = callTaskHandler(t, getTask, http.MethodGet, "/tasks/"+taskID,
		taskID, uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("task: %d (%s)", code, rec.Body.String())
	}
	out.task = num(decodeFirstJSON(t, rec)["shopping_budget_approved_cents"])

	return out
}

// The whole rule in one test: one task, the flag flipped underneath it, and
// every client-visible source still agreeing with what completion enforces.
func TestFlagTransitionShoppingTaskStaysCompletable(t *testing.T) {
	setupStripeWebhookDB(t)
	os.Setenv("PAYMENTS_ENFORCED", "1")
	defer os.Unsetenv("PAYMENTS_ENFORCED")

	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 30, false)

	on := readTaskBudgetSources(t, w.taskID, w.supporterID, oldSupporterEmail)
	if on.extensions != 2000 || on.settlement != 2000 || on.task != 2000 {
		t.Fatalf("with the flag ON the sources disagree: %+v, want 2000 everywhere", on)
	}

	// The operator turns payments off while this task is mid-flight.
	os.Setenv("PAYMENTS_ENFORCED", "")
	if paymentsEnforced() {
		t.Fatal("the flag did not actually go off")
	}

	off := readTaskBudgetSources(t, w.taskID, w.supporterID, oldSupporterEmail)
	if off != on {
		t.Fatalf("the flag moved a task's budget: %+v with it on, %+v with it off", on, off)
	}

	// The server still demands the receipt — correctly, because the task still
	// has a budget. The client must therefore still offer the field.
	body := completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusBadRequest)
	if body["error"] != "receipt_required" {
		t.Fatalf("error = %v, want receipt_required — the task's budget outlives the flag", body["error"])
	}
	if got := num(body["approved_budget_cents"]); got != off.task {
		t.Errorf("completion cites %d as the budget, the task reports %d — a client shown one "+
			"and refused by the other is exactly the stuck state", got, off.task)
	}

	// And the completion the app must be able to produce actually succeeds.
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 1200,
		"receipt_photo_url":    "https://example.test/receipt.jpg",
	}, http.StatusOK)
}

// The task's own field is the one a client is guaranteed to hold, so it has to
// be right on its own — not merely agree with the other two when all three are
// present. Read with NO extensions poll and NO worklogs fetch.
func TestFlagTransitionTaskCarriesItsOwnBudget(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	for _, party := range []struct {
		name, uid, email string
	}{
		{"supporter", w.supporterID, oldSupporterEmail},
		{"requester", w.requesterID, requesterEmail},
	} {
		code, rec := callTaskHandler(t, getTask, http.MethodGet, "/tasks/"+w.taskID,
			w.taskID, party.uid, party.email, "", nil)
		if code != http.StatusOK {
			t.Fatalf("%s: task read %d (%s)", party.name, code, rec.Body.String())
		}
		if got := num(decodeFirstJSON(t, rec)["shopping_budget_approved_cents"]); got != 2000 {
			t.Errorf("%s reads the task's budget as %d, want 2000 — this is the field the "+
				"completion form falls back to when nothing else has loaded", party.name, got)
		}
	}
}

// An approved mid-task increase raises the ceiling, and the task's own field
// has to move with it — otherwise a supporter sent shopping on a raised budget
// falls back to a stale ceiling and is refused for claiming what was approved.
func TestFlagTransitionApprovedIncreaseMovesTheTaskField(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 30, false)

	code, body := requestBudgetAs(t, w.taskID, w.supporterID, `{
		"requested_cents": 1500,
		"reason": "the only jar left was the bigger one",
		"fallback": "buy_alternative",
		"fallback_note": "the 500g jar instead"
	}`)
	if code != http.StatusCreated {
		t.Fatalf("budget request: %d (%v)", code, body)
	}
	extID, _ := body["id"].(string)
	if extID == "" {
		t.Fatal("no request id returned")
	}
	if code, body = resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID); code != http.StatusOK {
		t.Fatalf("approve: %d (%v)", code, body)
	}

	got := readTaskBudgetSources(t, w.taskID, w.supporterID, oldSupporterEmail)
	if got.task != 3500 || got.extensions != 3500 || got.settlement != 3500 {
		t.Fatalf("after an approved increase the sources read %+v, want 3500 everywhere", got)
	}
}
