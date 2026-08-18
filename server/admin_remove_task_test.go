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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/jackc/pgx/v5"
)

// End-to-end coverage for the admin takedown path against a real Postgres: the
// handler, its SQL, the notifications it fans out, and the constraints the
// migration adds. Skipped unless TEST_DATABASE_URL points at a throwaway
// database — this creates and drops tables, so never aim it at anything real.
//
//	docker run -d --name hora-remove-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run AdminRemove -v

// The slice of production schema this path touches, copied from
// supabase/migrations/20260711094158_remote_schema.sql (and the push-token
// migration) so the new migration runs against the tables it will meet in prod.
const removeFixture = `
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
	email text
);

CREATE TABLE public.profiles (
	id uuid,
	email text PRIMARY KEY,
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
	created_at timestamptz NOT NULL DEFAULT now()
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

// Every migration that lands after the fixture's snapshot of prod. Applied in
// order, real files rather than paraphrases, so the tests meet the schema the
// handlers will meet.
var removeMigrationPaths = []string{
	"../supabase/migrations/20260818140000_task_removed_status.sql",
	"../supabase/migrations/20260818150000_notification_type_completed_supporter.sql",
}

const adminEmail = "taoaura.lavoro@gmail.com"

func setupRemoveDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed takedown tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, removeFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, path := range removeMigrationPaths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("migration %s: %v", path, err)
		}
	}

	// notify.Create writes through database/sql, the handlers through pgx.
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

// seedTask returns (taskID, requesterID, supporterID). supporterEmail empty
// means nobody has accepted it.
func seedTask(t *testing.T, status, supporterEmail string) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	var requester, supporter, task string
	if err := db.QueryRow(ctx,
		`INSERT INTO public.users (email) VALUES ('requester@example.com') RETURNING id::text`,
	).Scan(&requester); err != nil {
		t.Fatalf("seed requester: %v", err)
	}
	if err := db.QueryRow(ctx,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`,
		nullableEmail(supporterEmail),
	).Scan(&supporter); err != nil {
		t.Fatalf("seed supporter: %v", err)
	}

	var assignedID any
	assignedTo := ""
	if supporterEmail != "" {
		assignedID = supporter
		assignedTo = supporterEmail
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO public.tasks (title, requester, requester_id, assigned_to, assigned_to_id, status)
		VALUES ('Pick up a parcel', 'requester@example.com', $1::uuid, $2, $3::uuid, $4)
		RETURNING id::text
	`, requester, assignedTo, assignedID, status).Scan(&task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task, requester, supporter
}

func nullableEmail(s string) string {
	if s == "" {
		return "supporter@example.com"
	}
	return s
}

// callRemove runs the real route chain — auth-populated context, admin
// middleware, handler — so a test can't accidentally skip the authorization
// the endpoint depends on.
func callRemove(t *testing.T, taskID, uid, email, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/tasks/"+taskID+"/remove", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	requireOpsAdmin()(c)
	if !c.IsAborted() {
		adminRemoveTask(c)
	}

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func getTaskAs(t *testing.T, taskID, uid, email string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/tasks/"+taskID, nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	getTask(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func postTaskAction(t *testing.T, handler gin.HandlerFunc, path, taskID, uid, email, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	handler(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func scalar[T any](t *testing.T, query string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func notificationTitles(t *testing.T, taskID, userID string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(),
		`SELECT title FROM public.notifications WHERE task_id=$1::uuid AND user_id=$2::uuid ORDER BY created_at`,
		taskID, userID)
	if err != nil {
		t.Fatalf("notifications: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan notification: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// The headline case: a task with a supporter mid-shift is taken down.
func TestAdminRemoveWithAssignedSupporter(t *testing.T) {
	setupRemoveDB(t)
	taskID, requesterID, supporterID := seedTask(t, "open", "supporter@example.com")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)
	if _, err := db.Exec(context.Background(),
		`INSERT INTO public.worklogs (task_id, "user") VALUES ($1::uuid, 'supporter@example.com')`, taskID); err != nil {
		t.Fatalf("seed worklog: %v", err)
	}

	code, out := callRemove(t, taskID, adminID, adminEmail,
		`{"reason":"out_of_scope_private_residence","note":"flat in Trastevere"}`)
	if code != http.StatusOK {
		t.Fatalf("remove: want 200, got %d (%v)", code, out)
	}
	if out["supporter_notified"] != true {
		t.Errorf("expected supporter_notified=true, got %v", out["supporter_notified"])
	}

	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, taskID); got != "removed" {
		t.Errorf("status = %q, want removed", got)
	}
	// Detached: the supporter is off the task entirely.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND assigned_to_id IS NULL AND assigned_to = ''`,
		taskID); n != 1 {
		t.Error("supporter was not detached from the removed task")
	}
	// The running timer is closed, not left ticking on a dead task.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.worklogs WHERE task_id=$1::uuid AND end_at IS NULL`, taskID); n != 0 {
		t.Errorf("%d worklog session(s) left open", n)
	}

	// Both parties notified. Each row is one notify.Create call, which is also
	// what fires the Expo push and the email — one trigger point, three channels.
	reqTitles := notificationTitles(t, taskID, requesterID)
	if len(reqTitles) != 1 || reqTitles[0] != "Task removed" {
		t.Errorf("requester notifications = %v, want [Task removed]", reqTitles)
	}
	supTitles := notificationTitles(t, taskID, supporterID)
	if len(supTitles) != 1 || supTitles[0] != "Task no longer available" {
		t.Errorf("supporter notifications = %v, want [Task no longer available]", supTitles)
	}
	body := scalar[string](t,
		`SELECT body FROM public.notifications WHERE task_id=$1::uuid AND user_id=$2::uuid`, taskID, requesterID)
	if !strings.Contains(body, "no private residences") || !strings.Contains(body, "Pick up a parcel") {
		t.Errorf("requester copy missing scope explanation or task title: %q", body)
	}

	// Audit trail: the existing audit_logs table, with the internal note.
	action := scalar[string](t, `SELECT action FROM public.audit_logs WHERE job_id=$1::uuid`, taskID)
	if action != "TASK_REMOVED" {
		t.Errorf("audit action = %q", action)
	}
	meta := scalar[string](t, `SELECT meta::text FROM public.audit_logs WHERE job_id=$1::uuid`, taskID)
	if !strings.Contains(meta, "flat in Trastevere") || !strings.Contains(meta, supporterID) {
		t.Errorf("audit meta missing note or previous assignee: %s", meta)
	}
	// The note is for us, not for the requester.
	if strings.Contains(body, "Trastevere") {
		t.Error("internal note leaked into the requester's notification")
	}
}

// Hidden from the supporter feed: /tasks/available only ever returned open,
// unassigned tasks, and a removed one is neither.
func TestAdminRemoveHidesTaskFromFeed(t *testing.T) {
	setupRemoveDB(t)
	taskID, _, _ := seedTask(t, "open", "")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)

	browserID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ('browser@example.com') RETURNING id::text`)
	if _, err := db.Exec(context.Background(),
		`INSERT INTO public.profiles (id, email, is_verified_supporter) VALUES ($1::uuid, 'browser@example.com', true)`,
		browserID); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	feed := func() []any {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/tasks/available", nil)
		c.Set("uid", browserID)
		c.Set("email", "browser@example.com")
		listAvailableTasks(c)
		var out struct {
			Items []any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode feed: %v (body=%s)", err, w.Body.String())
		}
		return out.Items
	}

	if len(feed()) != 1 {
		t.Fatalf("precondition: task should be in the feed before removal")
	}
	if code, out := callRemove(t, taskID, adminID, adminEmail, `{"reason":"inappropriate"}`); code != http.StatusOK {
		t.Fatalf("remove: %d %v", code, out)
	}
	if items := feed(); len(items) != 0 {
		t.Errorf("removed task still in the supporter feed: %v", items)
	}
}

// A second click, a retried request, a double-submitted form: all no-ops.
func TestAdminRemoveIsIdempotent(t *testing.T) {
	setupRemoveDB(t)
	taskID, requesterID, _ := seedTask(t, "open", "supporter@example.com")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)

	if code, _ := callRemove(t, taskID, adminID, adminEmail, `{"reason":"out_of_scope_other"}`); code != http.StatusOK {
		t.Fatalf("first remove: %d", code)
	}
	code, out := callRemove(t, taskID, adminID, adminEmail, `{"reason":"out_of_scope_other"}`)
	if code != http.StatusOK {
		t.Fatalf("second remove: want 200 no-op, got %d (%v)", code, out)
	}
	if out["already_removed"] != true {
		t.Errorf("second remove should report already_removed, got %v", out)
	}
	if n := scalar[int](t, `SELECT count(*) FROM public.audit_logs WHERE job_id=$1::uuid`, taskID); n != 1 {
		t.Errorf("%d audit rows, want 1 — the no-op wrote a second one", n)
	}
	if titles := notificationTitles(t, taskID, requesterID); len(titles) != 1 {
		t.Errorf("requester notified %d times, want once", len(titles))
	}
}

// Nobody outside the allowlist gets to take a task down, whatever the webapp
// happens to render.
func TestAdminRemoveRejectsNonAdmin(t *testing.T) {
	setupRemoveDB(t)
	taskID, requesterID, _ := seedTask(t, "open", "")

	code, out := callRemove(t, taskID, requesterID, "requester@example.com", `{"reason":"other"}`)
	if code != http.StatusForbidden {
		t.Fatalf("non-admin remove: want 403, got %d (%v)", code, out)
	}
	if out["error"] != "admin_only" {
		t.Errorf("error = %v, want admin_only", out["error"])
	}
	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, taskID); got != "open" {
		t.Errorf("status = %q — a non-admin changed the task", got)
	}
}

func TestAdminRemoveRejectsCompletedTask(t *testing.T) {
	setupRemoveDB(t)
	taskID, _, _ := seedTask(t, "completed", "supporter@example.com")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)

	code, out := callRemove(t, taskID, adminID, adminEmail, `{"reason":"other"}`)
	if code != http.StatusBadRequest || out["error"] != "task_not_removable" {
		t.Fatalf("completed removal: want 400 task_not_removable, got %d (%v)", code, out)
	}
	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, taskID); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
}

func TestAdminRemoveRejectsUnknownReason(t *testing.T) {
	setupRemoveDB(t)
	taskID, _, _ := seedTask(t, "open", "")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)

	code, out := callRemove(t, taskID, adminID, adminEmail, `{"reason":"because_i_said_so"}`)
	if code != http.StatusBadRequest || out["error"] != "invalid_reason" {
		t.Fatalf("want 400 invalid_reason, got %d (%v)", code, out)
	}
}

// Hiding the buttons is not the protection — the endpoints are.
func TestAdminRemoveBlocksTaskActions(t *testing.T) {
	setupRemoveDB(t)
	taskID, _, supporterID := seedTask(t, "open", "supporter@example.com")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)
	if code, _ := callRemove(t, taskID, adminID, adminEmail, `{"reason":"inappropriate"}`); code != http.StatusOK {
		t.Fatalf("remove failed")
	}

	// Somebody who had the open task on screen tries to accept it.
	otherID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ('other@example.com') RETURNING id::text`)
	code, out := postTaskAction(t, acceptTask, "/tasks/"+taskID+"/accept", taskID, otherID, "other@example.com", "")
	if code != http.StatusBadRequest || out["error"] != "task_removed" {
		t.Errorf("accept on removed task: want 400 task_removed, got %d (%v)", code, out)
	}

	// And the clock-in guard, exercised directly: re-attach the supporter so the
	// status check is what answers, not the assignee check in front of it.
	if _, err := db.Exec(context.Background(),
		`UPDATE public.tasks SET assigned_to_id=$2::uuid, assigned_to='supporter@example.com' WHERE id=$1::uuid`,
		taskID, supporterID); err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	code, out = postTaskAction(t, clockIn, "/tasks/"+taskID+"/clock-in", taskID, supporterID, "supporter@example.com", "")
	if code != http.StatusBadRequest || out["error"] != "task_removed" {
		t.Errorf("clock-in on removed task: want 400 task_removed, got %d (%v)", code, out)
	}
	if n := scalar[int](t, `SELECT count(*) FROM public.worklogs WHERE task_id=$1::uuid`, taskID); n != 0 {
		t.Errorf("clock-in created %d worklog(s) on a removed task", n)
	}
}

// Who can still see a removed task: the requester who posted it and admins.
func TestAdminRemoveTaskDetailVisibility(t *testing.T) {
	setupRemoveDB(t)
	taskID, requesterID, supporterID := seedTask(t, "open", "supporter@example.com")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)
	if code, _ := callRemove(t, taskID, adminID, adminEmail, `{"reason":"out_of_scope_private_residence"}`); code != http.StatusOK {
		t.Fatalf("remove failed")
	}

	code, out := getTaskAs(t, taskID, requesterID, "requester@example.com")
	if code != http.StatusOK {
		t.Fatalf("requester detail: want 200, got %d (%v)", code, out)
	}
	if out["status"] != "removed" || out["removal_reason"] != "out_of_scope_private_residence" {
		t.Errorf("requester detail missing takedown state: %v", out)
	}
	if _, leaked := out["removal_note"]; leaked {
		t.Error("internal removal_note serialized to the client")
	}

	if code, out = getTaskAs(t, taskID, adminID, adminEmail); code != http.StatusOK {
		t.Errorf("admin detail: want 200, got %d (%v)", code, out)
	}

	// The detached supporter and anyone else get a named error, so the app can
	// say "this task has been removed" instead of a generic failure.
	code, out = getTaskAs(t, taskID, supporterID, "supporter@example.com")
	if code != http.StatusForbidden || out["error"] != "task_removed" {
		t.Errorf("detached supporter detail: want 403 task_removed, got %d (%v)", code, out)
	}
}

// The requester keeps the record: it moves to their closed list, labelled.
func TestAdminRemoveKeepsTaskInRequesterList(t *testing.T) {
	setupRemoveDB(t)
	taskID, requesterID, _ := seedTask(t, "open", "")
	adminID := scalar[string](t,
		`INSERT INTO public.users (email) VALUES ($1) RETURNING id::text`, adminEmail)
	if code, _ := callRemove(t, taskID, adminID, adminEmail, `{"reason":"out_of_scope_other"}`); code != http.StatusOK {
		t.Fatalf("remove failed")
	}

	list := func(handler gin.HandlerFunc, path string) []map[string]any {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		c.Set("uid", requesterID)
		c.Set("email", "requester@example.com")
		handler(c)
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v (body=%s)", path, err, w.Body.String())
		}
		return out.Items
	}

	posted := list(listMyTasks, "/tasks/posted")
	if len(posted) != 1 || posted[0]["status"] != "removed" {
		t.Errorf("posted list = %v, want the task with status removed", posted)
	}
	closed := list(listMyPostedClosed, "/tasks/posted/closed")
	if len(closed) != 1 || closed[0]["status"] != "removed" {
		t.Errorf("closed list = %v, want the removed task", closed)
	}
}

// The allowlist itself — no DB needed.
func TestParseAdminEmails(t *testing.T) {
	fallback := []string{"fallback@example.com"}

	env := parseAdminEmails(" Ops@Example.com ,,second@example.com ", fallback)
	if len(env) != 2 {
		t.Fatalf("parsed %d entries from env, want 2: %v", len(env), env)
	}
	if _, ok := env["ops@example.com"]; !ok {
		t.Error("env address was not lowercased/trimmed")
	}
	if _, ok := env["fallback@example.com"]; ok {
		t.Error("fallback leaked in even though ADMIN_EMAILS was set")
	}

	for _, blank := range []string{"", "   ", ",,"} {
		got := parseAdminEmails(blank, fallback)
		if _, ok := got["fallback@example.com"]; !ok || len(got) != 1 {
			t.Errorf("ADMIN_EMAILS=%q should fall back to the defaults, got %v", blank, got)
		}
	}
}

func TestDefaultAdminsIncludeTeam(t *testing.T) {
	admins := parseAdminEmails("", defaultOpsAdmins)
	for _, email := range []string{adminEmail, "liang.you@horaapp.co"} {
		if _, ok := admins[email]; !ok {
			t.Errorf("%s is not in the default admin allowlist", email)
		}
	}
	if _, ok := admins["stranger@example.com"]; ok {
		t.Error("allowlist admitted an address that is not on it")
	}
}

func TestIsOpsAdminEmailIsCaseInsensitive(t *testing.T) {
	prev := opsAdmins
	opsAdmins = parseAdminEmails("", defaultOpsAdmins)
	t.Cleanup(func() { opsAdmins = prev })

	if !isOpsAdminEmail("  TaoAura.Lavoro@Gmail.com ") {
		t.Error("admin check should normalize case and whitespace")
	}
	if isOpsAdminEmail("") || isOpsAdminEmail("nobody@example.com") {
		t.Error("admin check admitted a non-admin")
	}
}

// Every type the Go code sends to notify.Create, which inserts it into an enum
// column. A type missing from the enum fails the INSERT inside Create, whose
// only caller logs the error and moves on — so the notification, its email and
// its push vanish with nothing but a log line to show for it. That is exactly
// how COMPLETED_SUPPORTER went unnoticed in prod (0 rows against 16 COMPLETED),
// which the 20260818150000 migration repairs. Add a type here when you emit one.
var notificationTypesEmitted = []string{
	"ORDER_ACCEPTED",
	"CLOCK_IN",
	"CLOCK_OUT",
	"CANCELLED",
	"COMPLETED",
	"COMPLETED_SUPPORTER",
	"TASK_REMOVED",
}

func TestAdminRemoveNotificationEnumCoversEveryEmittedType(t *testing.T) {
	setupRemoveDB(t)
	taskID, requesterID, _ := seedTask(t, "open", "")

	for _, ntype := range notificationTypesEmitted {
		if _, err := db.Exec(context.Background(), `
			INSERT INTO public.notifications (user_id, task_id, type, title, body)
			VALUES ($1::uuid, $2::uuid, $3, 'title', 'body')
		`, requesterID, taskID, ntype); err != nil {
			t.Errorf("notifications.type rejects %q — every notification of this type is silently dropped: %v", ntype, err)
		}
	}
}
