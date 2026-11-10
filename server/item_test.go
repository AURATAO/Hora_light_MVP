package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func reportItemAs(t *testing.T, taskID, uid, body string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, reportItemProblem, http.MethodPost,
		"/tasks/"+taskID+"/item-problem", taskID, uid, oldSupporterEmail, body, nil)
	return code, decodeFirstJSON(t, w)
}

// Out of stock, an alternative inside the budget: one item question, and "1"
// (approve) buys the alternative without moving the budget.
func TestItemProblemAlternativeApproved(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)

	code, body := reportItemAs(t, w.taskID, w.supporterID, `{
		"item_name": "oat  milk", "store_name": "Trader Joe's", "outcome": "alternative",
		"alternative_name": "almond milk", "alternative_cents": 449
	}`)
	if code != http.StatusCreated {
		t.Fatalf("report: %d (%v)", code, body)
	}
	if body["kind"] != extensionKindItem || body["item_name"] != "oat milk" || body["alternative_name"] != "almond milk" {
		t.Fatalf("row: %v", body)
	}
	if label, _ := body["reason_label"].(string); !strings.Contains(label, "Trader Joe's") || !strings.Contains(label, "$4.49") {
		t.Errorf("label %q", label)
	}
	if n := countNotifications(t, w.requesterID, "ITEM_ALTERNATIVE_PROPOSED"); n != 1 {
		t.Errorf("%d ITEM_ALTERNATIVE_PROPOSED notifications, want 1", n)
	}
	extID, _ := body["id"].(string)

	code, body = resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("approve: %d (%v)", code, body)
	}
	if got := num(body["approved_budget_cents"]); got != 2000 {
		t.Errorf("an item answer moved the budget: %s", formatCentsUSD(got))
	}
	req := body["request"].(map[string]any)
	if inst, _ := req["fallback_instruction"].(string); !strings.Contains(inst, "Buy almond milk") {
		t.Errorf("supporter instruction %q", inst)
	}
	waitFor(t, func() bool {
		return countNotifications(t, w.supporterID, "EXTENSION_RESOLVED") == 1 &&
			countNotifications(t, w.requesterID, "EXTENSION_RESOLVED") == 1
	}, "both parties told")

	// The record on the task says what was decided, in item words.
	recs := taskExtensionRecords(context.Background(), w.taskID)
	if len(recs) != 1 || recs[0].Kind != extensionKindItem || recs[0].Outcome != "Alternative chosen" {
		t.Errorf("records: %+v", recs)
	}
}

// "2" is skip it: declined, budget untouched, the supporter told to skip.
func TestItemProblemSkip(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	_, body := reportItemAs(t, w.taskID, w.supporterID, `{"item_name":"oat milk","outcome":"alternative","alternative_name":"almond milk","alternative_cents":449}`)
	extID, _ := body["id"].(string)
	code, body := resolveExtensionAs(t, denyExtension, w.taskID, extID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("deny: %d (%v)", code, body)
	}
	req := body["request"].(map[string]any)
	if inst, _ := req["fallback_instruction"].(string); inst != "Skip oat milk." {
		t.Errorf("instruction %q", inst)
	}
}

// No answer: the supporter decides, shown the requester's substitution
// preference from the task description.
func TestItemProblemExpiryShowsThePreference(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	if _, err := db.Exec(context.Background(),
		`update public.tasks set description = $2 where id = $1::uuid`, w.taskID,
		"Groceries from TJ's\n\nNote from the requester: Substitution preference: no dairy, otherwise similar is fine"); err != nil {
		t.Fatal(err)
	}
	_, body := reportItemAs(t, w.taskID, w.supporterID, `{"item_name":"oat milk","outcome":"alternative","alternative_name":"almond milk","alternative_cents":449}`)
	extID, _ := body["id"].(string)
	if _, err := db.Exec(context.Background(), `
		update public.extension_requests set created_at = now() - make_interval(mins => $2) where id = $1::uuid
	`, extID, Billing.ApprovalTimeoutMinutes+1); err != nil {
		t.Fatal(err)
	}
	list := listExtensionsAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	item := list["items"].([]any)[0].(map[string]any)
	if item["status"] != extensionStatusExpired {
		t.Fatalf("status %v", item["status"])
	}
	if pref := item["substitution_preference"]; pref != "no dairy, otherwise similar is fine" {
		t.Errorf("preference %q", pref)
	}
	if inst, _ := item["fallback_instruction"].(string); !strings.Contains(inst, "your call") {
		t.Errorf("instruction %q", inst)
	}
	waitFor(t, func() bool { return countNotifications(t, w.requesterID, "EXTENSION_RESOLVED") == 1 }, "requester told")
}

// More than the budget is never an item question: that is a budget ask.
func TestItemProblemOverBudgetIsRefused(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	code, body := reportItemAs(t, w.taskID, w.supporterID, `{"item_name":"oat milk","outcome":"alternative","alternative_name":"fancy milk","alternative_cents":2001}`)
	if code != http.StatusUnprocessableEntity || body["error"] != "over_budget" {
		t.Fatalf("over budget: %d (%v)", code, body)
	}
	var n int
	_ = db.QueryRow(context.Background(), `select count(*) from public.extension_requests where task_id = $1::uuid`, w.taskID).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows written for a refused report", n)
	}
}

// Nothing similar: a notice, no question, no row.
func TestItemProblemNothingSimilarIsANotice(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	code, body := reportItemAs(t, w.taskID, w.supporterID, `{"item_name":"oat milk","store_name":"Trader Joe's","outcome":"none"}`)
	if code != http.StatusOK {
		t.Fatalf("none: %d (%v)", code, body)
	}
	if got := notificationBody(t, w.requesterID, "ITEM_UNAVAILABLE"); got != "Trader Joe's is out of oat milk, and there's nothing similar. Your supporter will skip it." {
		t.Errorf("notice %q", got)
	}
	var n int
	_ = db.QueryRow(context.Background(), `select count(*) from public.extension_requests where task_id = $1::uuid`, w.taskID).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows for a notice", n)
	}
}

// Shopping tasks only, the assigned supporter only, a photo of this task only.
func TestItemProblemGuards(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	alt := `{"item_name":"oat milk","outcome":"alternative","alternative_name":"almond milk","alternative_cents":449}`
	if code, body := reportItemAs(t, w.taskID, w.supporterID, alt); code != http.StatusBadRequest || body["error"] != "not_a_shopping_task" {
		t.Errorf("no budget: %d (%v)", code, body)
	}
	setTaskBudget(t, w.taskID, 2000)
	if code, _ := reportItemAs(t, w.taskID, w.requesterID, alt); code != http.StatusForbidden {
		t.Errorf("requester reported: %d", code)
	}
	other := `{"item_name":"oat milk","outcome":"alternative","alternative_name":"almond milk","alternative_cents":449,
		"alternative_photo_url":"https://x.supabase.co/storage/v1/object/public/task-completions/completions/00000000-0000-0000-0000-000000000000/1.jpg"}`
	if code, body := reportItemAs(t, w.taskID, w.supporterID, other); code != http.StatusBadRequest || body["error"] != "invalid_photo" {
		t.Errorf("another task's photo: %d (%v)", code, body)
	}
	if code, _ := reportItemAs(t, w.taskID, w.supporterID, `{"item_name":"","outcome":"none"}`); code != http.StatusBadRequest {
		t.Errorf("no item name: %d", code)
	}
}

func TestSubstitutionPreference(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"Pick up groceries": "",
		"x\nNote from the requester: Substitution preference: ask me first\nNote from the requester: ring twice": "ask me first",
	} {
		if got := substitutionPreference(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
