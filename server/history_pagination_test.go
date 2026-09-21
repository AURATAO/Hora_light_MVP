package main

// History counts and pagination.
//
// The history lists now render three rows and a "See all (N)" link, and N is
// a number only the database has: the lists are keyset-paginated, and a cursor
// describes where the next page starts, not how many rows exist. A wrong N is
// worse than none — it is a promise about what is behind the link.
//
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55440/horatest' \
//	  go test ./ -run HistoryPagination -v

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// The count is of the HISTORY, not of the page — so it must not move when the
// caller asks for fewer rows, and must not be capped by the limit.
func TestHistoryPaginationTotalIsIndependentOfPageSize(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	ctx := context.Background()

	// Seven finished tasks for the supporter, and one still live — which must
	// NOT be counted: it is not history.
	for i := 0; i < 7; i++ {
		seedClosedTaskFor(t, w, i, "completed")
	}

	for _, limit := range []int{1, 3, 10, 50} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			page := doneTasksPage(t, w.supporterID, oldSupporterEmail, limit, nil)
			if page.Total != 7 {
				t.Errorf("total = %d at limit %d, want 7 — the count followed the page size",
					page.Total, limit)
			}
			want := limit
			if want > 7 {
				want = 7
			}
			if len(page.Items) != want {
				t.Errorf("%d items at limit %d, want %d", len(page.Items), limit, want)
			}
		})
	}

	// And the live task the supporter is still on is not in the history it is
	// counting — that row belongs in the active list above it.
	page := doneTasksPage(t, w.supporterID, oldSupporterEmail, 50, nil)
	for _, it := range page.Items {
		if it.ID == w.taskID {
			t.Error("the still-open task is being counted as history")
		}
	}
	_ = ctx
}

// Walking the cursor reaches every row exactly once, and the total stays put
// the whole way down.
func TestHistoryPaginationCursorWalksTheWholeHistory(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	for i := 0; i < 7; i++ {
		seedClosedTaskFor(t, w, i, "completed")
	}

	seen := map[string]int{}
	var cursor *cursorParams
	for pages := 0; pages < 10; pages++ {
		page := doneTasksPage(t, w.supporterID, oldSupporterEmail, 3, cursor)
		if page.Total != 7 {
			t.Fatalf("total = %d mid-walk, want 7", page.Total)
		}
		for _, it := range page.Items {
			seen[it.ID]++
		}
		if page.Next == nil {
			break
		}
		cursor = &cursorParams{
			BeforeCreatedAt: page.Next.BeforeCreatedAt,
			BeforeID:        page.Next.BeforeID,
		}
	}

	if len(seen) != 7 {
		t.Errorf("walked %d distinct tasks, want 7 — the cursor skipped rows", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("task %s came back %d times — the cursor repeated a row", id, n)
		}
	}
}

// A cancelled task is history too, and the supporter it was detached from
// still counts it. This is the row the old query lost entirely.
func TestHistoryPaginationCountsCancelledTasks(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedClosedTaskFor(t, w, 0, "completed")
	seedClosedTaskFor(t, w, 1, "cancelled")

	page := doneTasksPage(t, w.supporterID, oldSupporterEmail, 10, nil)
	if page.Total != 2 {
		t.Errorf("total = %d, want 2 — a cancelled task is history and the supporter was paid for it",
			page.Total)
	}
}

// The requester's half of the same question.
func TestHistoryPaginationPostedClosedCarriesATotal(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	for i := 0; i < 5; i++ {
		seedClosedTaskFor(t, w, i, "completed")
	}

	code, rec := callTaskHandler(t, listMyPostedClosed, http.MethodGet,
		"/tasks/posted/closed?limit=2", "", w.requesterID, requesterEmail, "", nil)
	if code != http.StatusOK {
		t.Fatalf("posted/closed: %d", code)
	}
	var page taskPageEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 5 {
		t.Errorf("total = %d, want 5", page.Total)
	}
	if len(page.Items) != 2 {
		t.Errorf("%d items, want the 2 that were asked for", len(page.Items))
	}
}

// Nothing finished yet: zero, and no cursor. The clients render no "See all"
// at all rather than "See all (0)".
func TestHistoryPaginationEmptyHistoryIsZero(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	page := doneTasksPage(t, w.supporterID, oldSupporterEmail, 10, nil)
	if page.Total != 0 {
		t.Errorf("total = %d on a supporter who has finished nothing, want 0", page.Total)
	}
	if page.Next != nil {
		t.Error("an empty history handed back a cursor")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

type cursorParams struct {
	BeforeCreatedAt string
	BeforeID        string
}

type taskPageEnvelope struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
	Next *struct {
		BeforeCreatedAt string `json:"before_created_at"`
		BeforeID        string `json:"before_id"`
	} `json:"next"`
	Total int `json:"total"`
}

func doneTasksPage(t *testing.T, uid, email string, limit int, cursor *cursorParams) taskPageEnvelope {
	t.Helper()
	path := fmt.Sprintf("/tasks/done?limit=%d", limit)
	if cursor != nil {
		// ESCAPED. An RFC3339 timestamp from a +02:00 database carries a "+",
		// which a query string decodes as a space — the cursor then fails to
		// parse, the handler silently ignores it, and every page is page one.
		path += fmt.Sprintf("&before_created_at=%s&before_id=%s",
			url.QueryEscape(cursor.BeforeCreatedAt), url.QueryEscape(cursor.BeforeID))
	}
	code, rec := callTaskHandler(t, listDoneTasks, http.MethodGet, path, "", uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("done: %d (%s)", code, rec.Body.String())
	}
	var page taskPageEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return page
}

// seedClosedTaskFor writes one finished task shared by the world's requester
// and supporter. `i` staggers created_at so the keyset ordering is total and
// the cursor walk is deterministic.
func seedClosedTaskFor(t *testing.T, w opsWorld, i int, status string) string {
	t.Helper()
	var id string
	assignee := "$4::uuid"
	cancelledAssignee := "null"
	if status == "cancelled" {
		// A cancel detaches the supporter and records them instead — the exact
		// shape listDoneTasks has to still find.
		assignee = "null"
		cancelledAssignee = "$4::uuid"
	}
	q := fmt.Sprintf(`
		insert into public.tasks
			(title, description, category, location_text, estimated_minutes,
			 requester, requester_id, assigned_to, assigned_to_id, cancelled_assignee_id,
			 status, created_at)
		values ($1, 'seeded', 'quick_errand', 'Somewhere | NYC', 30,
			 $2, $3::uuid, 'old.supporter@example.com', %s, %s,
			 $5, now() - make_interval(mins => $6))
		returning id::text`, assignee, cancelledAssignee)
	if err := db.QueryRow(context.Background(), q,
		fmt.Sprintf("History task %d", i), requesterEmail, w.requesterID, w.supporterID,
		status, (i+1)*10,
	).Scan(&id); err != nil {
		t.Fatalf("seed closed task: %v", err)
	}
	_ = time.Now
	return id
}
