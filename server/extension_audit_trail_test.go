package main

// The decision trail on a finished task.
//
// THE BUILD 11 FINDING: once a task completed, both parties lost every record
// of the mid-task asks. The requests live in extension_requests, the
// settlement view never rendered them, and the only surviving trace was a
// notification — dismissible, and gone the moment either of them cleared it.
//
// That is a gap between the money trail and the decision trail. The settlement
// already REFLECTS an approved budget increase in what was charged and an
// approved time extension in the ceiling it billed against; what it could not
// show was that anything had been asked, or who agreed to it. A requester
// looking at $8 more than they budgeted could see the number and not the
// approval they gave for it.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run ExtensionAuditTrail -v

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The trail, to both roles, with one line per ask and every outcome
// represented.
func TestExtensionAuditTrailSurvivesCompletion(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 40, false)
	ctx := context.Background()

	// 1. A time ask nobody answered, FIRST — it has to be aged past the
	// approval timeout to expire, and ageing a row moves it to the front of a
	// created_at ordering. Asking it first is the honest way to have it both
	// expired and oldest.
	//
	// Expired by the same lazy path a real one takes rather than by writing
	// 'expired' into the row: the status the record renders has to be the
	// status the mechanism actually produces.
	_, body := requestTimeAsBody(t, w.taskID, w.supporterID, 30)
	expiredID, _ := body["id"].(string)
	if _, err := db.Exec(ctx, `
		update public.extension_requests set created_at = now() - make_interval(mins => 30)
		 where id = $1::uuid`, expiredID); err != nil {
		t.Fatalf("age the request: %v", err)
	}
	expireAndAnnounce(ctx, w.taskID)

	// 2. A budget ask, approved.
	code, body := requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents":800,"reason":"price_higher","fallback":"buy_alternative","fallback_note":"the 500g jar"}`)
	if code != http.StatusCreated {
		t.Fatalf("budget request: %d (%v)", code, body)
	}
	approvedID, _ := body["id"].(string)
	if c, b := resolveExtensionAs(t, approveExtension, w.taskID, approvedID, w.requesterID); c != http.StatusOK {
		t.Fatalf("approve: %d (%v)", c, b)
	}

	// 3. A budget ask, denied.
	code, body = requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents":500,"reason":"extra_item","fallback":"skip_item"}`)
	if code != http.StatusCreated {
		t.Fatalf("second budget request: %d (%v)", code, body)
	}
	deniedID, _ := body["id"].(string)
	if c, b := resolveExtensionAs(t, denyExtension, w.taskID, deniedID, w.requesterID); c != http.StatusOK {
		t.Fatalf("deny: %d (%v)", c, b)
	}

	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 2500,
		"receipt_photo_url":    "https://example.test/receipt.jpg",
	}, http.StatusOK)

	// BOTH ROLES. The decision trail is not a receipt — it is the record of an
	// agreement, and an agreement has two parties.
	for _, who := range []struct{ name, uid, email string }{
		{"requester", w.requesterID, requesterEmail},
		{"supporter", w.supporterID, oldSupporterEmail},
	} {
		t.Run(who.name, func(t *testing.T) {
			records := settlementRequests(t, w.taskID, who.uid, who.email)
			if len(records) != 3 {
				t.Fatalf("%d request records, want 3 — the task view is not the record", len(records))
			}

			// Oldest first, so the trail reads in the order it happened.
			if records[0].ID != expiredID || records[1].ID != approvedID || records[2].ID != deniedID {
				t.Error("records are not in the order the requests were made")
			}

			// "No response", not "expired": expiry is our mechanism, not their
			// experience. From either side of it, the requester did not answer.
			expired := records[0]
			if expired.Outcome != "No response" {
				t.Errorf("outcome = %q, want \"No response\"", expired.Outcome)
			}
			if expired.Kind != "time" {
				t.Errorf("kind = %q, want time", expired.Kind)
			}
			if expired.RequestedMinutes == nil || *expired.RequestedMinutes != 30 {
				t.Errorf("requested_minutes = %v, want 30", expired.RequestedMinutes)
			}

			approved := records[1]
			if approved.Outcome != "Approved" {
				t.Errorf("outcome = %q, want Approved", approved.Outcome)
			}
			if approved.RequestedCents == nil || *approved.RequestedCents != 800 {
				t.Errorf("requested_cents = %v, want 800", approved.RequestedCents)
			}
			// The LABEL, never the slug. Nobody should be shown
			// "price_higher" on their own receipt.
			if approved.ReasonLabel != "Price higher than listed" {
				t.Errorf("reason_label = %q, want the label rather than the slug", approved.ReasonLabel)
			}
			if approved.ResolvedAt == nil {
				t.Error("an approved request has no decision timestamp")
			}

			if records[2].Outcome != "Denied" {
				t.Errorf("outcome = %q, want Denied", records[2].Outcome)
			}
		})
	}
}

// The fallback that RAN is carried on the statuses where it ran. "No response"
// on its own is half a sentence — the useful half is what happened next.
func TestExtensionAuditTrailCarriesTheFallbackThatRan(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 20, false)

	_, body := requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents":800,"reason":"item_unavailable","fallback":"buy_alternative","fallback_note":"the 500g jar"}`)
	deniedID, _ := body["id"].(string)
	if c, b := resolveExtensionAs(t, denyExtension, w.taskID, deniedID, w.requesterID); c != http.StatusOK {
		t.Fatalf("deny: %d (%v)", c, b)
	}
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
		"receipt_amount_cents": 0,
	}, http.StatusOK)

	records := settlementRequests(t, w.taskID, w.requesterID, requesterEmail)
	if len(records) != 1 {
		t.Fatalf("%d records, want 1", len(records))
	}
	// The supporter's own note, read back — "buy an alternative" with no note
	// is an instruction to guess, and the note is why the field exists.
	if records[0].Fallback == "" {
		t.Fatal("the record does not say what happened after the denial")
	}
	if want := "the 500g jar"; !strings.Contains(records[0].Fallback, want) {
		t.Errorf("fallback = %q, want it to name %q", records[0].Fallback, want)
	}
	// And an APPROVED request carries no fallback: nothing fell back.
	if records[0].Status != extensionStatusDenied {
		t.Fatalf("status = %q", records[0].Status)
	}
}

// A cancelled task keeps its trail too. The asks happened, and the settlement
// beside them is just as real.
func TestExtensionAuditTrailSurvivesCancellation(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setAcceptedAt(t, w.taskID, time.Hour)
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 25, false)

	_, body := requestTimeAsBody(t, w.taskID, w.supporterID, 15)
	extID, _ := body["id"].(string)
	if c, b := resolveExtensionAs(t, approveExtension, w.taskID, extID, w.requesterID); c != http.StatusOK {
		t.Fatalf("approve: %d (%v)", c, b)
	}
	if c, b := cancelAs(t, w.taskID, w.requesterID, "plans changed"); c != http.StatusOK {
		t.Fatalf("cancel: %d (%v)", c, b)
	}

	// The supporter, who by now has been DETACHED from the task, still reads
	// the trail for it.
	records := settlementRequests(t, w.taskID, w.supporterID, oldSupporterEmail)
	if len(records) != 1 {
		t.Fatalf("%d records on a cancelled task, want 1", len(records))
	}
	if records[0].Outcome != "Approved" {
		t.Errorf("outcome = %q", records[0].Outcome)
	}
}

// Most tasks never ask for anything. The section is absent, not empty — a
// "Requests" heading over nothing is a question the reader then has to answer.
func TestExtensionAuditTrailAbsentWhenNothingWasAsked(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	completeAs(t, w.taskID, w.supporterID, map[string]any{
		"completion_photo_url": "https://example.test/done.jpg",
	}, http.StatusOK)

	settlement := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["settlement"].(map[string]any)
	if _, present := settlement["requests"]; present {
		t.Error("a task nobody asked anything about carries an empty Requests section")
	}
}

// While a task is RUNNING, the live endpoint owns this surface — it carries
// the countdown and the fallback a supporter is actually waiting on. A second
// copy on the settlement would be two things to keep in step.
func TestExtensionAuditTrailIsFinishedTasksOnly(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	setTaskBudget(t, w.taskID, 2000)
	seedWorklog(t, w.taskID, 20, false)
	requestBudgetAs(t, w.taskID, w.supporterID,
		`{"requested_cents":800,"reason":"price_higher","fallback":"skip_item"}`)

	settlement := getWorklogsAs(t, w.taskID, w.requesterID, requesterEmail)["settlement"].(map[string]any)
	if _, present := settlement["requests"]; present {
		t.Error("the settlement is carrying a decision trail for a task that is still running")
	}
	// And the live surface does have it, so nothing was lost by leaving it
	// out above.
	live := listExtensionsAs(t, w.taskID, w.supporterID, oldSupporterEmail)
	if items, _ := live["items"].([]any); len(items) != 1 {
		t.Errorf("%d live items, want 1", len(items))
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

// settlementRequests decodes the trail out of the settlement payload, through
// the real handler, as a client would receive it.
func settlementRequests(t *testing.T, taskID, uid, email string) []ExtensionRecord {
	t.Helper()
	settlement := getWorklogsAs(t, taskID, uid, email)["settlement"].(map[string]any)
	raw, err := json.Marshal(settlement["requests"])
	if err != nil {
		t.Fatalf("marshal requests: %v", err)
	}
	var out []ExtensionRecord
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode requests: %v (%s)", err, raw)
	}
	return out
}
