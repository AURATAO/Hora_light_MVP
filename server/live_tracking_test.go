package main

// Live supporter tracking: the window, the states, the leak, and the one
// notification.
//
// Four layers, split the way the payments suites are — because they fail for
// different causes and a reader chasing a red line should be able to tell
// immediately which kind of failure they have:
//
//  1. The state machine (no DB). Pure arithmetic over a distance and two
//     booleans, table-driven, INCLUDING both boundaries — 100m and 500m are
//     the numbers a requester's copy turns on and the number the arrival push
//     fires at, so an off-by-one here is a wrong word on somebody's screen.
//
//  2. The window (DB). The property the privacy default rests on: a ping with
//     no open worklog is accepted in EXACTLY one situation and refused in
//     every other, including the ones that look like it.
//
//  3. The endpoint (DB). Who may read a live position, and — the part worth
//     more than the rest — who may not.
//
//  4. The arrival notification (DB). Exactly once, under concurrency.
//
//	docker run -d --rm --name hora-live-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55435:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55435/horatest' \
//	  go test ./ -run Live -v

import (
	"context"
	"encoding/json"
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

// ── 1. The state machine ───────────────────────────────────────────────────

// The thresholds, their boundaries, and the two rules that override them.
//
// Both boundaries are here on purpose. The spec reads ">500m on_the_way,
// 100-500m almost_there, <100m at_door", which pins exactly 100 to
// almost_there and exactly 500 to almost_there — the bands are closed at the
// bottom and open at the top. A reader should be able to check that by eye
// against this table, which is why it is a table.
func TestLiveStateThresholds(t *testing.T) {
	m := func(v float64) *float64 { return &v }

	cases := []struct {
		name      string
		distance  *float64
		clockedIn bool
		fresh     bool
		want      string
	}{
		{"far away", m(4200), false, true, liveStateOnTheWay},
		{"just outside the near band", m(500.01), false, true, liveStateOnTheWay},
		{"exactly 500m — the far boundary belongs to almost_there", m(500), false, true, liveStateAlmostThere},
		{"inside the near band", m(240), false, true, liveStateAlmostThere},
		{"exactly 100m — NOT yet at the door", m(100), false, true, liveStateAlmostThere},
		{"a hair inside 100m", m(99.99), false, true, liveStateAtDoor},
		{"on the doorstep", m(12), false, true, liveStateAtDoor},
		{"standing on the pin", m(0), false, true, liveStateAtDoor},

		// Clocked in beats everything, including a phone that has gone quiet.
		// The clock is a fact about the work; the GPS is a fact about the
		// phone, and only one of them is what the requester asked about.
		{"clocked in, close", m(30), true, true, liveStateWorking},
		{"clocked in, far (parked car, wrong pin)", m(3000), true, true, liveStateWorking},
		{"clocked in with a dead phone", m(30), true, false, liveStateWorking},
		{"clocked in with no position at all", nil, true, false, liveStateWorking},

		// Pre-clock-in, a stale fix is not a position.
		{"stale, was at the door", m(12), false, false, liveStateUnavailable},
		{"stale, was far", m(4200), false, false, liveStateUnavailable},
		{"never pinged", nil, false, false, liveStateUnavailable},

		// A task posted without coordinates. We know they set off; we cannot
		// know how close they are, and inventing a band would be a lie.
		{"fresh fix, task has no coordinates", nil, false, true, liveStateOnTheWay},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveState(tc.distance, tc.clockedIn, tc.fresh); got != tc.want {
				t.Errorf("liveState(%v, clockedIn=%v, fresh=%v) = %q, want %q",
					tc.distance, tc.clockedIn, tc.fresh, got, tc.want)
			}
		})
	}
}

// The distance the bands are cut from. Not a precision test — a sanity test
// that the formula is metres and not radians, degrees or kilometres, which is
// the way this function actually goes wrong.
func TestLiveHaversineIsMetres(t *testing.T) {
	// Two points on the same meridian, 0.001° of latitude apart ≈ 111.19m.
	if d := haversineMeters(45.4642, 9.1900, 45.4652, 9.1900); d < 110 || d > 113 {
		t.Errorf("0.001° of latitude = %.1fm, want ~111m", d)
	}
	// Identical points are zero, not NaN — the case that would make every
	// state 'on_the_way' if Atan2 were fed a negative square root.
	if d := haversineMeters(45.4642, 9.19, 45.4642, 9.19); d != 0 {
		t.Errorf("distance from a point to itself = %v, want 0", d)
	}
	// Milan to Rome, ~477km. Catches a radius expressed in kilometres.
	if d := haversineMeters(45.4642, 9.1900, 41.9028, 12.4964); d < 470_000 || d > 485_000 {
		t.Errorf("Milan→Rome = %.0fm, want ~477km", d)
	}
}

// ── The fixture ────────────────────────────────────────────────────────────

// The slice of production schema the live-tracking paths touch. tasks,
// worklogs and task_gps_pings are copied from
// supabase/migrations/20260711094158_remote_schema.sql; the two live-tracking
// migrations are then applied as the real files, so these tests meet the
// columns and the CHECK the handlers will meet in prod.
const liveFixture = `
DROP TABLE IF EXISTS public.task_gps_pings CASCADE;
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
	avatar_url text NOT NULL DEFAULT ''
);

CREATE TABLE public.tasks (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	title text NOT NULL DEFAULT '',
	requester text NOT NULL DEFAULT '',
	requester_id uuid REFERENCES public.users(id),
	assigned_to text NOT NULL DEFAULT '',
	assigned_to_id uuid REFERENCES public.users(id),
	status text NOT NULL DEFAULT 'open',
	job_lat double precision,
	job_lng double precision,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.worklogs (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	task_id uuid REFERENCES public.tasks(id) ON DELETE CASCADE,
	"user" text NOT NULL,
	start_at timestamptz NOT NULL DEFAULT now(),
	end_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE public.task_gps_pings (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	task_id uuid REFERENCES public.tasks(id) ON DELETE CASCADE,
	user_id uuid,
	lat double precision NOT NULL,
	lng double precision NOT NULL,
	accuracy integer,
	source text NOT NULL DEFAULT 'foreground',
	created_at timestamptz DEFAULT now()
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

CREATE TABLE public.device_push_tokens (
	id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
	expo_push_token text NOT NULL UNIQUE,
	platform text,
	created_at timestamptz NOT NULL DEFAULT now(),
	last_seen_at timestamptz NOT NULL DEFAULT now()
);
`

// The real files, in order. The enum one is listed second because that is the
// order it has to run in — and because listing it at all is the point: the
// handler emits SUPPORTER_ARRIVED, and a fixture whose enum lacks the value
// would fail the INSERT inside notify.Create, which only logs (this is the
// COMPLETED_SUPPORTER bug, and it is why arrivalCount below asserts on rows).
var liveMigrationPaths = []string{
	"../supabase/migrations/20260827120000_task_gps_pings_source.sql",
	"../supabase/migrations/20260917120000_task_enroute_live_tracking.sql",
	"../supabase/migrations/20260917130000_notification_type_supporter_arrived.sql",
}

func setupLiveDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed live-tracking tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, liveFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, path := range liveMigrationPaths {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", path, err)
		}
	}

	// notify.Create writes through database/sql, not the pgx pool, so both
	// handles have to point at the fixture or the arrival notification lands
	// in whatever the last suite left behind.
	prevPool, prevSQL := db, sqldb
	db = pool
	sqldb = stdlib.OpenDB(*pool.Config().ConnConfig)
	t.Cleanup(func() {
		_ = sqldb.Close()
		pool.Close()
		db, sqldb = prevPool, prevSQL
	})

	// The rate limiter is process-global and keyed per (user, task), and these
	// tests poll the same task far faster than a real client would. Reset it
	// between tests so one test's polling cannot 429 the next one's.
	liveRate.Lock()
	liveRate.seen = map[string]liveRateEntry{}
	liveRate.Unlock()
}

type liveWorld struct {
	taskID         string
	requesterID    string
	requesterEmail string
	supporterID    string
	supporterEmail string
	// Where the task is. Every coordinate below is offset from here so the
	// distances in the tests read as distances, not as decimals.
	jobLat, jobLng float64
}

// Distinct emails per world. users.email is not unique but profiles.email is
// the primary key, so a test that seeds two worlds (the rate limiter's, which
// needs a second task) would otherwise collide on the second profile.
var liveWorldSeq int

// A duomo-adjacent task in Milan, accepted and not yet started.
func seedLiveWorld(t *testing.T) liveWorld {
	t.Helper()
	ctx := context.Background()
	liveWorldSeq++
	w := liveWorld{
		requesterEmail: fmt.Sprintf("requester.live%d@example.test", liveWorldSeq),
		supporterEmail: fmt.Sprintf("supporter.live%d@example.test", liveWorldSeq),
		jobLat:         45.4642,
		jobLng:         9.1900,
	}
	ins := func(email, name string) string {
		var id string
		if err := db.QueryRow(ctx,
			`INSERT INTO public.users (email, name) VALUES ($1,$2) RETURNING id::text`, email, name,
		).Scan(&id); err != nil {
			t.Fatalf("seed user %s: %v", email, err)
		}
		return id
	}
	w.requesterID = ins(w.requesterEmail, "Rita Requester")
	w.supporterID = ins(w.supporterEmail, "Sam Supporter")
	if _, err := db.Exec(ctx,
		`INSERT INTO public.profiles (id, email, name, avatar_url) VALUES ($1::uuid,$2,$3,$4)`,
		w.supporterID, w.supporterEmail, "Sam Supporter", "https://cdn.example.test/sam.jpg",
	); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO public.tasks (title, requester, requester_id, assigned_to, assigned_to_id, status, job_lat, job_lng)
		VALUES ('Grocery run', $1, $2::uuid, $3, $4::uuid, 'open', $5, $6)
		RETURNING id::text
	`, w.requesterEmail, w.requesterID, w.supporterEmail, w.supporterID, w.jobLat, w.jobLng).Scan(&w.taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return w
}

// metresNorth converts a distance into a latitude offset, so a test can say
// "820m from the door" and mean it. 1° of latitude ≈ 111_320m everywhere.
func metresNorth(lat, metres float64) float64 { return lat + metres/111_320.0 }

func markEnroute(t *testing.T, taskID string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`UPDATE public.tasks SET enroute_at = now() WHERE id = $1::uuid`, taskID); err != nil {
		t.Fatalf("mark enroute: %v", err)
	}
}

func clockInAt(t *testing.T, taskID, email string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`INSERT INTO public.worklogs (task_id, "user", start_at) VALUES ($1::uuid, $2, now())`,
		taskID, email); err != nil {
		t.Fatalf("clock in: %v", err)
	}
}

func clockOutAll(t *testing.T, taskID string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`UPDATE public.worklogs SET end_at = now() WHERE task_id = $1::uuid AND end_at IS NULL`,
		taskID); err != nil {
		t.Fatalf("clock out: %v", err)
	}
}

// seedPing writes a position directly, at a chosen age — the only way to test
// the staleness branch without sleeping for two minutes.
func seedPing(t *testing.T, taskID, uid string, lat, lng float64, age time.Duration) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO public.task_gps_pings (task_id, user_id, lat, lng, source, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'enroute', now() - $5::interval)
	`, taskID, uid, lat, lng, fmt.Sprintf("%d seconds", int(age.Seconds()))); err != nil {
		t.Fatalf("seed ping: %v", err)
	}
}

func getLiveAs(t *testing.T, taskID, uid string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/tasks/"+taskID+"/live", nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)

	getLiveLocation(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func postEnrouteAs(t *testing.T, taskID, uid string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/enroute", nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)

	postEnroute(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func pingCount(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM public.task_gps_pings WHERE task_id = $1::uuid`, taskID).Scan(&n); err != nil {
		t.Fatalf("count pings: %v", err)
	}
	return n
}

func arrivalCount(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM public.notifications WHERE task_id = $1::uuid AND type = 'SUPPORTER_ARRIVED'`,
		taskID).Scan(&n); err != nil {
		t.Fatalf("count arrivals: %v", err)
	}
	return n
}

// ── 2. The window ──────────────────────────────────────────────────────────

// The supporter's tap, and the guard behind it.
func TestLiveEnrouteTapIsAssigneeOnlyAndIdempotent(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)

	// The requester cannot start sharing on the supporter's behalf. That is
	// the privacy default, and it is the whole reason this is a button.
	if code, _ := postEnrouteAs(t, w.taskID, w.requesterID); code != http.StatusForbidden {
		t.Errorf("requester tapping enroute: want 403, got %d", code)
	}
	var enrouteAt *time.Time
	if err := db.QueryRow(context.Background(),
		`SELECT enroute_at FROM public.tasks WHERE id = $1::uuid`, w.taskID).Scan(&enrouteAt); err != nil {
		t.Fatalf("read enroute_at: %v", err)
	}
	if enrouteAt != nil {
		t.Fatal("a refused tap still opened the window")
	}

	code, body := postEnrouteAs(t, w.taskID, w.supporterID)
	if code != http.StatusOK {
		t.Fatalf("supporter tapping enroute: want 200, got %d (%v)", code, body)
	}
	first, _ := body["enroute_at"].(string)
	if first == "" {
		t.Fatal("no enroute_at came back")
	}

	// A second tap — a double-tap, or a retry after a dropped response — must
	// not restart the window.
	code, body = postEnrouteAs(t, w.taskID, w.supporterID)
	if code != http.StatusOK {
		t.Fatalf("second tap: want 200, got %d (%v)", code, body)
	}
	if again, _ := body["enroute_at"].(string); again != first {
		t.Errorf("the second tap moved the timestamp: %q → %q", first, again)
	}
}

// Once the clock has started there is nothing for this button to do, and
// offering it would put the supporter in a state that shares nothing new.
func TestLiveEnrouteTapRefusedOnceStarted(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	clockInAt(t, w.taskID, w.supporterEmail)

	code, body := postEnrouteAs(t, w.taskID, w.supporterID)
	if code != http.StatusBadRequest {
		t.Fatalf("enroute after clock-in: want 400, got %d (%v)", code, body)
	}
	if got, _ := body["error"].(string); got != "already_started" {
		t.Errorf("error = %q, want already_started", got)
	}
}

// THE PROPERTY THE PRIVACY DEFAULT RESTS ON.
//
// A position with no open worklog is accepted in exactly one situation. Every
// row below that wants 403 is a way the exception could have been widened by
// accident — and the two after clock-out are the ones that matter most,
// because enroute_at is never cleared, so a window keyed on that column alone
// would stay open for the rest of the task's life.
func TestLivePingsWithoutWorklogScopedToEnrouteWindow(t *testing.T) {
	cases := []struct {
		name string
		// Applied to a freshly seeded, accepted, not-yet-started task.
		arrange func(t *testing.T, w liveWorld)
		source  string
		// Who sends it. Empty means the assigned supporter.
		asStranger bool
		want       int
	}{
		{
			"tapped On my way, not yet clocked in — THE one open case",
			func(t *testing.T, w liveWorld) { markEnroute(t, w.taskID) },
			"enroute", false, http.StatusOK,
		},
		{
			"never tapped — the privacy default, nothing is shared",
			func(t *testing.T, w liveWorld) {},
			"enroute", false, http.StatusForbidden,
		},
		{
			"tapped, then clocked OUT — sharing ends at clock-out",
			func(t *testing.T, w liveWorld) {
				markEnroute(t, w.taskID)
				clockInAt(t, w.taskID, w.supporterEmail)
				clockOutAll(t, w.taskID)
			},
			"enroute", false, http.StatusForbidden,
		},
		{
			"tapped, task completed — a finished task shares nothing",
			func(t *testing.T, w liveWorld) {
				markEnroute(t, w.taskID)
				mustExecLive(t, `UPDATE public.tasks SET status='completed' WHERE id=$1::uuid`, w.taskID)
			},
			"enroute", false, http.StatusForbidden,
		},
		{
			"tapped, task cancelled",
			func(t *testing.T, w liveWorld) {
				markEnroute(t, w.taskID)
				mustExecLive(t, `UPDATE public.tasks SET status='cancelled' WHERE id=$1::uuid`, w.taskID)
			},
			"enroute", false, http.StatusForbidden,
		},
		{
			"tapped, then reassigned to somebody else",
			func(t *testing.T, w liveWorld) {
				markEnroute(t, w.taskID)
				mustExecLive(t, `UPDATE public.tasks SET assigned_to_id = requester_id WHERE id=$1::uuid`, w.taskID)
			},
			"enroute", false, http.StatusForbidden,
		},
		{
			"a stranger claiming to be enroute on somebody else's task",
			func(t *testing.T, w liveWorld) { markEnroute(t, w.taskID) },
			"enroute", true, http.StatusForbidden,
		},
		// The exception is keyed on the SOURCE, not merely on the window. A
		// clocked-out phone still sending 'background' is refused exactly as it
		// is today, even with the window open — otherwise the enroute feature
		// would have quietly re-opened the path the 403-stops-tracking
		// self-heal depends on.
		{
			"window open, but the ping claims 'background'",
			func(t *testing.T, w liveWorld) { markEnroute(t, w.taskID) },
			"background", false, http.StatusForbidden,
		},
		{
			"window open, but the ping claims 'foreground'",
			func(t *testing.T, w liveWorld) { markEnroute(t, w.taskID) },
			"foreground", false, http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupLiveDB(t)
			w := seedLiveWorld(t)
			tc.arrange(t, w)

			uid, email := w.supporterID, w.supporterEmail
			if tc.asStranger {
				uid, email = w.requesterID, w.requesterEmail
			}
			code, body := postGpsPing(t, w.taskID, uid, email,
				fmt.Sprintf(`{"lat": %f, "lng": %f, "source": %q}`,
					metresNorth(w.jobLat, 3000), w.jobLng, tc.source))
			if code != tc.want {
				t.Fatalf("ping: want %d, got %d (%v)", tc.want, code, body)
			}
			wantRows := 0
			if tc.want == http.StatusOK {
				wantRows = 1
			}
			if got := pingCount(t, w.taskID); got != wantRows {
				t.Errorf("stored pings = %d, want %d", got, wantRows)
			}
		})
	}
}

// The other half: opening the enroute window must not have changed the
// clocked-in path at all. A supporter on the clock keeps posting foreground
// and background pings, and a source they never tap is still rejected.
func TestLiveClockedInPingPathUnchanged(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	clockInAt(t, w.taskID, w.supporterEmail)

	for _, source := range []string{gpsSourceForeground, gpsSourceBackground} {
		code, body := postGpsPing(t, w.taskID, w.supporterID, w.supporterEmail,
			fmt.Sprintf(`{"lat": 45.47, "lng": 9.19, "source": %q}`, source))
		if code != http.StatusOK {
			t.Errorf("clocked-in ping source=%s: want 200, got %d (%v)", source, code, body)
		}
	}
	if got := pingCount(t, w.taskID); got != 2 {
		t.Errorf("stored pings = %d, want 2", got)
	}
}

// ── 3. The endpoint ────────────────────────────────────────────────────────

// The states, end to end: a real ping, a real distance, the real handler.
// Same thresholds as the unit table above, now with the database and the
// haversine between the test and the answer.
func TestLiveEndpointDerivesStateFromRealPings(t *testing.T) {
	cases := []struct {
		name         string
		metresOut    float64
		age          time.Duration
		clockIn      bool
		wantState    string
		wantDistance bool
	}{
		{"three streets away", 820, 5 * time.Second, false, liveStateOnTheWay, true},
		{"round the corner", 300, 5 * time.Second, false, liveStateAlmostThere, true},
		{"at the door", 40, 5 * time.Second, false, liveStateAtDoor, true},
		{"clocked in", 40, 5 * time.Second, true, liveStateWorking, true},
		// Older than the two-minute ceiling. The position is still returned —
		// the requester wants to see where they were — but the DISTANCE is
		// withheld, because "0.8 mi away" about a five-minute-old fix is a
		// claim about the present that the data cannot support.
		{"gone quiet", 820, 5 * time.Minute, false, liveStateUnavailable, false},
		{"just over the staleness line", 300, 150 * time.Second, false, liveStateUnavailable, false},
		{"just inside it", 300, 90 * time.Second, false, liveStateAlmostThere, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupLiveDB(t)
			w := seedLiveWorld(t)
			markEnroute(t, w.taskID)
			if tc.clockIn {
				clockInAt(t, w.taskID, w.supporterEmail)
			}
			seedPing(t, w.taskID, w.supporterID, metresNorth(w.jobLat, tc.metresOut), w.jobLng, tc.age)

			code, body := getLiveAs(t, w.taskID, w.requesterID)
			if code != http.StatusOK {
				t.Fatalf("live: want 200, got %d (%v)", code, body)
			}
			if got, _ := body["state"].(string); got != tc.wantState {
				t.Errorf("state = %q, want %q", got, tc.wantState)
			}
			_, hasDistance := body["distance_m"].(float64)
			if hasDistance != tc.wantDistance {
				t.Errorf("distance_m present = %v, want %v (got %v)", hasDistance, tc.wantDistance, body["distance_m"])
			}
			if tc.wantDistance {
				d, _ := body["distance_m"].(float64)
				if d < tc.metresOut*0.97 || d > tc.metresOut*1.03 {
					t.Errorf("distance_m = %.0f, want ~%.0f", d, tc.metresOut)
				}
			}
			// The position is always shipped, stale or not — an empty map is
			// worse than an honestly-labelled old dot.
			if _, ok := body["lat"].(float64); !ok {
				t.Errorf("lat missing from the payload: %v", body)
			}
		})
	}
}

// A requester who opens the screen before anything has happened gets a usable
// answer, not a 404 the client has to special-case into a state of its own.
func TestLiveEndpointBeforeAnyPing(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)

	code, body := getLiveAs(t, w.taskID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("live with no pings: want 200, got %d (%v)", code, body)
	}
	if got, _ := body["state"].(string); got != liveStateUnavailable {
		t.Errorf("state = %q, want %q", got, liveStateUnavailable)
	}
	if body["lat"] != nil || body["distance_m"] != nil {
		t.Errorf("a task with no pings carried a position: %v", body)
	}
	// The destination and the supporter's marker are still there: the map can
	// draw the pin and the card can name who it is waiting for.
	dest, _ := body["destination"].(map[string]any)
	if dest == nil || dest["lat"] == nil {
		t.Errorf("destination missing: %v", body)
	}
	sup, _ := body["supporter"].(map[string]any)
	if name, _ := sup["name"].(string); name != "Sam Supporter" {
		t.Errorf("supporter name = %q, want Sam Supporter", name)
	}
}

// THE LEAK TEST. Extends the pattern from payments_phase3_test.go to the thing
// this feature puts on the wire: a live human being's position.
//
// Requester-only, and 404 rather than 403 for everyone else — including the
// supporter, whose own position it is. A supporter probing this endpoint
// should not be able to tell "you may not read this" from "there is nothing
// here", and no legitimate client asks.
func TestLiveEndpointIsRequesterOnly(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	markEnroute(t, w.taskID)
	seedPing(t, w.taskID, w.supporterID, metresNorth(w.jobLat, 120), w.jobLng, 5*time.Second)

	// A third party with no connection to the task at all.
	var strangerID string
	if err := db.QueryRow(context.Background(),
		`INSERT INTO public.users (email, name) VALUES ('nosy@example.test','Nosy') RETURNING id::text`,
	).Scan(&strangerID); err != nil {
		t.Fatalf("seed stranger: %v", err)
	}

	for _, who := range []struct {
		name string
		uid  string
	}{
		{"the assigned supporter", w.supporterID},
		{"a stranger", strangerID},
	} {
		code, body := getLiveAs(t, w.taskID, who.uid)
		if code != http.StatusNotFound {
			t.Errorf("%s reading /live: want 404, got %d (%v)", who.name, code, body)
		}
		raw, _ := json.Marshal(body)
		for _, secret := range []string{"45.46", "9.19", "distance_m", "state", "destination", "avatar_url"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s got %q in the body: %s", who.name, secret, raw)
			}
		}
	}

	// And the requester, who may, gets the real thing.
	code, body := getLiveAs(t, w.taskID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("requester reading /live: want 200, got %d (%v)", code, body)
	}
	if got, _ := body["state"].(string); got != liveStateAlmostThere {
		t.Errorf("state = %q, want %q", got, liveStateAlmostThere)
	}
}

// "Only while active." A position is live or it is nothing: once the task is
// finished, cancelled, taken down, or the supporter has been detached, the
// requester does not get to keep watching the last dot.
func TestLiveEndpointOnlyWhileActive(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(t *testing.T, w liveWorld)
	}{
		{"completed", func(t *testing.T, w liveWorld) {
			mustExecLive(t, `UPDATE public.tasks SET status='completed' WHERE id=$1::uuid`, w.taskID)
		}},
		{"cancelled", func(t *testing.T, w liveWorld) {
			mustExecLive(t, `UPDATE public.tasks SET status='cancelled' WHERE id=$1::uuid`, w.taskID)
		}},
		{"removed by the platform", func(t *testing.T, w liveWorld) {
			mustExecLive(t, `UPDATE public.tasks SET status='removed' WHERE id=$1::uuid`, w.taskID)
		}},
		{"supporter detached", func(t *testing.T, w liveWorld) {
			mustExecLive(t, `UPDATE public.tasks SET assigned_to_id=NULL, assigned_to='' WHERE id=$1::uuid`, w.taskID)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupLiveDB(t)
			w := seedLiveWorld(t)
			markEnroute(t, w.taskID)
			seedPing(t, w.taskID, w.supporterID, metresNorth(w.jobLat, 60), w.jobLng, 5*time.Second)
			tc.arrange(t, w)

			code, body := getLiveAs(t, w.taskID, w.requesterID)
			if code != http.StatusNotFound {
				t.Fatalf("live on a %s task: want 404, got %d (%v)", tc.name, code, body)
			}
		})
	}
}

// The limiter exists for a runaway client, not an attacker — a screen whose
// interval was never cleared, or a retry loop with no backoff. What is under
// test is that a real 15s poll is nowhere near it and a runaway one is caught.
func TestLiveEndpointRateLimit(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)

	// A 30s window at the real 15s cadence is 2 calls. Four is two screens
	// open on two devices, and must sail through.
	for i := 0; i < 4; i++ {
		if code, _ := getLiveAs(t, w.taskID, w.requesterID); code != http.StatusOK {
			t.Fatalf("poll %d of a normal cadence was limited (%d)", i+1, code)
		}
	}
	// A loose interval. Somewhere past the burst it starts refusing, and it
	// says how long to wait rather than just saying no.
	limited := false
	for i := 0; i < liveRateBurst*2; i++ {
		if code, _ := getLiveAs(t, w.taskID, w.requesterID); code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("a runaway poller was never limited")
	}

	// The budget is per (user, task): one screen misbehaving must not lock the
	// requester out of a different task they also have open.
	other := seedLiveWorld(t)
	mustExecLive(t, `UPDATE public.tasks SET requester_id=$2::uuid, requester=$3 WHERE id=$1::uuid`,
		other.taskID, w.requesterID, w.requesterEmail)
	if code, _ := getLiveAs(t, other.taskID, w.requesterID); code != http.StatusOK {
		t.Errorf("a second task was limited by the first task's budget (%d)", code)
	}
}

// ── 4. The arrival notification ────────────────────────────────────────────

// Once per task, and not once per ping. The supporter parks, walks in, walks
// out to the car and back — every one of those pings is inside 100m and only
// the first says anything.
func TestLiveArrivalNotifiesExactlyOnce(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	markEnroute(t, w.taskID)

	// Far out: no arrival.
	postLivePing(t, w, metresNorth(w.jobLat, 3000))
	if got := arrivalCount(t, w.taskID); got != 0 {
		t.Fatalf("arrivals while 3km away = %d, want 0", got)
	}
	// Close, but not there — the boundary is 100m and 140m is not inside it.
	postLivePing(t, w, metresNorth(w.jobLat, 140))
	if got := arrivalCount(t, w.taskID); got != 0 {
		t.Fatalf("arrivals at 140m = %d, want 0 (the threshold is %.0fm)", got, arrivalRadiusMeters)
	}

	// Arrived.
	postLivePing(t, w, metresNorth(w.jobLat, 30))
	if got := arrivalCount(t, w.taskID); got != 1 {
		t.Fatalf("arrivals at 30m = %d, want 1", got)
	}

	// Everything after it, including leaving and coming back, is silent.
	for _, m := range []float64{10, 60, 900, 20} {
		postLivePing(t, w, metresNorth(w.jobLat, m))
	}
	if got := arrivalCount(t, w.taskID); got != 1 {
		t.Errorf("arrivals after five more pings = %d, want 1", got)
	}

	// It went to the requester, and it says who arrived.
	var userID, title string
	if err := db.QueryRow(context.Background(), `
		SELECT user_id::text, title FROM public.notifications
		WHERE task_id = $1::uuid AND type = 'SUPPORTER_ARRIVED'
	`, w.taskID).Scan(&userID, &title); err != nil {
		t.Fatalf("read notification: %v", err)
	}
	if userID != w.requesterID {
		t.Errorf("arrival notification went to %s, want the requester %s", userID, w.requesterID)
	}
	if !strings.Contains(title, "has arrived") {
		t.Errorf("title = %q, want it to say somebody has arrived", title)
	}
}

// The latch is a guarded UPDATE, not a read-then-write, precisely so this
// holds: several pings crossing the threshold at once still produce one
// notification. The read-then-write version passes the test above and fails
// this one.
func TestLiveArrivalIsExactlyOnceUnderConcurrency(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	markEnroute(t, w.taskID)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postLivePing(t, w, metresNorth(w.jobLat, 25))
		}()
	}
	wg.Wait()

	if got := arrivalCount(t, w.taskID); got != 1 {
		t.Errorf("arrivals from 8 concurrent doorstep pings = %d, want exactly 1", got)
	}
}

// After clock-in the requester has already been told, by the clock-in
// notification. A supporter walking back from a parked car is not an arrival.
func TestLiveArrivalDoesNotFireAfterClockIn(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	clockInAt(t, w.taskID, w.supporterEmail)

	code, body := postGpsPing(t, w.taskID, w.supporterID, w.supporterEmail,
		fmt.Sprintf(`{"lat": %f, "lng": %f, "source": "background"}`, metresNorth(w.jobLat, 15), w.jobLng))
	if code != http.StatusOK {
		t.Fatalf("clocked-in doorstep ping: want 200, got %d (%v)", code, body)
	}
	if got := arrivalCount(t, w.taskID); got != 0 {
		t.Errorf("arrivals from a clocked-in ping = %d, want 0", got)
	}
}

// A task with no coordinates can never fire the arrival push — there is no
// destination to be near. Silence is the right failure: the alternative is
// announcing an arrival nothing verified.
func TestLiveArrivalSilentWithoutTaskCoordinates(t *testing.T) {
	setupLiveDB(t)
	w := seedLiveWorld(t)
	mustExecLive(t, `UPDATE public.tasks SET job_lat=NULL, job_lng=NULL WHERE id=$1::uuid`, w.taskID)
	markEnroute(t, w.taskID)

	postLivePing(t, w, w.jobLat)
	if got := arrivalCount(t, w.taskID); got != 0 {
		t.Errorf("arrivals on a task with no coordinates = %d, want 0", got)
	}
	// The ping itself still lands — tracking works, only the push is absent.
	if got := pingCount(t, w.taskID); got != 1 {
		t.Errorf("stored pings = %d, want 1", got)
	}
	// And the state falls back rather than failing.
	code, body := getLiveAs(t, w.taskID, w.requesterID)
	if code != http.StatusOK {
		t.Fatalf("live without coordinates: want 200, got %d (%v)", code, body)
	}
	if got, _ := body["state"].(string); got != liveStateOnTheWay {
		t.Errorf("state = %q, want %q", got, liveStateOnTheWay)
	}
	if body["distance_m"] != nil {
		t.Errorf("distance_m = %v, want null with no destination", body["distance_m"])
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func postLivePing(t *testing.T, w liveWorld, lat float64) {
	t.Helper()
	code, body := postGpsPing(t, w.taskID, w.supporterID, w.supporterEmail,
		fmt.Sprintf(`{"lat": %f, "lng": %f, "source": "enroute"}`, lat, w.jobLng))
	if code != http.StatusOK {
		t.Fatalf("enroute ping: want 200, got %d (%v)", code, body)
	}
}

func mustExecLive(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
