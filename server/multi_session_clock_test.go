package main

// Multi-session clock in/out: the backend property both clients' new "Clock
// back in" affordance rests on.
//
// The billing arithmetic over several sessions is already covered twice —
// TestBillingMultiSessionConsumesInclusionOnce (pure) and
// TestBillingMultiSessionSettlementAgainstDB (SQL) — but both of those SEED
// worklogs directly. Neither has ever called clockIn a second time, so
// "a supporter may clock back in" was, until this file, a property of the code
// as read rather than a property of the code as run.
//
// That distinction now matters: the supporter's screen offers the button.
//
//	docker run -d --rm --name hora-session-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55437:5432 supabase/postgres:15.8.1.060
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55437/horatest' \
//	  go test ./ -run MultiSession -v

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// clockAs calls the real handler the way the middleware would, so the guards
// under test are the ones that actually run in production — not a re-statement
// of them.
func clockAs(t *testing.T, handler gin.HandlerFunc, taskID, uid, email string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/clock", nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	handler(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func openSessionCount(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM public.worklogs WHERE task_id = $1::uuid AND end_at IS NULL`,
		taskID).Scan(&n); err != nil {
		t.Fatalf("count open sessions: %v", err)
	}
	return n
}

func sessionCount(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM public.worklogs WHERE task_id = $1::uuid`, taskID).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

// THE PROPERTY: clock in, clock out, clock in again — all through the real
// handlers, on one task, with the task never leaving 'open'.
//
// The only thing clockIn refuses is a SECOND OPEN session; a closed one is no
// obstacle, and a clock-out does not move the task out of 'open'. Which means
// the screen that went terminal at the first clock-out was the only thing
// standing between a supporter and a pause.
func TestMultiSessionClockBackInIsAccepted(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)

	code, body := clockAs(t, clockIn, w.taskID, w.supporterID, w.supporterEmail)
	if code != http.StatusCreated {
		t.Fatalf("first clock-in: want 201, got %d (%v)", code, body)
	}

	// The guard that DOES exist: no second session while one is open.
	code, body = clockAs(t, clockIn, w.taskID, w.supporterID, w.supporterEmail)
	if code != http.StatusBadRequest {
		t.Fatalf("clock-in while already clocked in: want 400, got %d (%v)", code, body)
	}
	if got, _ := body["error"].(string); got != "already clocked in" {
		t.Errorf("error = %q, want \"already clocked in\"", got)
	}
	if n := sessionCount(t, w.taskID); n != 1 {
		t.Fatalf("a refused clock-in still created a session (%d rows)", n)
	}

	if code, body := clockAs(t, clockOut, w.taskID, w.supporterID, w.supporterEmail); code != http.StatusOK {
		t.Fatalf("clock-out: want 200, got %d (%v)", code, body)
	}
	if n := openSessionCount(t, w.taskID); n != 0 {
		t.Fatalf("open sessions after clock-out = %d, want 0", n)
	}

	// A clock-out is a PAUSE, not an ending: the task is still open, and this
	// is the call the new "Clock back in" button makes.
	var status string
	if err := db.QueryRow(context.Background(),
		`SELECT status FROM public.tasks WHERE id = $1::uuid`, w.taskID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "open" {
		t.Fatalf("task status after clock-out = %q, want open", status)
	}

	code, body = clockAs(t, clockIn, w.taskID, w.supporterID, w.supporterEmail)
	if code != http.StatusCreated {
		t.Fatalf("CLOCK BACK IN: want 201, got %d (%v)", code, body)
	}
	if n := sessionCount(t, w.taskID); n != 2 {
		t.Errorf("sessions = %d, want 2", n)
	}
	if n := openSessionCount(t, w.taskID); n != 1 {
		t.Errorf("open sessions = %d, want exactly 1", n)
	}
}

// Four sessions, and the sum is what billing sees — through the handlers this
// time, so the path the supporter's thumb takes is the path that was measured.
//
// The number matters: 15 minutes are inside the base fee ONCE per task
// (billing.go billableMinutes), not once per session. Four sessions that each
// sit under the inclusion would be free if the rule were per-session, and the
// supporter would be working for nothing.
func TestMultiSessionMinutesSumAcrossClockBackIns(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	ctx := context.Background()

	// Each session is 10 minutes — comfortably under the 15-minute inclusion.
	for i := 0; i < 4; i++ {
		if code, body := clockAs(t, clockIn, w.taskID, w.supporterID, w.supporterEmail); code != http.StatusCreated {
			t.Fatalf("clock-in %d: want 201, got %d (%v)", i+1, code, body)
		}
		if code, body := clockAs(t, clockOut, w.taskID, w.supporterID, w.supporterEmail); code != http.StatusOK {
			t.Fatalf("clock-out %d: want 200, got %d (%v)", i+1, code, body)
		}
		// Give the session that just closed a real duration. Backdated against
		// its OWN end_at, not against now(): the handler stamps end_at at its
		// own now(), so subtracting from a later now() leaves a few
		// milliseconds over ten minutes, and the server's per-session ceil()
		// turns each of those into an eleventh minute. Which is the rounding
		// working correctly — and a reminder that the ceiling is per session.
		mustExecLive(t, `
			UPDATE public.worklogs SET start_at = end_at - interval '10 minutes'
			 WHERE id = (
			   SELECT id FROM public.worklogs
			    WHERE task_id = $1::uuid AND end_at IS NOT NULL
			    ORDER BY end_at DESC LIMIT 1)`, w.taskID)
	}

	total, err := totalClosedMinutes(ctx, w.taskID)
	if err != nil {
		t.Fatalf("totalClosedMinutes: %v", err)
	}
	if total != 40 {
		t.Fatalf("total minutes = %d, want 40 (four 10-minute sessions must sum)", total)
	}

	// The inclusion is consumed once: 40 logged → 25 billable.
	if got := billableMinutes(total); got != 25 {
		t.Errorf("billable = %d, want 25 — the 15-minute inclusion is being applied per session", got)
	}

	// And the gaps between the sessions cost nothing. The four sessions above
	// were opened minutes apart in wall-clock terms; only the time INSIDE them
	// is counted, which is the promise the paused banner makes to both parties.
	cost := calcTaskCostCents(ctx, w.taskID, total)
	want := Billing.BaseFeeDefaultCents + 25*Billing.PerMinuteRateCents
	if cost != want {
		t.Errorf("cost = %d, want %d ($12.00 base + 25 billable min x $0.50)", cost, want)
	}
}

// Geolocation rescopes to the NEW session: a ping refused during the pause is
// accepted again after the clock-back-in, and lands against the open worklog.
//
// The refusal half is already covered from the enroute side
// (TestLivePingsWithoutWorklogScopedToEnrouteWindow, "tapped, then clocked
// OUT"). This is the round trip the supporter's phone actually makes.
func TestMultiSessionGpsFollowsTheOpenSession(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)

	ping := func() (int, map[string]any) {
		return postGpsPing(t, w.taskID, w.supporterID, w.supporterEmail,
			`{"lat": 45.47, "lng": 9.19, "source": "foreground"}`)
	}

	if code, _ := clockAs(t, clockIn, w.taskID, w.supporterID, w.supporterEmail); code != http.StatusCreated {
		t.Fatal("clock-in failed")
	}
	if code, body := ping(); code != http.StatusOK {
		t.Fatalf("ping while clocked in: want 200, got %d (%v)", code, body)
	}

	// PAUSED. Nothing is accepted — the supporter is not on the clock, and
	// their position is not the requester's to watch.
	if code, _ := clockAs(t, clockOut, w.taskID, w.supporterID, w.supporterEmail); code != http.StatusOK {
		t.Fatal("clock-out failed")
	}
	if code, body := ping(); code != http.StatusForbidden {
		t.Fatalf("ping while paused: want 403, got %d (%v)", code, body)
	}
	if got := pingCount(t, w.taskID); got != 1 {
		t.Errorf("a paused ping was stored: %d rows, want 1", got)
	}

	// Clocked back in: accepted again, and scoped to the session that is open
	// now rather than to the closed one.
	if code, _ := clockAs(t, clockIn, w.taskID, w.supporterID, w.supporterEmail); code != http.StatusCreated {
		t.Fatal("clock back in failed")
	}
	if code, body := ping(); code != http.StatusOK {
		t.Fatalf("ping after clocking back in: want 200, got %d (%v)", code, body)
	}
	if got := pingCount(t, w.taskID); got != 2 {
		t.Errorf("stored pings = %d, want 2", got)
	}
}
