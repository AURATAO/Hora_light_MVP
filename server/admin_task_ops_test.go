package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Coverage for the three ops-panel task actions against a real Postgres.
//
// The acceptance bar for every one of these is an audit_logs row. That is not
// ceremony: these actions ran as SECURITY DEFINER Postgres functions for the
// project's entire life and never once succeeded, because the functions guard
// on auth.jwt() and Go connects as `postgres` with no JWT. A failed write is
// silent, so the only way anyone could have noticed was the absence of audit
// rows — and nobody was looking. Production had nine TASK_REMOVED rows and zero
// FORCE_COMPLETED / CANCELLED / TIME_ADJUSTED when this was found.
//
// So each happy-path test asserts the row exists, carries the right action, and
// records the *admin from the session* as actor_id. These tests also stand as
// the regression net for the original bug: nothing in this test harness sets a
// JWT, so a handler that reached for auth.uid() again would fail here.
//
//	docker run -d --rm --name hora-ops-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run AdminOps -v

// Mirrors reassignFixture, plus worklogs.updated_at — which production has and
// the older fixtures omit, and which these handlers write.
const adminOpsFixture = `
DROP TABLE IF EXISTS public.audit_logs CASCADE;
DROP TABLE IF EXISTS public.device_push_tokens CASCADE;
DROP TABLE IF EXISTS public.notifications CASCADE;
DROP TABLE IF EXISTS public.worklogs CASCADE;
DROP TABLE IF EXISTS public.profiles CASCADE;
DROP TABLE IF EXISTS public.tasks CASCADE;
DROP TABLE IF EXISTS public.users CASCADE;
DROP TYPE IF EXISTS public.notification_type;

CREATE TYPE public.notification_type AS ENUM (
	'ORDER_ACCEPTED', 'CLOCK_IN', 'CLOCK_OUT', 'CANCELLED', 'COMPLETED'
);

CREATE TABLE public.users (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	email text,
	name text
);

CREATE TABLE public.profiles (
	id uuid,
	email text PRIMARY KEY,
	name text,
	city text,
	is_verified_supporter boolean DEFAULT false
);

CREATE TABLE public.tasks (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	title text NOT NULL DEFAULT '',
	description text NOT NULL DEFAULT '',
	category text NOT NULL DEFAULT 'task',
	location_text text NOT NULL DEFAULT '',
	estimated_minutes integer NOT NULL DEFAULT 30,
	prepay_amount_cents integer NOT NULL DEFAULT 0,
	is_immediate boolean NOT NULL DEFAULT false,
	scheduled_at timestamptz,
	requester text NOT NULL DEFAULT '',
	status text NOT NULL DEFAULT 'open',
	assigned_to text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	requester_id uuid REFERENCES public.users(id),
	assigned_to_id uuid REFERENCES public.users(id),
	cancelled_at timestamptz,
	cancel_reason text,
	completed_at timestamptz,
	completion_photo_url text,
	completion_note text,
	travel_time_minutes integer,
	total_estimate_minutes integer,
	CONSTRAINT tasks_status_check CHECK ((status = ANY (ARRAY['open'::text, 'completed'::text, 'cancelled'::text])))
);

CREATE TABLE public.worklogs (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	task_id uuid REFERENCES public.tasks(id),
	"user" text,
	start_at timestamptz NOT NULL DEFAULT now(),
	end_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.notifications (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id uuid NOT NULL,
	task_id uuid NOT NULL,
	type public.notification_type NOT NULL,
	title text NOT NULL,
	body text NOT NULL,
	unread boolean NOT NULL DEFAULT true,
	via_email boolean NOT NULL DEFAULT false,
	email_sent_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.audit_logs (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	job_id uuid NOT NULL,
	actor_id uuid NOT NULL,
	action text NOT NULL,
	reason text,
	meta jsonb NOT NULL DEFAULT '{}'::jsonb,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.device_push_tokens (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
	expo_push_token text NOT NULL UNIQUE,
	platform text,
	created_at timestamptz NOT NULL DEFAULT now(),
	last_seen_at timestamptz NOT NULL DEFAULT now()
);
`

func setupAdminOpsDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed ops action tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, adminOpsFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, path := range reassignMigrationPaths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("migration %s: %v", path, err)
		}
	}

	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	stdDB, err := sql.Open("pgx", stdlib.RegisterConnConfig(cfg))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	prevPool, prevSQL := db, sqldb
	db, sqldb = pool, stdDB
	t.Cleanup(func() {
		_ = stdDB.Close()
		pool.Close()
		db, sqldb = prevPool, prevSQL
	})
}

// opsWorld is an admin, a requester, a supporter, and a task they share.
type opsWorld struct {
	adminID     string
	requesterID string
	supporterID string
	taskID      string
}

func seedOpsWorld(t *testing.T, status string) opsWorld {
	t.Helper()
	w := opsWorld{
		adminID:     seedUser(t, adminEmail, "Ops Admin", false),
		requesterID: seedUser(t, requesterEmail, "Rita Requester", false),
		supporterID: seedUser(t, oldSupporterEmail, "Otto Supporter", true),
	}
	w.taskID = seedReassignTask(t, status, w.requesterID, requesterEmail, w.supporterID, oldSupporterEmail)
	return w
}

// seedWorklog adds a session. minutesAgo is when it started; openEnded leaves
// the supporter on the clock.
func seedWorklog(t *testing.T, taskID string, minutesAgo int, openEnded bool) string {
	t.Helper()
	var id string
	q := `INSERT INTO public.worklogs (task_id, "user", start_at, end_at)
	      VALUES ($1::uuid, $2, now() - make_interval(mins => $3), now())
	      RETURNING id::text`
	if openEnded {
		q = `INSERT INTO public.worklogs (task_id, "user", start_at, end_at)
		     VALUES ($1::uuid, $2, now() - make_interval(mins => $3), NULL)
		     RETURNING id::text`
	}
	if err := db.QueryRow(context.Background(), q, taskID, oldSupporterEmail, minutesAgo).Scan(&id); err != nil {
		t.Fatalf("seed worklog: %v", err)
	}
	return id
}

// callAdminOps runs the real route chain: the admin middleware then the
// handler, with the session identity gin's auth middleware would have set.
func callAdminOps(t *testing.T, h gin.HandlerFunc, action, taskID, uid, email, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/tasks/"+taskID+"/"+action, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	requireOpsAdmin()(c)
	if !c.IsAborted() {
		h(c)
	}

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// auditRows returns every audit row for a task+action, newest last.
func auditRows(t *testing.T, taskID, action string) []struct {
	ActorID string
	Reason  string
	Meta    string
} {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		SELECT actor_id::text, coalesce(reason,''), meta::text
		FROM public.audit_logs WHERE job_id=$1::uuid AND action=$2
		ORDER BY created_at
	`, taskID, action)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	var out []struct {
		ActorID string
		Reason  string
		Meta    string
	}
	for rows.Next() {
		var r struct {
			ActorID string
			Reason  string
			Meta    string
		}
		if err := rows.Scan(&r.ActorID, &r.Reason, &r.Meta); err != nil {
			t.Fatalf("audit scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// ── Force complete ─────────────────────────────────────────────────────────

// The acceptance test. Under the old implementation this produced no audit row,
// no status change, and a 500.
func TestAdminOpsForceCompleteWritesAuditRow(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, true) // supporter still on the clock

	code, out := callAdminOps(t, adminForceCompleteTask, "force-complete", w.taskID, w.adminID, adminEmail, `{}`)
	if code != http.StatusOK {
		t.Fatalf("force-complete: want 200, got %d (%v)", code, out)
	}

	audit := auditRows(t, w.taskID, "FORCE_COMPLETED")
	if len(audit) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1 — this is the bar the old implementation never met", len(audit))
	}
	// actor_id from the session, not auth.uid(). The old SQL function inserted
	// auth.uid(), which is null on Go's connection — the NOT NULL column made
	// that a hard failure.
	if audit[0].ActorID != w.adminID {
		t.Errorf("audit actor_id = %s, want the acting admin %s", audit[0].ActorID, w.adminID)
	}
	if !strings.Contains(audit[0].Meta, adminEmail) {
		t.Errorf("audit meta does not record the admin: %s", audit[0].Meta)
	}

	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, w.taskID); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
	// completed_at is set — the old SQL function never set it, so a force-completed
	// task was indistinguishable from one completed at the epoch.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND completed_at IS NOT NULL`, w.taskID); n != 1 {
		t.Error("completed_at was not set")
	}
	// The running timer is stopped.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.worklogs WHERE task_id=$1::uuid AND end_at IS NULL`, w.taskID); n != 0 {
		t.Error("an open work session survived the force-complete")
	}
	if out["closed_worklog_sessions"] != float64(1) {
		t.Errorf("closed_worklog_sessions = %v, want 1", out["closed_worklog_sessions"])
	}

	// Both parties told.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid AND user_id=$2::uuid AND type='COMPLETED'`,
		w.taskID, w.requesterID); n != 1 {
		t.Error("requester was not notified")
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid AND user_id=$2::uuid AND type='COMPLETED_SUPPORTER'`,
		w.taskID, w.supporterID); n != 1 {
		t.Error("supporter was not notified")
	}
}

func TestAdminOpsForceCompleteIsIdempotent(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 20, false)

	if code, out := callAdminOps(t, adminForceCompleteTask, "force-complete", w.taskID, w.adminID, adminEmail, `{}`); code != http.StatusOK {
		t.Fatalf("first call: %d (%v)", code, out)
	}
	code, out := callAdminOps(t, adminForceCompleteTask, "force-complete", w.taskID, w.adminID, adminEmail, `{}`)
	if code != http.StatusOK || out["already_completed"] != true {
		t.Fatalf("second call: want 200 already_completed, got %d (%v)", code, out)
	}
	// A double click must not produce a second audit row or a second round of
	// notifications.
	if n := len(auditRows(t, w.taskID, "FORCE_COMPLETED")); n != 1 {
		t.Errorf("audit rows after two calls = %d, want 1", n)
	}
	if n := scalar[int](t, `SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid`, w.taskID); n != 2 {
		t.Errorf("notifications after two calls = %d, want 2", n)
	}
}

func TestAdminOpsForceCompleteRejectsCancelledTask(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "cancelled")

	code, out := callAdminOps(t, adminForceCompleteTask, "force-complete", w.taskID, w.adminID, adminEmail, `{}`)
	if code != http.StatusBadRequest || out["error"] != "task_not_completable" {
		t.Fatalf("want 400 task_not_completable, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, "FORCE_COMPLETED")); n != 0 {
		t.Errorf("a refused action still wrote %d audit rows", n)
	}
}

func TestAdminOpsForceCompleteRejectsNonAdmin(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")

	code, out := callAdminOps(t, adminForceCompleteTask, "force-complete", w.taskID, w.requesterID, requesterEmail, `{}`)
	if code != http.StatusForbidden || out["error"] != "admin_only" {
		t.Fatalf("want 403 admin_only, got %d (%v)", code, out)
	}
	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, w.taskID); got != "open" {
		t.Errorf("a non-admin changed status to %q", got)
	}
	if n := len(auditRows(t, w.taskID, "FORCE_COMPLETED")); n != 0 {
		t.Error("a non-admin call wrote an audit row")
	}
}

// ── Admin cancel ───────────────────────────────────────────────────────────

func TestAdminOpsCancelWritesAuditRow(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 15, true)

	code, out := callAdminOps(t, adminCancelTaskHandler, "cancel", w.taskID, w.adminID, adminEmail,
		`{"reason":"requester phoned in"}`)
	if code != http.StatusOK {
		t.Fatalf("cancel: want 200, got %d (%v)", code, out)
	}

	audit := auditRows(t, w.taskID, "CANCELLED")
	if len(audit) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(audit))
	}
	if audit[0].ActorID != w.adminID {
		t.Errorf("audit actor_id = %s, want %s", audit[0].ActorID, w.adminID)
	}
	if audit[0].Reason != "requester phoned in" {
		t.Errorf("audit reason = %q", audit[0].Reason)
	}

	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, w.taskID); got != "cancelled" {
		t.Errorf("status = %q, want cancelled", got)
	}
	if got := scalar[string](t, `SELECT coalesce(cancel_reason,'') FROM public.tasks WHERE id=$1::uuid`, w.taskID); got != "requester phoned in" {
		t.Errorf("cancel_reason = %q", got)
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND cancelled_at IS NOT NULL`, w.taskID); n != 1 {
		t.Error("cancelled_at was not set")
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.worklogs WHERE task_id=$1::uuid AND end_at IS NULL`, w.taskID); n != 0 {
		t.Error("an open work session survived the cancel")
	}
	// Requester and supporter both told.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid AND type='CANCELLED'`, w.taskID); n != 2 {
		t.Errorf("cancellation notifications = %d, want 2", n)
	}
}

// A cancel with no reason is allowed — the reason is optional in the ops panel.
func TestAdminOpsCancelWithoutReason(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")

	code, out := callAdminOps(t, adminCancelTaskHandler, "cancel", w.taskID, w.adminID, adminEmail, `{"reason":""}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, "CANCELLED")); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND cancel_reason IS NULL`, w.taskID); n != 1 {
		t.Error("an empty reason should store NULL, not an empty string")
	}
}

func TestAdminOpsCancelRejectsNonAdmin(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")

	code, out := callAdminOps(t, adminCancelTaskHandler, "cancel", w.taskID, w.supporterID, oldSupporterEmail, `{}`)
	if code != http.StatusForbidden || out["error"] != "admin_only" {
		t.Fatalf("want 403 admin_only, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, "CANCELLED")); n != 0 {
		t.Error("a non-admin call wrote an audit row")
	}
}

// ── Adjust time ────────────────────────────────────────────────────────────

func TestAdminOpsAdjustTimeWritesAuditRow(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	worklogID := seedWorklog(t, w.taskID, 30, false) // a closed 30-minute session

	before := scalar[int](t, `
		SELECT ceil(extract(epoch from (end_at - start_at))/60)::int
		FROM public.worklogs WHERE id=$1::uuid`, worklogID)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":15}`)
	if code != http.StatusOK {
		t.Fatalf("adjust-time: want 200, got %d (%v)", code, out)
	}

	audit := auditRows(t, w.taskID, "TIME_ADJUSTED")
	if len(audit) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(audit))
	}
	if audit[0].ActorID != w.adminID {
		t.Errorf("audit actor_id = %s, want %s", audit[0].ActorID, w.adminID)
	}
	// Parsed rather than substring-matched: jsonb renders with a space after
	// the colon, and the value is what matters, not its formatting.
	var meta struct {
		DeltaMinutes int `json:"delta_minutes"`
	}
	if err := json.Unmarshal([]byte(audit[0].Meta), &meta); err != nil {
		t.Fatalf("audit meta is not valid json: %v", err)
	}
	if meta.DeltaMinutes != 15 {
		t.Errorf("audit meta delta_minutes = %d, want 15", meta.DeltaMinutes)
	}

	after := scalar[int](t, `
		SELECT ceil(extract(epoch from (end_at - start_at))/60)::int
		FROM public.worklogs WHERE id=$1::uuid`, worklogID)
	if after != before+15 {
		t.Errorf("session went from %dm to %dm, want %dm", before, after, before+15)
	}
	if out["worklog_id"] != worklogID {
		t.Errorf("worklog_id = %v, want %s", out["worklog_id"], worklogID)
	}
}

func TestAdminOpsAdjustTimeAcceptsNegativeDelta(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	worklogID := seedWorklog(t, w.taskID, 30, false)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":-10}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%v)", code, out)
	}
	after := scalar[int](t, `
		SELECT ceil(extract(epoch from (end_at - start_at))/60)::int
		FROM public.worklogs WHERE id=$1::uuid`, worklogID)
	if after != 20 {
		t.Errorf("session = %dm, want 20m", after)
	}
	if n := len(auditRows(t, w.taskID, "TIME_ADJUSTED")); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

// The guard the SQL function never had: subtracting more than the session
// lasted would have pushed end_at behind start_at and produced negative
// billable minutes.
func TestAdminOpsAdjustTimeRejectsOutOfRange(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	worklogID := seedWorklog(t, w.taskID, 10, false)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":-45}`)
	if code != http.StatusBadRequest || out["error"] != "adjustment_out_of_range" {
		t.Fatalf("want 400 adjustment_out_of_range, got %d (%v)", code, out)
	}
	// The session is untouched.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.worklogs WHERE id=$1::uuid AND end_at > start_at`, worklogID); n != 1 {
		t.Error("the rejected adjustment still moved end_at behind start_at")
	}
	if n := len(auditRows(t, w.taskID, "TIME_ADJUSTED")); n != 0 {
		t.Error("a rejected adjustment wrote an audit row")
	}
}

func TestAdminOpsAdjustTimeWithoutWorklog(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":5}`)
	if code != http.StatusBadRequest || out["error"] != "no_worklog" {
		t.Fatalf("want 400 no_worklog, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, "TIME_ADJUSTED")); n != 0 {
		t.Error("wrote an audit row for a task with no work session")
	}
}

func TestAdminOpsAdjustTimeRejectsZeroDelta(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":0}`)
	if code != http.StatusBadRequest || out["error"] != "invalid_delta" {
		t.Fatalf("want 400 invalid_delta, got %d (%v)", code, out)
	}
}

// A billing correction is usually spotted after the fact, so a completed task
// stays adjustable.
func TestAdminOpsAdjustTimeAllowedOnCompletedTask(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "completed")
	seedWorklog(t, w.taskID, 30, false)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":5}`)
	if code != http.StatusOK {
		t.Fatalf("want 200 on a completed task, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, "TIME_ADJUSTED")); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
}

func TestAdminOpsAdjustTimeRejectsCancelledTask(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "cancelled")
	seedWorklog(t, w.taskID, 30, false)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.adminID, adminEmail, `{"delta":5}`)
	if code != http.StatusBadRequest || out["error"] != "task_not_adjustable" {
		t.Fatalf("want 400 task_not_adjustable, got %d (%v)", code, out)
	}
}

func TestAdminOpsAdjustTimeRejectsNonAdmin(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	code, out := callAdminOps(t, adminAdjustTime, "adjust-time", w.taskID, w.supporterID, oldSupporterEmail, `{"delta":600}`)
	if code != http.StatusForbidden || out["error"] != "admin_only" {
		t.Fatalf("want 403 admin_only, got %d (%v)", code, out)
	}
	if n := len(auditRows(t, w.taskID, "TIME_ADJUSTED")); n != 0 {
		t.Error("a supporter managed to adjust their own logged time")
	}
}

// ── The whole point, in one test ────────────────────────────────────────────

// Every action leaves exactly one audit row, and every row names the acting
// admin. This is the invariant whose absence hid the bug: production had nine
// TASK_REMOVED rows and zero of these three.
func TestAdminOpsEveryActionIsAudited(t *testing.T) {
	setupAdminOpsDB(t)

	for _, tc := range []struct {
		name    string
		handler gin.HandlerFunc
		action  string
		body    string
		audit   string
	}{
		{"force-complete", adminForceCompleteTask, "force-complete", `{}`, "FORCE_COMPLETED"},
		{"cancel", adminCancelTaskHandler, "cancel", `{"reason":"x"}`, "CANCELLED"},
		{"adjust-time", adminAdjustTime, "adjust-time", `{"delta":5}`, "TIME_ADJUSTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupAdminOpsDB(t)
			w := seedOpsWorld(t, "open")
			seedWorklog(t, w.taskID, 30, false)

			code, out := callAdminOps(t, tc.handler, tc.action, w.taskID, w.adminID, adminEmail, tc.body)
			if code != http.StatusOK {
				t.Fatalf("%s: want 200, got %d (%v)", tc.name, code, out)
			}
			audit := auditRows(t, w.taskID, tc.audit)
			if len(audit) != 1 {
				t.Fatalf("%s wrote %d %s rows, want 1", tc.name, len(audit), tc.audit)
			}
			if audit[0].ActorID != w.adminID {
				t.Errorf("%s recorded actor %s, want the session admin %s", tc.name, audit[0].ActorID, w.adminID)
			}
		})
	}
}
