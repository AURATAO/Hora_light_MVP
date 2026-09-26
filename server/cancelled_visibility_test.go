package main

// A cancelled task, as its SUPPORTER sees it.
//
// THE BUILD 11 FINDING: it vanished. A cancel NULLs assigned_to_id so the dead
// task leaves the supporter's active list — and their history query matched on
// that same column and on status='completed', so a cancelled task fell out of
// both. The only route back was the notification deep link, which is
// dismissible and then gone. A requester has always kept their cancelled
// tasks; this is the same completeness for the other side.
//
// The cancellation billing policy makes it sharper still: a supporter can be
// PAID the base fee for a task they accepted and never clocked into, where no
// worklog exists to reach it through either. Every "is this person a party to
// this task" check has to read cancelled_assignee_id.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run CancelledVisibility -v

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The history list. Both shapes of cancelled task — worked and never clocked
// into — and the completed one that was always there.
func TestCancelledVisibilityInSupporterHistory(t *testing.T) {
	cases := []struct {
		name        string
		sessionMins int
	}{
		{"cancelled after working a session", 40},
		// The shape the billing policy created: paid, and no worklog anywhere.
		// Before cancelled_assignee_id there was nothing left tying this task
		// to the person it had just charged somebody for.
		{"cancelled having never clocked in", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupStripeWebhookDB(t)
			w := seedOpsWorld(t, "open")
			setAcceptedAt(t, w.taskID, time.Hour)
			if tc.sessionMins > 0 {
				seedWorklog(t, w.taskID, tc.sessionMins, false)
			}

			if code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed"); code != http.StatusOK {
				t.Fatalf("cancel: %d (%v)", code, body)
			}

			ids := doneTaskIDs(t, w.supporterID, oldSupporterEmail)
			if !contains(ids, w.taskID) {
				t.Fatal("the cancelled task is not in the supporter's history — the only way back to it is a dismissible notification")
			}
		})
	}
}

// Completed tasks did not stop appearing because cancelled ones started.
func TestCancelledVisibilityKeepsCompletedTasks(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusOK)

	ids := doneTaskIDs(t, w.supporterID, oldSupporterEmail)
	if !contains(ids, w.taskID) {
		t.Error("a completed task fell out of the supporter's history")
	}
}

// A cancelled task is history, not work. It must not come back as something
// still to do.
func TestCancelledVisibilityStaysOutOfTheActiveList(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setAcceptedAt(t, w.taskID, time.Hour)
	if code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed"); code != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", code, body)
	}

	code, rec := callTaskHandler(t, listAssignedTasks, http.MethodGet, "/tasks/assigned",
		"", w.supporterID, oldSupporterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("assigned: %d", code)
	}
	if contains(taskIDsFrom(t, rec.Body.String()), w.taskID) {
		t.Error("a cancelled task is sitting in the supporter's active list")
	}
}

// The read-only detail behind the history row: what the task was, when it was
// cancelled, and what they are paid for it.
func TestCancelledVisibilityOpensToAReadOnlyDetail(t *testing.T) {
	cases := []struct {
		name        string
		sessionMins int
		wantEarned  int
	}{
		// $12.00 base + 25 billable x $0.50.
		// $24.50 of service nets $19.60 after the 20% platform fee (D-14).
		{"worked a session", 40, 1960},
		// Paid the guarantee, no worklog at all — and no assignment either,
		// since the cancel detached them. Two of the three ways this handler
		// knows somebody is a party to a task are gone.
		// The $12.00 base fee is service too: $9.60 after the fee.
		{"never clocked in", 0, 960},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupStripeWebhookDB(t)
			w := seedOpsWorld(t, "open")
			setAcceptedAt(t, w.taskID, time.Hour)
			if tc.sessionMins > 0 {
				seedWorklog(t, w.taskID, tc.sessionMins, false)
			}
			if code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed"); code != http.StatusOK {
				t.Fatalf("cancel: %d (%v)", code, body)
			}

			// The task itself opens.
			task := taskDetailAs(t, w.taskID, w.supporterID, oldSupporterEmail, http.StatusOK)
			if got, _ := task["status"].(string); got != "cancelled" {
				t.Errorf("status = %q", got)
			}
			if task["cancelled_at"] == nil {
				t.Error("no cancellation timestamp — the detail cannot say WHEN")
			}

			// WHAT THEY ARE PAID. The whole reason the read-only view exists.
			settlement, ok := getWorklogsAs(t, w.taskID, w.supporterID, oldSupporterEmail)["settlement"].(map[string]any)
			if !ok {
				t.Fatal("the supporter cannot read the settlement for a task they were paid under")
			}
			earned, ok := settlement["earned"].(map[string]any)
			if !ok {
				t.Fatal("no `earned` block — the supporter is shown the requester's half of their own settlement")
			}
			if got := num(earned["total_cents"]); got != tc.wantEarned {
				t.Errorf("earned = %s, want %s", formatCentsUSD(got), formatCentsUSD(tc.wantEarned))
			}
		})
	}
}

// THE REASON, AND WHO IS ALLOWED TO READ WHICH VERSION OF IT.
//
// tasks.cancel_reason is free text written by three different callers, the ops
// panel among them, and it was relayed verbatim — "The requester cancelled the
// task. Reason: <anything>". An ops note written about a supporter is not a
// sentence to forward to them.
func TestCancelledVisibilityRelaysPresetLabelsOnly(t *testing.T) {
	cases := []struct {
		name string
		// What the client sends.
		reason     string
		reasonCode string
		// What the supporter may see, and what must never reach them.
		wantLabel   string
		wantInBody  string
		bannedInAll string
	}{
		{
			name: "a preset", reason: "Plans changed", reasonCode: "plans_changed",
			wantLabel: "Plans changed", wantInBody: "plans changed",
		},
		{
			// "Other" carries a free-text note that is for ops, not for the
			// supporter. No reason clause at all is the right answer.
			name: "other, with a note", reason: "my sister turned up and did it", reasonCode: "other",
			wantLabel: "", bannedInAll: "my sister turned up",
		},
		{
			// A shipped build 11 client, sending its own preset LABEL as free
			// text with no code. Mapped back rather than dropped, so the
			// relay keeps working without a client update.
			name: "a shipped client's label, no code", reason: "Changed my mind", reasonCode: "",
			wantLabel: "Plans changed", wantInBody: "plans changed",
		},
		{
			// Free text from a client that never had presets — web, before
			// this. Nothing is relayed, which is what it did before the
			// reason was ever forwarded.
			name: "arbitrary free text", reason: "supporter was rude, see ticket 412", reasonCode: "",
			wantLabel: "", bannedInAll: "ticket 412",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupStripeWebhookDB(t)
			w := seedOpsWorld(t, "open")
			setAcceptedAt(t, w.taskID, time.Hour)

			code, body := cancelWithCode(t, w.taskID, w.requesterID, tc.reason, tc.reasonCode)
			if code != http.StatusOK {
				t.Fatalf("cancel: %d (%v)", code, body)
			}

			// What the supporter's detail screen renders.
			task := taskDetailAs(t, w.taskID, w.supporterID, oldSupporterEmail, http.StatusOK)
			gotLabel, _ := task["cancel_reason_label"].(string)
			if gotLabel != tc.wantLabel {
				t.Errorf("cancel_reason_label = %q, want %q", gotLabel, tc.wantLabel)
			}

			// THE FREE TEXT NEVER CROSSES. Not in the label, not in the
			// notification, not anywhere the supporter can see.
			notification := notificationBody(t, w.supporterID, "CANCELLED")
			if tc.bannedInAll != "" {
				if strings.Contains(notification, tc.bannedInAll) {
					t.Errorf("free text reached the supporter's notification: %q", notification)
				}
				if strings.Contains(gotLabel, tc.bannedInAll) {
					t.Errorf("free text reached the supporter's task detail: %q", gotLabel)
				}
				// And the raw task payload carries none of it either — a
				// field the client happens not to render today is still a
				// field it could render tomorrow.
				raw, _ := json.Marshal(task)
				if strings.Contains(string(raw), tc.bannedInAll) {
					t.Errorf("free text is somewhere in the supporter's task payload: %s", raw)
				}
			}
			if tc.wantInBody != "" && !strings.Contains(notification, tc.wantInBody) {
				t.Errorf("the notification does not say why: %q", notification)
			}
			// And it is never the opaque line the build 11 supporter got.
			if notification == "The requester cancelled the task." && tc.wantInBody != "" {
				t.Error("the notification is back to saying nothing")
			}
		})
	}
}

// The requester keeps their own free text — it is theirs, and it is what the
// ops feed reads.
func TestCancelledVisibilityRequesterKeepsTheirOwnWords(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setAcceptedAt(t, w.taskID, time.Hour)

	const note = "my sister turned up and did it"
	if code, body := cancelWithCode(t, w.taskID, w.requesterID, note, "other"); code != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", code, body)
	}

	task := taskDetailAs(t, w.taskID, w.requesterID, requesterEmail, http.StatusOK)
	if got, _ := task["cancel_reason"].(string); got != note {
		t.Errorf("cancel_reason = %q, want the requester's own note back", got)
	}
	// And it is still on the row for ops, whoever else can or cannot see it.
	var stored string
	if err := db.QueryRow(context.Background(),
		`select coalesce(cancel_reason,'') from public.tasks where id=$1::uuid`, w.taskID).Scan(&stored); err != nil {
		t.Fatalf("read cancel_reason: %v", err)
	}
	if stored != note {
		t.Errorf("stored cancel_reason = %q, want the free text kept for ops", stored)
	}
}

// A stranger still cannot read a cancelled task. Widening the door for the
// supporter must not have taken it off its hinges.
func TestCancelledVisibilityStillRefusesStrangers(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setAcceptedAt(t, w.taskID, time.Hour)
	if code, body := cancelAs(t, w.taskID, w.requesterID, "plans changed"); code != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", code, body)
	}

	// A logged-in user who is neither party and never worked it.
	strangerID := seedUser(t, "stranger@example.com", "Sam Stranger", true)
	taskDetailAs(t, w.taskID, strangerID, "stranger@example.com", http.StatusForbidden)

	code, _ := callTaskHandler(t, getWorklogs, http.MethodGet, "/tasks/"+w.taskID+"/worklogs",
		w.taskID, strangerID, "stranger@example.com", "", nil)
	if code != http.StatusForbidden {
		t.Errorf("worklogs for a stranger: %d, want 403", code)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

func cancelWithCode(t *testing.T, taskID, uid, reason, reasonCode string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"reason": reason, "reason_code": reasonCode})
	code, w := callTaskHandler(t, cancelTask, http.MethodPost, "/tasks/"+taskID+"/cancel",
		taskID, uid, requesterEmail, string(body), nil)
	return code, decodeFirstJSON(t, w)
}

// taskDetailAs is getTaskAs (admin_remove_task_test.go) with the status
// assertion folded in, because every caller here asserts one.
func taskDetailAs(t *testing.T, taskID, uid, email string, wantCode int) map[string]any {
	t.Helper()
	code, body := getTaskAs(t, taskID, uid, email)
	if code != wantCode {
		t.Fatalf("getTask as %s: %d (%v), want %d", email, code, body, wantCode)
	}
	return body
}

func doneTaskIDs(t *testing.T, uid, email string) []string {
	t.Helper()
	code, w := callTaskHandler(t, listDoneTasks, http.MethodGet, "/tasks/done",
		"", uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("done: %d (%s)", code, w.Body.String())
	}
	return taskIDsFrom(t, w.Body.String())
}

func taskIDsFrom(t *testing.T, body string) []string {
	t.Helper()
	var payload struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	ids := make([]string, 0, len(payload.Items))
	for _, it := range payload.Items {
		ids = append(ids, it.ID)
	}
	return ids
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
