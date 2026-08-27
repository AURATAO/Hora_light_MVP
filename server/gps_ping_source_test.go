package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Coverage for the `source` field on POST /tasks/:id/gps-ping against a real
// Postgres: the handler, the insert, and the CHECK constraint the migration
// adds, together. The point of the column is to tell background pings from
// foreground ones, so the cases that matter are the three a live deploy will
// actually meet — the old TestFlight build (no field), the new build's two
// paths, and a bad value.
//
// Skipped unless TEST_DATABASE_URL points at a throwaway database — this
// creates and drops tables, so never aim it at anything real.
//
//	docker run -d --name hora-gps-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55433:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55433/horatest' \
//	  go test ./ -run GpsPing -v

// Minimal slice of the production schema saveGpsPing touches. The
// task_gps_pings and worklogs DDL is copied verbatim from
// supabase/migrations/20260711094158_remote_schema.sql so the new migration
// runs against exactly the table it will meet in prod.
const gpsPingFixture = `
DROP TABLE IF EXISTS public.task_gps_pings CASCADE;
DROP TABLE IF EXISTS public.worklogs CASCADE;
DROP TABLE IF EXISTS public.tasks CASCADE;
DROP TABLE IF EXISTS public.users CASCADE;

CREATE TABLE public.users (id uuid PRIMARY KEY DEFAULT gen_random_uuid());

CREATE TABLE public.tasks (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	title text DEFAULT '',
	requester_id uuid REFERENCES public.users(id),
	assigned_to_id uuid REFERENCES public.users(id),
	status text NOT NULL DEFAULT 'open'
);

CREATE TABLE public.worklogs (
	"id" uuid DEFAULT gen_random_uuid() NOT NULL,
	"task_id" uuid NOT NULL,
	"user" text NOT NULL,
	"start_at" timestamp with time zone NOT NULL,
	"end_at" timestamp with time zone,
	"created_at" timestamp with time zone DEFAULT now() NOT NULL,
	"updated_at" timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.task_gps_pings (
	"id" uuid DEFAULT gen_random_uuid() NOT NULL,
	"task_id" uuid,
	"user_id" uuid,
	"lat" double precision NOT NULL,
	"lng" double precision NOT NULL,
	"accuracy" integer,
	"created_at" timestamp with time zone DEFAULT now()
);
ALTER TABLE ONLY public.task_gps_pings ADD CONSTRAINT task_gps_pings_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.task_gps_pings ADD CONSTRAINT task_gps_pings_task_id_fkey
	FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;
ALTER TABLE public.task_gps_pings ENABLE ROW LEVEL SECURITY;
`

const gpsPingMigrationPath = "../supabase/migrations/20260827120000_task_gps_pings_source.sql"

func setupGpsPingDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed gps-ping tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, gpsPingFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// The real migration file, not a paraphrase of it.
	migration, err := os.ReadFile(gpsPingMigrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("migration: %v", err)
	}

	prev := db
	db = pool
	t.Cleanup(func() {
		pool.Close()
		db = prev
	})
}

// seedClockedInTask returns (taskID, supporterID, supporterEmail) for a
// supporter with an open worklog — the only state saveGpsPing accepts.
func seedClockedInTask(t *testing.T) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	const supporterEmail = "supporter@example.test"
	var requester, supporter, task string
	if err := db.QueryRow(ctx, `INSERT INTO public.users DEFAULT VALUES RETURNING id::text`).Scan(&requester); err != nil {
		t.Fatalf("seed requester: %v", err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO public.users DEFAULT VALUES RETURNING id::text`).Scan(&supporter); err != nil {
		t.Fatalf("seed supporter: %v", err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO public.tasks (title, requester_id, assigned_to_id, status)
		VALUES ('Coffee run', $1::uuid, $2::uuid, 'open') RETURNING id::text
	`, requester, supporter).Scan(&task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO public.worklogs (task_id, "user", start_at) VALUES ($1::uuid, $2, now())
	`, task, supporterEmail); err != nil {
		t.Fatalf("seed worklog: %v", err)
	}
	return task, supporter, supporterEmail
}

func postGpsPing(t *testing.T, taskID, uid, email, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/gps-ping", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)
	c.Set("email", email)

	saveGpsPing(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func gpsSources(t *testing.T, taskID string) []string {
	t.Helper()
	rows, err := db.Query(context.Background(),
		`SELECT source FROM public.task_gps_pings WHERE task_id = $1::uuid ORDER BY created_at`, taskID)
	if err != nil {
		t.Fatalf("read sources: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan source: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// The current TestFlight build sends {lat, lng, accuracy} and no `source`.
// It must keep working, and its pings must land as 'foreground' — which is
// what they are.
func TestGpsPingOmittedSourceDefaultsToForeground(t *testing.T) {
	setupGpsPingDB(t)
	task, uid, email := seedClockedInTask(t)

	code, body := postGpsPing(t, task, uid, email, `{"lat": 45.4642, "lng": 9.19, "accuracy": 12}`)
	if code != http.StatusOK {
		t.Fatalf("ping: want 200, got %d (%v)", code, body)
	}
	if got := gpsSources(t, task); len(got) != 1 || got[0] != "foreground" {
		t.Fatalf("source for a payload with no source field: want [foreground], got %v", got)
	}
}

// Both values the new build sends are stored verbatim, so the max-gap and
// background/foreground-ratio queries can tell the two paths apart.
func TestGpsPingStoresExplicitSource(t *testing.T) {
	setupGpsPingDB(t)
	task, uid, email := seedClockedInTask(t)

	for _, source := range []string{gpsSourceForeground, gpsSourceBackground} {
		code, body := postGpsPing(t, task, uid, email,
			`{"lat": 45.4642, "lng": 9.19, "accuracy": 12, "source": "`+source+`"}`)
		if code != http.StatusOK {
			t.Fatalf("ping source=%s: want 200, got %d (%v)", source, code, body)
		}
	}

	got := gpsSources(t, task)
	if len(got) != 2 {
		t.Fatalf("want 2 pings, got %v", got)
	}
	// Read back in insertion order, which is the order they were posted above.
	if got[0] != gpsSourceForeground || got[1] != gpsSourceBackground {
		t.Fatalf("stored sources: want [foreground background], got %v", got)
	}
}

// An unlisted value would violate task_gps_pings_source_check and surface as a
// 500. The handler rejects it first, and nothing is written.
func TestGpsPingRejectsUnknownSource(t *testing.T) {
	setupGpsPingDB(t)
	task, uid, email := seedClockedInTask(t)

	code, body := postGpsPing(t, task, uid, email,
		`{"lat": 45.4642, "lng": 9.19, "source": "sneaky"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown source: want 400, got %d (%v)", code, body)
	}
	if got := gpsSources(t, task); len(got) != 0 {
		t.Fatalf("a rejected ping must not be stored, got %v", got)
	}
}

// The guard the background task self-heals on: no open worklog means 403, and
// the mobile task reads that as "stop tracking, this is orphaned".
func TestGpsPingWithoutOpenWorklogIsForbidden(t *testing.T) {
	setupGpsPingDB(t)
	task, uid, email := seedClockedInTask(t)

	if _, err := db.Exec(context.Background(),
		`UPDATE public.worklogs SET end_at = now() WHERE task_id = $1::uuid`, task); err != nil {
		t.Fatalf("clock out: %v", err)
	}

	code, body := postGpsPing(t, task, uid, email,
		`{"lat": 45.4642, "lng": 9.19, "source": "background"}`)
	if code != http.StatusForbidden {
		t.Fatalf("clocked-out ping: want 403, got %d (%v)", code, body)
	}
	if got := gpsSources(t, task); len(got) != 0 {
		t.Fatalf("a forbidden ping must not be stored, got %v", got)
	}
}
