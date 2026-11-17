package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestStageLabel(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 17, 14, 0, 0, 0, ny)
	later := time.Date(2026, 11, 17, 17, 30, 0, 0, ny)
	tomorrow := time.Date(2026, 11, 18, 9, 0, 0, 0, ny)
	past := now.Add(-time.Hour)
	for _, tc := range []struct {
		name                       string
		enroute, working, immediat bool
		scheduled                  *time.Time
		want                       string
	}{
		{"just accepted", false, false, true, nil, "Accepted"},
		{"on the way", true, false, true, nil, "On my way"},
		// Clocked in wins: "On my way" stays set on the row after arrival.
		{"working", true, true, true, nil, "Working"},
		{"booked for later today", false, false, false, &later, "Accepted · starts 5:30 PM"},
		{"booked for tomorrow", false, false, false, &tomorrow, "Accepted · starts Wed 9:00 AM"},
		{"its start has passed", false, false, false, &past, "Accepted"},
	} {
		if got := stageLabel(tc.enroute, tc.working, tc.immediat, tc.scheduled, now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ── with Postgres ──────────────────────────────────────────────────────────

// A second task, already accepted by the world's supporter.
func seedHeldTask(t *testing.T, w opsWorld, title string, immediate bool, scheduled *time.Time) string {
	t.Helper()
	ctx := context.Background()
	for _, ddl := range []string{
		`alter table public.tasks add column if not exists accepted_at timestamptz`,
		`alter table public.tasks add column if not exists enroute_at timestamptz`,
		`alter table public.tasks add column if not exists is_immediate boolean`,
		`alter table public.tasks add column if not exists scheduled_at timestamptz`,
		`alter table public.tasks add column if not exists estimated_minutes integer`,
	} {
		if _, err := db.Exec(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	id := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.supporterID, oldSupporterEmail)
	if _, err := db.Exec(ctx, `
		update public.tasks set title = $2, is_immediate = $3, scheduled_at = $4,
		       estimated_minutes = 30, accepted_at = now()
		 where id = $1::uuid`, id, title, immediate, scheduled); err != nil {
		t.Fatal(err)
	}
	return id
}

func setTaskTiming(t *testing.T, taskID string, immediate bool, scheduled *time.Time) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`update public.tasks set is_immediate = $2, scheduled_at = $3, estimated_minutes = 30 where id = $1::uuid`,
		taskID, immediate, scheduled); err != nil {
		t.Fatal(err)
	}
}

// Busy now, and the next task is also for now: refused, by the title of the
// one in the way, and the task stays on the board.
func TestAcceptRefusesATaskThatOverlapsOneAlreadyHeld(t *testing.T) {
	w := setupAckDB(t)
	seedHeldTask(t, w, "Wait in line at the Apple Store", true, nil)
	setTaskTiming(t, w.taskID, true, nil)

	code, body := acceptWith(t, w, "")
	if code != http.StatusConflict || body["error"] != "task_overlap" {
		t.Fatalf("accept: %d %v, want 409 task_overlap", code, body)
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "Wait in line at the Apple Store") {
		t.Errorf("message %q should name the task in the way", msg)
	}
	var assigned *string
	if err := db.QueryRow(context.Background(), `select assigned_to_id::text from public.tasks where id = $1::uuid`, w.taskID).Scan(&assigned); err != nil || assigned != nil {
		t.Errorf("assigned_to_id = %v (%v), want still unassigned", assigned, err)
	}
}

// Holding a task for this evening is no reason to turn down one for now:
// this is an overlap rule, never a cap on how many tasks someone holds.
func TestAcceptAllowsATaskThatDoesNotOverlap(t *testing.T) {
	w := setupAckDB(t)
	tonight := time.Now().Add(6 * time.Hour)
	seedHeldTask(t, w, "Pick up a cake at 8", false, &tonight)
	setTaskTiming(t, w.taskID, true, nil)

	if code, body := acceptWith(t, w, ""); code != http.StatusOK {
		t.Fatalf("accept: %d %v, want 200", code, body)
	}
}

// App Review walks several demo tasks in a row with one login.
func TestAcceptOverlapSkipsTheReviewSandbox(t *testing.T) {
	w := setupAckDB(t)
	seedHeldTask(t, w, "Hold a place in line", true, nil)
	setTaskTiming(t, w.taskID, true, nil)
	if _, err := db.Exec(context.Background(),
		`update public.users set is_sandbox = true where id = any($1::uuid[])`, []string{w.supporterID, w.requesterID}); err != nil {
		t.Skipf("no is_sandbox in this fixture: %v", err)
	}
	if title, err := overlappingTaskTitle(context.Background(), w.supporterID, w.taskID); err != nil || title == "" {
		t.Fatalf("precondition: overlap = %q (%v), want one", title, err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+w.taskID+"/accept", strings.NewReader("{}"))
	if !acceptOverlapStep(c, w.taskID, w.supporterID) {
		t.Errorf("sandbox accept refused: %s", rec.Body.String())
	}
}

func TestTaskStageLabelsFromTheRows(t *testing.T) {
	w := setupAckDB(t)
	held := seedHeldTask(t, w, "Laundry run", true, nil)
	ctx := context.Background()
	stage := func() string {
		return taskStageLabels(ctx, w.supporterID, oldSupporterEmail, []string{held, w.taskID})[held]
	}
	if got := stage(); got != "Accepted" {
		t.Errorf("accepted: %q", got)
	}
	if _, err := db.Exec(ctx, `update public.tasks set enroute_at = now() where id = $1::uuid`, held); err != nil {
		t.Fatal(err)
	}
	if got := stage(); got != "On my way" {
		t.Errorf("en route: %q", got)
	}
	if _, err := db.Exec(ctx, `insert into public.worklogs (task_id, "user", start_at) values ($1::uuid, $2, now())`, held, oldSupporterEmail); err != nil {
		t.Fatal(err)
	}
	if got := stage(); got != "Working" {
		t.Errorf("clocked in: %q", got)
	}
	// A task that is not theirs has no label.
	if got := taskStageLabels(ctx, w.supporterID, oldSupporterEmail, []string{w.taskID}); len(got) != 0 {
		t.Errorf("unassigned task labelled: %v", got)
	}
}
