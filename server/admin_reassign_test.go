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

// End-to-end coverage for admin reassignment against a real Postgres: the
// eligibility matrix, the atomic swap, the audit row, the three-way
// notification fan-out, and the WHERE-guard that makes a concurrent accept or
// clock-in lose rather than corrupt. Skipped unless TEST_DATABASE_URL points at
// a throwaway database — this creates and drops tables, so never aim it at
// anything real.
//
//	docker run -d --name hora-reassign-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run AdminReassign -v
//
// TalkJS is not exercised: talkjsHandoverChat short-circuits to
// "skipped_unconfigured" with no credentials in the environment, which is what
// these tests run with. The chat handover is covered by the manual steps in the
// PR description instead — it is one REST call against a third party.

// A superset of removeFixture: the reassign path joins profiles to users on
// email and reads names, so those columns have to exist.
const reassignFixture = `
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

// Real migration files, in order, so the tests meet the schema the handler will
// meet — including the enum value this PR adds, which the handler's INSERTs
// depend on.
var reassignMigrationPaths = []string{
	"../supabase/migrations/20260818140000_task_removed_status.sql",
	"../supabase/migrations/20260818150000_notification_type_completed_supporter.sql",
	"../supabase/migrations/20260909120000_notification_type_task_reassigned.sql",
}

func setupReassignDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed reassignment tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, reassignFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, path := range reassignMigrationPaths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		// ALTER TYPE ... ADD VALUE cannot run inside a transaction block on
		// older servers; pgx sends these unbatched, so each file applies as its
		// own implicit transaction and this is fine.
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

// ── Seeding ────────────────────────────────────────────────────────────────

// seedUser creates a users row plus its profile. approved controls
// is_verified_supporter, which is the reassign eligibility gate.
func seedUser(t *testing.T, email, name string, approved bool) string {
	t.Helper()
	ctx := context.Background()
	var uid string
	if err := db.QueryRow(ctx,
		`INSERT INTO public.users (email, name) VALUES ($1,$2) RETURNING id::text`,
		email, name).Scan(&uid); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO public.profiles (id, email, name, city, is_verified_supporter)
		 VALUES ($1::uuid,$2,$3,'Rome',$4)`,
		uid, email, name, approved); err != nil {
		t.Fatalf("seed profile %s: %v", email, err)
	}
	return uid
}

// seedReassignTask returns the task id. assigneeID/assigneeEmail empty means
// nobody holds it yet.
func seedReassignTask(t *testing.T, status, requesterID, requesterEmail, assigneeID, assigneeEmail string) string {
	t.Helper()
	var assigned any
	if assigneeID != "" {
		assigned = assigneeID
	}
	var taskID string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO public.tasks (title, requester, requester_id, assigned_to, assigned_to_id, status)
		VALUES ('Pick up a parcel', $1, $2::uuid, $3, $4::uuid, $5)
		RETURNING id::text
	`, requesterEmail, requesterID, assigneeEmail, assigned, status).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return taskID
}

// world is the cast every test needs: an admin, a requester, the supporter who
// currently holds the task, and the one taking it over.
type world struct {
	adminID     string
	requesterID string
	oldID       string
	newID       string
}

const (
	oldSupporterEmail = "old.supporter@example.com"
	newSupporterEmail = "new.supporter@example.com"
	requesterEmail    = "requester@example.com"
)

func seedWorld(t *testing.T) world {
	t.Helper()
	return world{
		adminID:     seedUser(t, adminEmail, "Ops Admin", false),
		requesterID: seedUser(t, requesterEmail, "Rita Requester", false),
		oldID:       seedUser(t, oldSupporterEmail, "Otto Old", true),
		newID:       seedUser(t, newSupporterEmail, "Nina New", true),
	}
}

// callReassign runs the real route chain — auth-populated context, the admin
// middleware, then the handler — so a test cannot accidentally skip the
// authorization the endpoint depends on.
func callReassign(t *testing.T, taskID, uid, email, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/tasks/"+taskID+"/reassign", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	requireOpsAdmin()(c)
	if !c.IsAborted() {
		adminReassignTask(c)
	}

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func notificationsOfType(t *testing.T, taskID, userID, ntype string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		SELECT title FROM public.notifications
		WHERE task_id=$1::uuid AND user_id=$2::uuid AND type=$3::public.notification_type
		ORDER BY created_at
	`, taskID, userID, ntype)
	if err != nil {
		t.Fatalf("notifications: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// ── The headline case ──────────────────────────────────────────────────────

// A supporter is swapped out for another on an accepted-but-not-started task:
// both assignment columns move together, the audit row lands, and all three
// parties are told.
func TestAdminReassignSwapsSupporter(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"`+newSupporterEmail+`"}`)
	if code != http.StatusOK {
		t.Fatalf("reassign: want 200, got %d (%v)", code, out)
	}
	if out["previous_supporter"] != oldSupporterEmail {
		t.Errorf("previous_supporter = %v", out["previous_supporter"])
	}
	if out["new_supporter"] != newSupporterEmail {
		t.Errorf("new_supporter = %v", out["new_supporter"])
	}
	if out["old_supporter_notified"] != true {
		t.Errorf("old_supporter_notified = %v, want true", out["old_supporter_notified"])
	}

	// Both columns move together — the id and the email cache acceptTask keeps
	// in step. A swap that updated only one would leave chat and worklogs
	// pointing at different people.
	if n := scalar[int](t, `
		SELECT count(*) FROM public.tasks
		WHERE id=$1::uuid AND assigned_to_id=$2::uuid AND assigned_to=$3
	`, taskID, w.newID, newSupporterEmail); n != 1 {
		t.Error("assigned_to_id and assigned_to did not both move to the new supporter")
	}
	if got := scalar[string](t, `SELECT status FROM public.tasks WHERE id=$1::uuid`, taskID); got != "open" {
		t.Errorf("status = %q, want it left open", got)
	}

	// Audit row, with both sides of the swap recorded.
	meta := scalar[string](t, `
		SELECT meta::text FROM public.audit_logs
		WHERE job_id=$1::uuid AND action='TASK_REASSIGNED'
	`, taskID)
	for _, want := range []string{adminEmail, oldSupporterEmail, newSupporterEmail} {
		if !strings.Contains(meta, want) {
			t.Errorf("audit meta missing %q: %s", want, meta)
		}
	}
	if got := scalar[string](t, `
		SELECT reason FROM public.audit_logs WHERE job_id=$1::uuid AND action='TASK_REASSIGNED'
	`, taskID); got != "swap" {
		t.Errorf("audit reason = %q, want swap", got)
	}

	// Fan-out: every party hears about it exactly once.
	if got := notificationsOfType(t, taskID, w.newID, "TASK_REASSIGNED"); len(got) != 1 {
		t.Errorf("new supporter notifications = %v, want 1", got)
	}
	if got := notificationsOfType(t, taskID, w.oldID, "TASK_REASSIGNED"); len(got) != 1 {
		t.Errorf("old supporter notifications = %v, want 1", got)
	}
	if got := notificationsOfType(t, taskID, w.requesterID, "TASK_REASSIGNED"); len(got) != 1 {
		t.Errorf("requester notifications = %v, want 1", got)
	}

	// The requester's copy names the incoming supporter — that is the point of
	// telling them at all.
	body := scalar[string](t, `
		SELECT body FROM public.notifications WHERE task_id=$1::uuid AND user_id=$2::uuid
	`, taskID, w.requesterID)
	if !strings.Contains(body, "Nina New") {
		t.Errorf("requester notification does not name the new supporter: %q", body)
	}
	// The outgoing supporter's copy is the thank-you, not a task alert.
	oldBody := scalar[string](t, `
		SELECT body FROM public.notifications WHERE task_id=$1::uuid AND user_id=$2::uuid
	`, taskID, w.oldID)
	if !strings.Contains(oldBody, "unassigned") {
		t.Errorf("old supporter notification = %q", oldBody)
	}
}

// Direct assignment: a task nobody has accepted, handed to a supporter the team
// lined up over WhatsApp. Same path, no outgoing supporter to notify.
func TestAdminReassignDirectAssignsUnassignedTask(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, "", "")

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"`+newSupporterEmail+`"}`)
	if code != http.StatusOK {
		t.Fatalf("direct assign: want 200, got %d (%v)", code, out)
	}
	if out["old_supporter_notified"] != false {
		t.Errorf("old_supporter_notified = %v, want false", out["old_supporter_notified"])
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND assigned_to_id=$2::uuid`,
		taskID, w.newID); n != 1 {
		t.Error("task was not assigned")
	}
	if got := scalar[string](t, `
		SELECT reason FROM public.audit_logs WHERE job_id=$1::uuid AND action='TASK_REASSIGNED'
	`, taskID); got != "direct_assign" {
		t.Errorf("audit reason = %q, want direct_assign", got)
	}
	// Two recipients, not three.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid`, taskID); n != 2 {
		t.Errorf("notification count = %d, want 2 (new supporter + requester)", n)
	}
}

// Accepting by id rather than email, since the ops picker has both.
func TestAdminReassignAcceptsSupporterID(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_id":"`+w.newID+`"}`)
	if code != http.StatusOK {
		t.Fatalf("reassign by id: want 200, got %d (%v)", code, out)
	}
	if out["new_supporter"] != newSupporterEmail {
		t.Errorf("new_supporter = %v", out["new_supporter"])
	}
}

// ── Eligibility matrix ─────────────────────────────────────────────────────

// The one that matters most: a task that has been clocked into. Its worklog is
// keyed by the old supporter's email and its GPS pings by their user id, so a
// swap here would silently reattribute someone else's tracked work.
func TestAdminReassignRejectsTaskWithWorklog(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	if _, err := db.Exec(context.Background(),
		`INSERT INTO public.worklogs (task_id, "user") VALUES ($1::uuid, $2)`,
		taskID, oldSupporterEmail); err != nil {
		t.Fatalf("seed worklog: %v", err)
	}

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"`+newSupporterEmail+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%v)", code, out)
	}
	if out["error"] != "task_in_progress" {
		t.Errorf("error = %v, want task_in_progress", out["error"])
	}
	// The admin is told what to do instead.
	if msg, _ := out["message"].(string); !strings.Contains(msg, "Remove the task") {
		t.Errorf("message does not point at remove + repost: %q", msg)
	}
	// Nothing moved.
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND assigned_to_id=$2::uuid`,
		taskID, w.oldID); n != 1 {
		t.Error("the task was reassigned despite the worklog")
	}
	if n := scalar[int](t, `SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid`, taskID); n != 0 {
		t.Errorf("a rejected reassign still notified %d people", n)
	}
}

// A closed worklog counts too — the work happened, even if the timer stopped.
func TestAdminReassignRejectsTaskWithClosedWorklog(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
	if _, err := db.Exec(context.Background(),
		`INSERT INTO public.worklogs (task_id, "user", end_at) VALUES ($1::uuid, $2, now())`,
		taskID, oldSupporterEmail); err != nil {
		t.Fatalf("seed worklog: %v", err)
	}

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"`+newSupporterEmail+`"}`)
	if code != http.StatusConflict || out["error"] != "task_in_progress" {
		t.Fatalf("want 409 task_in_progress, got %d (%v)", code, out)
	}
}

// completed / cancelled / removed are history and stay that way.
func TestAdminReassignRejectsNonOpenTask(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "removed"} {
		t.Run(status, func(t *testing.T) {
			setupReassignDB(t)
			w := seedWorld(t)
			taskID := seedReassignTask(t, status, w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

			code, out := callReassign(t, taskID, w.adminID, adminEmail,
				`{"supporter_email":"`+newSupporterEmail+`"}`)
			if code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%v)", code, out)
			}
			if out["error"] != "task_not_reassignable" {
				t.Errorf("error = %v, want task_not_reassignable", out["error"])
			}
			if out["status"] != status {
				t.Errorf("status echoed as %v, want %q", out["status"], status)
			}
		})
	}
}

// The authorization boundary. A signed-in non-admin is refused by the same
// middleware that guards remove — the webapp's copy of the allowlist only
// decides whether to draw the button.
func TestAdminReassignRejectsNonAdmin(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.requesterID, requesterEmail,
		`{"supporter_email":"`+newSupporterEmail+`"}`)
	if code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (%v)", code, out)
	}
	if out["error"] != "admin_only" {
		t.Errorf("error = %v, want admin_only", out["error"])
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND assigned_to_id=$2::uuid`,
		taskID, w.oldID); n != 1 {
		t.Error("a non-admin managed to reassign the task")
	}
}

// Reassigning to whoever already holds it is a no-op the caller did not mean;
// letting it through would re-run the whole notification fan-out.
func TestAdminReassignRejectsSameSupporter(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"`+oldSupporterEmail+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%v)", code, out)
	}
	if out["error"] != "same_supporter" {
		t.Errorf("error = %v, want same_supporter", out["error"])
	}
	if n := scalar[int](t, `SELECT count(*) FROM public.notifications WHERE task_id=$1::uuid`, taskID); n != 0 {
		t.Errorf("same-supporter reassign notified %d people", n)
	}
}

// Only approved supporters. The picker filters to these, but the picker is not
// the authorization layer.
func TestAdminReassignRejectsUnapprovedSupporter(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	seedUser(t, "pending@example.com", "Pat Pending", false)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"pending@example.com"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%v)", code, out)
	}
	if out["error"] != "supporter_not_approved" {
		t.Errorf("error = %v, want supporter_not_approved", out["error"])
	}
	if n := scalar[int](t,
		`SELECT count(*) FROM public.tasks WHERE id=$1::uuid AND assigned_to_id=$2::uuid`,
		taskID, w.oldID); n != 1 {
		t.Error("an unapproved supporter was assigned")
	}
}

func TestAdminReassignRejectsUnknownSupporter(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"nobody@example.com"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%v)", code, out)
	}
	if out["error"] != "supporter_not_found" {
		t.Errorf("error = %v, want supporter_not_found", out["error"])
	}
}

// A requester cannot be handed their own task — the same rule acceptTask
// enforces, which this endpoint bypasses.
func TestAdminReassignRejectsRequesterAsSupporter(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	// Make the requester an approved supporter, so this is the only check left.
	if _, err := db.Exec(context.Background(),
		`UPDATE public.profiles SET is_verified_supporter = true WHERE email = $1`, requesterEmail); err != nil {
		t.Fatalf("approve requester: %v", err)
	}
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail,
		`{"supporter_email":"`+requesterEmail+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (%v)", code, out)
	}
	if out["error"] != "supporter_is_requester" {
		t.Errorf("error = %v, want supporter_is_requester", out["error"])
	}
}

func TestAdminReassignRequiresASupporter(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)

	code, out := callReassign(t, taskID, w.adminID, adminEmail, `{}`)
	if code != http.StatusBadRequest || out["error"] != "supporter_required" {
		t.Fatalf("want 400 supporter_required, got %d (%v)", code, out)
	}
}

func TestAdminReassignUnknownTaskIs404(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)

	code, _ := callReassign(t, "00000000-0000-0000-0000-000000000000", w.adminID, adminEmail,
		`{"supporter_email":"`+newSupporterEmail+`"}`)
	if code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}

// ── The WHERE guard ────────────────────────────────────────────────────────

// applyReassign is the statement the handler runs once its checks have passed.
// Everything it guards against happens *after* those checks, so it is exercised
// directly: each case sets up the state a concurrent request would have left
// behind and asserts the swap declines to apply.
func TestAdminReassignWhereGuardLosesRaces(t *testing.T) {
	setupReassignDB(t)
	w := seedWorld(t)
	ctx := context.Background()
	target := reassignTarget{UID: w.newID, Email: newSupporterEmail, Name: "Nina New"}

	t.Run("someone else accepted first", func(t *testing.T) {
		// The handler read the task as unassigned; by the time it updates,
		// another supporter has accepted it.
		taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, "", "")
		if _, err := db.Exec(ctx,
			`UPDATE public.tasks SET assigned_to_id=$2::uuid, assigned_to=$3 WHERE id=$1::uuid`,
			taskID, w.oldID, oldSupporterEmail); err != nil {
			t.Fatalf("simulate accept: %v", err)
		}

		n, err := applyReassign(ctx, taskID, target, nil) // nil = "was unassigned"
		if err != nil {
			t.Fatalf("applyReassign: %v", err)
		}
		if n != 0 {
			t.Error("the swap overwrote a concurrent accept")
		}
		if got := scalar[string](t, `SELECT assigned_to FROM public.tasks WHERE id=$1::uuid`, taskID); got != oldSupporterEmail {
			t.Errorf("assigned_to = %q, want the accepting supporter kept", got)
		}
	})

	t.Run("the supporter clocked in first", func(t *testing.T) {
		taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
		if _, err := db.Exec(ctx,
			`INSERT INTO public.worklogs (task_id, "user") VALUES ($1::uuid,$2)`,
			taskID, oldSupporterEmail); err != nil {
			t.Fatalf("simulate clock-in: %v", err)
		}

		n, err := applyReassign(ctx, taskID, target, &w.oldID)
		if err != nil {
			t.Fatalf("applyReassign: %v", err)
		}
		if n != 0 {
			t.Error("the swap applied on top of a clock-in that raced it")
		}
	})

	t.Run("the task was cancelled first", func(t *testing.T) {
		taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
		if _, err := db.Exec(ctx,
			`UPDATE public.tasks SET status='cancelled' WHERE id=$1::uuid`, taskID); err != nil {
			t.Fatalf("simulate cancel: %v", err)
		}

		n, err := applyReassign(ctx, taskID, target, &w.oldID)
		if err != nil {
			t.Fatalf("applyReassign: %v", err)
		}
		if n != 0 {
			t.Error("the swap applied to a cancelled task")
		}
	})

	t.Run("nothing changed", func(t *testing.T) {
		// The control: with the state the handler read, the guard lets it through.
		taskID := seedReassignTask(t, "open", w.requesterID, requesterEmail, w.oldID, oldSupporterEmail)
		n, err := applyReassign(ctx, taskID, target, &w.oldID)
		if err != nil {
			t.Fatalf("applyReassign: %v", err)
		}
		if n != 1 {
			t.Errorf("rows affected = %d, want 1", n)
		}
	})
}

// ── Supporter picker ───────────────────────────────────────────────────────

// GET /admin/supporters backs the ops dialog. It must list exactly the set the
// reassign check accepts — an approved supporter missing from it cannot be
// picked, and an unapproved one appearing in it produces a confusing 400.
func TestAdminListSupportersReturnsOnlyApproved(t *testing.T) {
	setupReassignDB(t)
	seedWorld(t)
	seedUser(t, "pending@example.com", "Pat Pending", false)
	seedUser(t, "also.approved@example.com", "Alma Approved", true)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/supporters", nil)
	c.Set("uid", "irrelevant")
	c.Set("email", adminEmail)
	adminListSupporters(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var items []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := map[string]string{}
	for _, it := range items {
		got[it.Email] = it.Name
		if it.ID == "" {
			t.Errorf("%s has no id — the picker needs one", it.Email)
		}
	}
	for _, email := range []string{oldSupporterEmail, newSupporterEmail, "also.approved@example.com"} {
		if _, ok := got[email]; !ok {
			t.Errorf("approved supporter %s missing from the picker", email)
		}
	}
	for _, email := range []string{"pending@example.com", requesterEmail, adminEmail} {
		if _, ok := got[email]; ok {
			t.Errorf("%s should not be offered as a supporter", email)
		}
	}
	if got[newSupporterEmail] != "Nina New" {
		t.Errorf("name = %q, want the profile name", got[newSupporterEmail])
	}
}
