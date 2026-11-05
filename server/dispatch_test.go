package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// D-24: supporter push dispatch.

const dispatchFixture = `
DROP TABLE IF EXISTS public.task_dispatches CASCADE;
DROP TABLE IF EXISTS public.device_push_tokens CASCADE;
DROP TABLE IF EXISTS public.profiles CASCADE;
DROP TABLE IF EXISTS public.tasks CASCADE;
DROP TABLE IF EXISTS public.users CASCADE;

CREATE TABLE public.users (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	email text,
	name text,
	is_sandbox boolean NOT NULL DEFAULT false
);

CREATE TABLE public.profiles (
	id uuid,
	email text PRIMARY KEY,
	name text,
	is_verified_supporter boolean NOT NULL DEFAULT false
);

CREATE TABLE public.tasks (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	title text NOT NULL DEFAULT '',
	category text NOT NULL DEFAULT 'delivery',
	requester_id uuid REFERENCES public.users(id),
	assigned_to_id uuid REFERENCES public.users(id),
	status text NOT NULL DEFAULT 'open',
	estimated_minutes integer NOT NULL DEFAULT 30,
	rate_cents_per_min integer NOT NULL DEFAULT 50,
	is_immediate boolean NOT NULL DEFAULT true,
	scheduled_at timestamptz,
	location_text text,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.device_push_tokens (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
	expo_push_token text NOT NULL UNIQUE,
	created_at timestamptz NOT NULL DEFAULT now()
);
`

type dispatchRecorder struct {
	mu     sync.Mutex
	pushes []string // "userID|title|body"
	ops    []string // task ids
}

func (r *dispatchRecorder) to() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]bool{}
	for _, p := range r.pushes {
		m[strings.SplitN(p, "|", 2)[0]] = true
	}
	return m
}

func setupDispatchDB(t *testing.T) *dispatchRecorder {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		requireOrSkipDB(t, "TEST_DATABASE_URL not set — skipping DB-backed dispatch tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, dispatchFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	prevPool, prevSQL := db, sqldb
	db = pool
	sqldb = stdlib.OpenDB(*pool.Config().ConnConfig)
	applySafetyMigration(t)
	mig, err := os.ReadFile("../supabase/migrations/20261106120000_supporter_dispatch.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("migration: %v", err)
	}

	rec := &dispatchRecorder{}
	prevPush, prevOps, prevBorough := dispatchPushFn, dispatchOpsFn, dispatchBoroughFn
	dispatchPushFn = func(_ context.Context, userID, _ string, title, body string) {
		rec.mu.Lock()
		rec.pushes = append(rec.pushes, userID+"|"+title+"|"+body)
		rec.mu.Unlock()
	}
	dispatchOpsFn = func(taskID, _ string, _, _ int) {
		rec.mu.Lock()
		rec.ops = append(rec.ops, taskID)
		rec.mu.Unlock()
	}
	dispatchBoroughFn = func(_ context.Context, loc string) string {
		if strings.Contains(loc, "Brooklyn") {
			return "Brooklyn"
		}
		if strings.Contains(loc, "nowhere") {
			return ""
		}
		return "Manhattan"
	}
	t.Setenv("DISPATCH_PUSH_ENABLED", "1")
	t.Cleanup(func() {
		dispatchWG.Wait()
		dispatchPushFn, dispatchOpsFn, dispatchBoroughFn = prevPush, prevOps, prevBorough
		_ = sqldb.Close()
		pool.Close()
		db, sqldb = prevPool, prevSQL
	})
	return rec
}

var dispatchSeq int

// seedPerson inserts a user + profile. boroughs nil = none chosen.
func seedPerson(t *testing.T, verified, available, token, sandbox bool, boroughs []string) string {
	t.Helper()
	ctx := context.Background()
	dispatchSeq++
	email := fmt.Sprintf("p%d@example.test", dispatchSeq)
	var id string
	if err := db.QueryRow(ctx, `insert into public.users (email, name, is_sandbox) values ($1, 'P', $2) returning id::text`,
		email, sandbox).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var until *time.Time
	if available {
		u := time.Now().Add(time.Hour)
		until = &u
	}
	if boroughs == nil {
		boroughs = []string{}
	}
	if _, err := db.Exec(ctx, `insert into public.profiles (id, email, name, is_verified_supporter, dispatch_boroughs, available_until)
		values ($1::uuid, $2, 'P', $3, $4, $5)`, id, email, verified, boroughs, until); err != nil {
		t.Fatal(err)
	}
	if token {
		if _, err := db.Exec(ctx, `insert into public.device_push_tokens (user_id, expo_push_token) values ($1::uuid, $2)`,
			id, "ExponentPushToken["+email+"]"); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func seedDispatchTask(t *testing.T, requesterID, location string, immediate bool) string {
	t.Helper()
	var id string
	var when *time.Time
	if !immediate {
		w := time.Now().Add(48 * time.Hour)
		when = &w
	}
	if err := db.QueryRow(context.Background(), `insert into public.tasks (title, requester_id, location_text, is_immediate, scheduled_at)
		values ('Pick up a box', $1::uuid, $2, $3, $4) returning id::text`, requesterID, location, immediate, when).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDispatchRoundsAndOps(t *testing.T) {
	rec := setupDispatchDB(t)
	ctx := context.Background()
	requester := seedPerson(t, false, false, false, false, nil)
	manhattan := seedPerson(t, true, true, true, false, []string{"Manhattan"})
	brooklyn := seedPerson(t, true, true, true, false, []string{"Brooklyn"})
	noChoice := seedPerson(t, true, true, true, false, nil)
	unavailable := seedPerson(t, true, false, true, false, []string{"Manhattan"})
	unverified := seedPerson(t, false, true, true, false, []string{"Manhattan"})
	noToken := seedPerson(t, true, true, false, false, []string{"Manhattan"})
	sandboxed := seedPerson(t, true, true, true, true, []string{"Manhattan"})
	blocked := seedPerson(t, true, true, true, false, []string{"Manhattan"})
	if _, err := db.Exec(ctx, `insert into public.user_blocks (blocker_id, blocked_id) values ($1::uuid, $2::uuid)`, requester, blocked); err != nil {
		t.Fatal(err)
	}

	task := seedDispatchTask(t, requester, "280 Mercer St, New York, NY", true)
	startDispatch(task)
	dispatchWG.Wait()

	got := rec.to()
	if !got[manhattan] || len(got) != 1 {
		t.Fatalf("round 1 should reach only the Manhattan supporter: %v (manhattan=%s)", got, manhattan)
	}
	for _, id := range []string{brooklyn, noChoice, unavailable, unverified, noToken, sandboxed, blocked, requester} {
		if got[id] {
			t.Errorf("round 1 reached %s", id)
		}
	}
	if p := rec.pushes[0]; !strings.Contains(p, "New task near you · ~$") || !strings.Contains(p, "Pick up a box · Manhattan · Now") ||
		strings.Contains(p, "Mercer") {
		t.Errorf("push copy %q: want pay, borough, Now, and no address", p)
	}

	// A second announce for the same task does nothing.
	startDispatch(task)
	dispatchWG.Wait()
	if len(rec.pushes) != 1 {
		t.Fatalf("a second announce pushed again: %d", len(rec.pushes))
	}

	// Before 3 minutes: nothing more.
	sweepDispatches(ctx, time.Now().Add(2*time.Minute))
	if len(rec.pushes) != 1 {
		t.Fatalf("widened early: %d pushes", len(rec.pushes))
	}

	// After 3: everyone else available and eligible, once.
	sweepDispatches(ctx, time.Now().Add(3*time.Minute+time.Second))
	sweepDispatches(ctx, time.Now().Add(3*time.Minute+2*time.Second))
	got = rec.to()
	if !got[brooklyn] || !got[noChoice] || len(rec.pushes) != 3 {
		t.Fatalf("round 2 should add Brooklyn and no-choice exactly once: %v (%d pushes)", got, len(rec.pushes))
	}
	if len(rec.ops) != 0 {
		t.Fatalf("ops alerted before 5 min")
	}

	// After 5: ops, once; then the row is closed.
	sweepDispatches(ctx, time.Now().Add(5*time.Minute+time.Second))
	sweepDispatches(ctx, time.Now().Add(6*time.Minute))
	if len(rec.ops) != 1 || rec.ops[0] != task {
		t.Fatalf("ops alerts %v, want exactly one for %s", rec.ops, task)
	}
}

func TestDispatchStopsWhenAccepted(t *testing.T) {
	rec := setupDispatchDB(t)
	ctx := context.Background()
	requester := seedPerson(t, false, false, false, false, nil)
	a := seedPerson(t, true, true, true, false, []string{"Manhattan"})
	seedPerson(t, true, true, true, false, []string{"Brooklyn"})
	task := seedDispatchTask(t, requester, "280 Mercer St", true)
	startDispatch(task)
	dispatchWG.Wait()
	if _, err := db.Exec(ctx, `update public.tasks set assigned_to_id = $2::uuid where id = $1::uuid`, task, a); err != nil {
		t.Fatal(err)
	}
	sweepDispatches(ctx, time.Now().Add(10*time.Minute))
	if len(rec.pushes) != 1 || len(rec.ops) != 0 {
		t.Fatalf("an accepted task kept dispatching: %d pushes, %d ops", len(rec.pushes), len(rec.ops))
	}
}

func TestDispatchNoBoroughGoesToEveryone(t *testing.T) {
	rec := setupDispatchDB(t)
	requester := seedPerson(t, false, false, false, false, nil)
	a := seedPerson(t, true, true, true, false, []string{"Manhattan"})
	b := seedPerson(t, true, true, true, false, nil)
	task := seedDispatchTask(t, requester, "somewhere nowhere", true)
	startDispatch(task)
	dispatchWG.Wait()
	got := rec.to()
	if !got[a] || !got[b] {
		t.Fatalf("no borough must reach everyone available: %v", got)
	}
	sweepDispatches(context.Background(), time.Now().Add(4*time.Minute))
	if len(rec.pushes) != 2 {
		t.Fatalf("no-borough task widened again: %d", len(rec.pushes))
	}
}

func TestDispatchScheduledTaskNoOpsAlert(t *testing.T) {
	rec := setupDispatchDB(t)
	requester := seedPerson(t, false, false, false, false, nil)
	seedPerson(t, true, true, true, false, []string{"Manhattan"})
	task := seedDispatchTask(t, requester, "280 Mercer St", false)
	startDispatch(task)
	dispatchWG.Wait()
	if !strings.Contains(rec.pushes[0], "Pick up a box · Manhattan · ") || strings.HasSuffix(rec.pushes[0], " · Now") {
		t.Errorf("scheduled push should carry the time: %q", rec.pushes[0])
	}
	sweepDispatches(context.Background(), time.Now().Add(10*time.Minute))
	if len(rec.ops) != 0 {
		t.Fatalf("a scheduled task alerted ops")
	}
}

func TestDispatchOffDoesNothing(t *testing.T) {
	rec := setupDispatchDB(t)
	t.Setenv("DISPATCH_PUSH_ENABLED", "")
	requester := seedPerson(t, false, false, false, false, nil)
	seedPerson(t, true, true, true, false, []string{"Manhattan"})
	startDispatch(seedDispatchTask(t, requester, "280 Mercer St", true))
	dispatchWG.Wait()
	if len(rec.pushes) != 0 {
		t.Fatalf("dispatch ran with the flag off")
	}
}

func TestDispatchSettingsEndpoints(t *testing.T) {
	setupDispatchDB(t)
	gin.SetMode(gin.TestMode)
	supporter := seedPerson(t, true, false, true, false, nil)
	plain := seedPerson(t, false, false, false, false, nil)
	call := func(uid, method, path, body string) (int, string) {
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("uid", uid); c.Next() })
		r.GET("/me/dispatch", getMyDispatch)
		r.PUT("/me/dispatch", putMyDispatch)
		r.POST("/me/availability", postMyAvailability)
		r.GET("/config", getAppConfig)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := call(supporter, "PUT", "/me/dispatch", `{"boroughs":["manhattan","Bronx","Manhattan"]}`); code != 200 ||
		!strings.Contains(body, `"boroughs":["Manhattan","the Bronx"]`) {
		t.Fatalf("put boroughs: %d %s", code, body)
	}
	if code, _ := call(supporter, "PUT", "/me/dispatch", `{"boroughs":["Hoboken"]}`); code != 400 {
		t.Fatalf("unknown borough: %d", code)
	}
	if code, body := call(supporter, "POST", "/me/availability", `{"on":true}`); code != 200 || !strings.Contains(body, `"available":true`) {
		t.Fatalf("go available: %d %s", code, body)
	}
	var until time.Time
	if err := db.QueryRow(context.Background(), `select available_until from public.profiles where id = $1::uuid`, supporter).Scan(&until); err != nil ||
		until.Before(time.Now().Add(availabilityLasts-time.Minute)) || until.After(time.Now().Add(availabilityLasts+time.Minute)) {
		t.Fatalf("available_until %v, want now + %s", until, availabilityLasts)
	}
	if code, body := call(supporter, "POST", "/me/availability", `{"on":false}`); code != 200 || !strings.Contains(body, `"available":false`) {
		t.Fatalf("go unavailable: %d %s", code, body)
	}
	if code, _ := call(plain, "POST", "/me/availability", `{"on":true}`); code != http.StatusForbidden {
		t.Fatalf("a non-supporter went available: %d", code)
	}
	if code, body := call("", "GET", "/config", ""); code != 200 || !strings.Contains(body, `"dispatch":true`) {
		t.Fatalf("config: %d %s", code, body)
	}
}

func TestNormalizeBoroughs(t *testing.T) {
	got, ok := normalizeBoroughs([]string{" brooklyn ", "THE BRONX", "staten island"})
	if !ok || strings.Join(got, ",") != "Brooklyn,the Bronx,Staten Island" {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := normalizeBoroughs([]string{"Jersey City"}); ok {
		t.Fatal("Jersey City accepted")
	}
}
