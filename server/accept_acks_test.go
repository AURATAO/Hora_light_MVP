package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// ── the file ───────────────────────────────────────────────────────────────

func TestAcceptAcksFileParses(t *testing.T) {
	cfg, err := acceptAcks()
	if err != nil {
		t.Fatalf("accept_acks.json: %v", err)
	}
	// Every playbook has its own item, so a new errand type cannot ship
	// without deciding what a supporter is told ("other" is for words no
	// playbook matches, not for a type nobody wrote a text for).
	pbs, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range pbs.IDs() {
		if id == "repeat_multi" { // no match words: never a task's type
			continue
		}
		if _, ok := cfg.Items[id]; !ok {
			t.Errorf("playbook %q has no accept acknowledgment", id)
		}
	}
	// The pilot's rule (Aura 2026-11-16): the type's item is asked on every
	// accept; only the baseline is once.
	for key, it := range cfg.Items {
		want := ackAlways
		if key == ackBaseline {
			want = ackOnce
		}
		if it.When != want {
			t.Errorf("item %q: when = %q, want %q", key, it.When, want)
		}
	}
}

func TestParseAckConfigRefusesBrokenFiles(t *testing.T) {
	for name, body := range map[string]string{
		"no button":    `{"heading":"h","items":{}}`,
		"no baseline":  `{"heading":"h","button":"b","items":{"other":{"version":"1","when":"always","title":"t","body":"b"},"home_entry":{"version":"1","when":"always","title":"t","body":"b"}}}`,
		"no version":   `{"heading":"h","button":"b","items":{"baseline":{"when":"once","title":"t","body":"b"},"other":{"version":"1","when":"always","title":"t","body":"b"},"home_entry":{"version":"1","when":"always","title":"t","body":"b"}}}`,
		"unknown when": `{"heading":"h","button":"b","items":{"baseline":{"version":"1","when":"sometimes","title":"t","body":"b"},"other":{"version":"1","when":"always","title":"t","body":"b"},"home_entry":{"version":"1","when":"always","title":"t","body":"b"}}}`,
	} {
		if _, err := parseAckConfig([]byte(body)); err == nil {
			t.Errorf("%s: parsed, want an error", name)
		}
	}
}

func TestAckTypeKey(t *testing.T) {
	cfg, err := acceptAcks()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ category, title, desc, want string }{
		{"grocery", "Groceries from Trader Joe's", "", "groceries"},
		{"delivery", "Pick up dry cleaning", "", "laundry"},
		{"delivery", "Buy Advil at CVS", "", "pharmacy"},
		{"delivery", "Return a package at UPS", "", "packages"},
		{"queue", "Wait in line at the Apple Store", "", "queue"},
		{"queue", "Check if there is a line at Levain", "", "check"},
		{"delivery", "Get a key copied", "", "small_services"},
		{"delivery", "Coffee and a bagel", "", "food"},
		{"companionship", "A walk in the park", "", "companionship"},
		{"task", "Move a ladder", "", ackOther},
		// The title decides: a laundry task whose notes mention a package
		// is still laundry.
		{"laundry", "Laundry run", "Leave the package of detergent by the door.", "laundry"},
		// Only when the title says nothing is the description read.
		{"task", "Errand", "Please get milk and eggs.", "groceries"},
	} {
		if got := ackTypeKey(cfg, tc.category, tc.title, tc.desc); got != tc.want {
			t.Errorf("ackTypeKey(%q, %q) = %q, want %q", tc.category, tc.title, got, tc.want)
		}
	}
}

func TestAckEntersHome(t *testing.T) {
	pbs, _ := llm.Playbooks()
	note := pbs.ScopeRules.HomeEntry.SupporterNote
	if !ackEntersHome("Groceries", "Buy milk.\n\n"+note) {
		t.Error("a description carrying the home-entry note: want true")
	}
	if !ackEntersHome("Put the groceries away in my kitchen", "") {
		t.Error("words that send the supporter inside: want true")
	}
	if ackEntersHome("Pick up dry cleaning", "Hand it to the doorman.") {
		t.Error("a doorstep errand: want false")
	}
}

func TestMissingAcks(t *testing.T) {
	req := []AckItem{{Key: "baseline", Version: "2"}, {Key: "laundry", Version: "1"}}
	got := missingAcks(req, []ackSent{{Key: "baseline", Version: "1"}, {Key: "laundry", Version: "1"}, {Key: "made_up", Version: "9"}})
	if len(got) != 1 || got[0].Key != "baseline" {
		t.Errorf("missing = %+v, want only the baseline (an old version is not this version)", got)
	}
}

// ── with Postgres ──────────────────────────────────────────────────────────

func setupAckDB(t *testing.T) opsWorld {
	t.Helper()
	setupStripeWebhookDB(t)
	mig, err := os.ReadFile("../supabase/migrations/20261116120000_supporter_acknowledgments.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), string(mig)); err != nil {
		t.Fatalf("migration: %v", err)
	}
	w := seedOpsWorld(t, "open")
	unassign(t, w.taskID)
	if _, err := db.Exec(context.Background(),
		`update public.tasks set title = 'Pick up dry cleaning', category = 'laundry', description = '' where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	return w
}

func ackRows(t *testing.T, uid string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(),
		`select ack_key || '@' || version from public.supporter_acknowledgments where user_id = $1::uuid order by ack_key`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func requirementKeys(t *testing.T, taskID, uid string) []string {
	t.Helper()
	code, w := callTaskHandler(t, getAcceptRequirements, http.MethodGet, "/tasks/"+taskID+"/accept-requirements", taskID, uid, oldSupporterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("requirements: %d %s", code, w.Body.String())
	}
	body := decodeFirstJSON(t, w)
	if body["button"] == "" || body["heading"] == "" {
		t.Errorf("heading/button missing: %v", body)
	}
	var keys []string
	for _, raw := range body["items"].([]any) {
		it := raw.(map[string]any)
		if it["title"] == "" || it["body"] == "" || it["version"] == "" {
			t.Errorf("item without words: %v", it)
		}
		keys = append(keys, it["key"].(string))
	}
	return keys
}

func acceptWith(t *testing.T, w opsWorld, body string) (int, map[string]any) {
	t.Helper()
	code, rec := callTaskHandler(t, acceptTask, http.MethodPost, "/tasks/"+w.taskID+"/accept", w.taskID, w.supporterID, oldSupporterEmail, body, nil)
	return code, decodeFirstJSON(t, rec)
}

// The whole loop: asked for the baseline and the type; accepting records
// both; next time the baseline is gone and the type is asked again.
func TestAcceptAck_RequirementsRecordAndAskAgain(t *testing.T) {
	w := setupAckDB(t)
	if got := strings.Join(requirementKeys(t, w.taskID, w.supporterID), ","); got != "baseline,laundry" {
		t.Fatalf("first requirements = %s, want baseline,laundry", got)
	}
	code, body := acceptWith(t, w, `{"acknowledged":[{"key":"baseline","version":"1"},{"key":"laundry","version":"1"},{"key":"made_up","version":"1"}]}`)
	if code != http.StatusOK {
		t.Fatalf("accept: %d %v", code, body)
	}
	if got := strings.Join(ackRows(t, w.supporterID), ","); got != "baseline@1,laundry@1" {
		t.Errorf("recorded = %s, want baseline@1,laundry@1 (an item nobody asked for is not recorded)", got)
	}
	var hash string
	if err := db.QueryRow(context.Background(),
		`select text_sha256 from public.supporter_acknowledgments where user_id = $1::uuid and ack_key = 'laundry'`, w.supporterID).Scan(&hash); err != nil || len(hash) != 64 {
		t.Errorf("text hash = %q (%v), want a sha256", hash, err)
	}
	if got := strings.Join(requirementKeys(t, w.taskID, w.supporterID), ","); got != "laundry" {
		t.Errorf("second requirements = %s, want laundry (baseline is once, the type is every time)", got)
	}
}

// The web app sends nothing and must keep working until it shows the sheet.
func TestAcceptAck_NotEnforced_AcceptsWithoutAcknowledgments(t *testing.T) {
	w := setupAckDB(t)
	code, body := acceptWith(t, w, "")
	if code != http.StatusOK {
		t.Fatalf("accept with no body: %d %v, want 200", code, body)
	}
	if rows := ackRows(t, w.supporterID); len(rows) != 0 {
		t.Errorf("recorded %v, want nothing", rows)
	}
}

func TestAcceptAck_Enforced_RefusesThenAccepts(t *testing.T) {
	w := setupAckDB(t)
	t.Setenv("ACCEPT_ACK_ENFORCED", "1")
	code, body := acceptWith(t, w, `{"acknowledged":[{"key":"laundry","version":"1"}]}`)
	if code != http.StatusConflict || body["error"] != "acknowledgment_required" {
		t.Fatalf("accept without the baseline: %d %v, want 409 acknowledgment_required", code, body)
	}
	if items, _ := body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["key"] != "baseline" {
		t.Errorf("items = %v, want the missing baseline with its words", body["items"])
	}
	// Refused before the claim: the task is still on the board, nothing recorded.
	var assigned *string
	if err := db.QueryRow(context.Background(), `select assigned_to_id::text from public.tasks where id = $1::uuid`, w.taskID).Scan(&assigned); err != nil || assigned != nil {
		t.Fatalf("assigned_to_id = %v (%v), want still unassigned", assigned, err)
	}
	if rows := ackRows(t, w.supporterID); len(rows) != 0 {
		t.Errorf("recorded %v after a refusal, want nothing", rows)
	}
	code, body = acceptWith(t, w, `{"acknowledged":[{"key":"baseline","version":"1"},{"key":"laundry","version":"1"}]}`)
	if code != http.StatusOK {
		t.Fatalf("accept with both: %d %v", code, body)
	}
}

func TestAcceptAck_HomeEntryIsAddedOnTop(t *testing.T) {
	w := setupAckDB(t)
	if _, err := db.Exec(context.Background(),
		`update public.tasks set title = 'Groceries', category = 'grocery', description = 'Put the groceries away in my kitchen.' where id = $1::uuid`, w.taskID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requirementKeys(t, w.taskID, w.supporterID), ","); got != "baseline,groceries,home_entry" {
		t.Errorf("requirements = %s, want baseline,groceries,home_entry", got)
	}
}

// A task behind a block answers like one that is not there.
func TestAcceptAck_RequirementsHiddenAcrossABlock(t *testing.T) {
	w := setupAckDB(t)
	if _, err := db.Exec(context.Background(),
		`insert into public.user_blocks (blocker_id, blocked_id) values ($1::uuid, $2::uuid)`, w.requesterID, w.supporterID); err != nil {
		t.Skipf("no user_blocks in this fixture: %v", err)
	}
	code, _ := callTaskHandler(t, getAcceptRequirements, http.MethodGet, "/tasks/"+w.taskID+"/accept-requirements", w.taskID, w.supporterID, oldSupporterEmail, "", nil)
	if code != http.StatusNotFound {
		t.Errorf("requirements across a block: %d, want 404", code)
	}
}

// App Review walks the demo tasks (review_seed.go). Each one must show its
// own type's notes (the sheet is part of what is being reviewed) and carry
// a pay label, or the reviewer sees less than a supporter does.
func TestReviewSeedTasksShowTheSheetAndThePay(t *testing.T) {
	cfg, err := acceptAcks()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Pick up a coffee order": "food",
		"Hold a place in line":   "queue",
		"Drop off a package":     "packages",
	}
	for _, seed := range reviewSeedTasks {
		if got := ackTypeKey(cfg, seed.Category, seed.Title, seed.Description); got != want[seed.Title] {
			t.Errorf("%q: type = %q, want %q", seed.Title, got, want[seed.Title])
		}
		if ackEntersHome(seed.Title, seed.Description) {
			t.Errorf("%q: reads as entering a home; demo tasks are public places", seed.Title)
		}
		pay := estimatedSupporterPayCents(dispatchTask{Category: seed.Category, Minutes: seed.Minutes, RateCents: pricing().PerMinuteRateCents})
		if payLabel(pay, seed.Minutes) == "" {
			t.Errorf("%q: no pay label", seed.Title)
		}
	}
}
