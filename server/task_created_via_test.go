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

// Coverage for `created_via` on POST /tasks against a real Postgres: the
// handler, the insert, and the CHECK constraint the migration adds, together.
// The column exists to count re-posts in the October round, so the cases that
// matter are the ones a live deploy will actually meet — the shipped build that
// sends no field at all, the three paths the new build sends, a bad value, and
// the guarantee that editing a task never rewrites how it was created.
//
// Skipped unless TEST_DATABASE_URL points at a throwaway database — this
// creates and drops tables, so never aim it at anything real.
//
//	docker run -d --name hora-createdvia-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55435:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55435/horatest' \
//	  go test ./ -run CreatedVia -v

// The slice of production schema createTask/updateTask touch, copied from
// supabase/migrations/20260711094158_remote_schema.sql so the new migration
// runs against exactly the table it will meet in prod.
const createdViaFixture = `
DROP TABLE IF EXISTS public.tasks CASCADE;
DROP TABLE IF EXISTS public.profiles CASCADE;
DROP TABLE IF EXISTS public.users CASCADE;

CREATE TABLE public.users (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	email text UNIQUE,
	name text
);

CREATE TABLE public.profiles (
	id uuid PRIMARY KEY,
	email text UNIQUE,
	name text,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.tasks (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	title text NOT NULL,
	description text NOT NULL,
	category text NOT NULL,
	location_text text NOT NULL,
	estimated_minutes integer NOT NULL,
	prepay_amount_cents integer NOT NULL DEFAULT 0,
	is_immediate boolean NOT NULL DEFAULT false,
	scheduled_at timestamptz,
	requester text NOT NULL,
	status text NOT NULL DEFAULT 'open',
	assigned_to text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	requester_id uuid NOT NULL REFERENCES public.users(id),
	assigned_to_id uuid REFERENCES public.users(id),
	cancelled_at timestamptz,
	cancel_reason text,
	transport_required text NOT NULL DEFAULT 'none',
	travel_time_minutes integer,
	total_estimate_minutes integer,
	CONSTRAINT tasks_status_check CHECK ((status = ANY (ARRAY['open'::text, 'completed'::text, 'cancelled'::text])))
);
`

const createdViaMigrationPath = "../supabase/migrations/20260827160000_tasks_created_via.sql"

func setupCreatedViaDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed created_via tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, createdViaFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// The real migration file, not a paraphrase of it.
	migration, err := os.ReadFile(createdViaMigrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// createTask writes through database/sql (sqldb), updateTask through pgx (db).
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

const createdViaEmail = "requester@example.test"

func postTaskJSON(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("email", createdViaEmail)

	createTask(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// createdViaOf returns the stored value, and whether it is non-NULL.
func createdViaOf(t *testing.T, taskID string) (string, bool) {
	t.Helper()
	var v *string
	if err := db.QueryRow(context.Background(),
		`SELECT created_via FROM public.tasks WHERE id = $1::uuid`, taskID,
	).Scan(&v); err != nil {
		t.Fatalf("read created_via: %v", err)
	}
	if v == nil {
		return "", false
	}
	return *v, true
}

// The build currently in TestFlight — and the web app, which is not being
// changed in this round — post without the field. Both must keep working, and
// their tasks must land as NULL: unattributed, not guessed at.
func TestCreatedViaOmittedStoresNull(t *testing.T) {
	setupCreatedViaDB(t)

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != 201 {
		t.Fatalf("expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	if got, ok := createdViaOf(t, id); ok {
		t.Fatalf("expected NULL created_via, got %q", got)
	}
}

// The three paths the mobile build sends, each stored verbatim. 'duplicate' is
// the one the October count is actually about.
func TestCreatedViaStoresEachPath(t *testing.T) {
	setupCreatedViaDB(t)

	for _, want := range []string{"form", "ai_parse", "duplicate"} {
		code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true,"created_via":"`+want+`"}`)
		if code != 201 {
			t.Fatalf("%s: expected 201, got %d (%v)", want, code, body)
		}
		id, _ := body["id"].(string)
		got, ok := createdViaOf(t, id)
		if !ok || got != want {
			t.Fatalf("%s: stored %q (present=%v)", want, got, ok)
		}
	}
}

// A value outside the closed set is refused at the handler with a readable 400,
// rather than reaching the CHECK constraint and surfacing as "db error" — and,
// more to the point, rather than being counted as something it isn't.
func TestCreatedViaRejectsUnknownValue(t *testing.T) {
	setupCreatedViaDB(t)

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true,"created_via":"telepathy"}`)
	if code != 400 {
		t.Fatalf("expected 400, got %d (%v)", code, body)
	}
	if body["error"] != "invalid created_via" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

// The database keeps the same set the handler does, so nothing that bypasses
// the handler can widen it either.
func TestCreatedViaCheckConstraintRejectsUnknownValue(t *testing.T) {
	setupCreatedViaDB(t)

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true,"created_via":"duplicate"}`)
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)

	if _, err := db.Exec(context.Background(),
		`UPDATE public.tasks SET created_via = 'telepathy' WHERE id = $1::uuid`, id,
	); err == nil {
		t.Fatal("expected tasks_created_via_check to reject an unknown value")
	}
}

// created_via records how a task was CREATED. PATCH /tasks/:id binds the same
// createTaskInput, so a client that sends the field on an edit — or a future
// change that starts sending it — must not be able to rewrite the attribution.
func TestCreatedViaSurvivesAnEdit(t *testing.T) {
	setupCreatedViaDB(t)

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true,"created_via":"duplicate"}`)
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPatch, "/tasks/"+id,
		strings.NewReader(`{"title":"Coffee run, oat milk","category":"delivery","estimated_minutes":45,"is_immediate":true,"created_via":"form"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Set("email", createdViaEmail)

	updateTask(c)
	if w.Code != 200 {
		t.Fatalf("edit: expected 200, got %d (%s)", w.Code, w.Body.String())
	}

	got, ok := createdViaOf(t, id)
	if !ok || got != "duplicate" {
		t.Fatalf("edit rewrote attribution: stored %q (present=%v), want \"duplicate\"", got, ok)
	}
}
