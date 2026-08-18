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

// End-to-end coverage for the Traction 3 questionnaire against a real Postgres:
// the handlers, the SQL, and the constraints the migration adds, together.
// Skipped unless TEST_DATABASE_URL points at a throwaway database — this
// creates and drops tables, so never aim it at anything real.
//
//	docker run -d --name hora-review-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55433:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55433/horatest' \
//	  go test ./ -run Traction3 -v

// Minimal slice of the production schema these handlers touch. The reviews DDL
// is copied verbatim from supabase/migrations/20260711094158_remote_schema.sql
// so the new migration runs against exactly the table it will meet in prod.
const traction3Fixture = `
DROP TABLE IF EXISTS public.reviews CASCADE;
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

CREATE TABLE public.reviews (
	"id" uuid DEFAULT gen_random_uuid() NOT NULL,
	"task_id" uuid,
	"reviewer_id" uuid,
	"supporter_id" uuid,
	"stars" integer,
	"value_rating" text,
	"would_rehire" boolean,
	"comment" text,
	"created_at" timestamp with time zone DEFAULT now(),
	CONSTRAINT "reviews_stars_check" CHECK ((("stars" >= 1) AND ("stars" <= 5))),
	CONSTRAINT "reviews_value_rating_check" CHECK (("value_rating" = ANY (ARRAY['not_worth'::text, 'fair'::text, 'great'::text])))
);
ALTER TABLE ONLY public.reviews ADD CONSTRAINT reviews_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.reviews ADD CONSTRAINT reviews_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id);
ALTER TABLE ONLY public.reviews ADD CONSTRAINT reviews_reviewer_id_fkey FOREIGN KEY (reviewer_id) REFERENCES public.users(id);
ALTER TABLE ONLY public.reviews ADD CONSTRAINT reviews_supporter_id_fkey FOREIGN KEY (supporter_id) REFERENCES public.users(id);
ALTER TABLE public.reviews ENABLE ROW LEVEL SECURITY;
`

const traction3MigrationPath = "../supabase/migrations/20260818034329_reviews_traction3_questionnaire.sql"

func setupTraction3DB(t *testing.T) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed review tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(ctx, traction3Fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// The real migration file, not a paraphrase of it.
	migration, err := os.ReadFile(traction3MigrationPath)
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

// seedCompletedTask returns (taskID, requesterID, supporterID).
func seedCompletedTask(t *testing.T, status string) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	var requester, supporter, task string
	if err := db.QueryRow(ctx, `INSERT INTO public.users DEFAULT VALUES RETURNING id::text`).Scan(&requester); err != nil {
		t.Fatalf("seed requester: %v", err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO public.users DEFAULT VALUES RETURNING id::text`).Scan(&supporter); err != nil {
		t.Fatalf("seed supporter: %v", err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO public.tasks (title, requester_id, assigned_to_id, status)
		VALUES ('Coffee run', $1::uuid, $2::uuid, $3) RETURNING id::text
	`, requester, supporter, status).Scan(&task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task, requester, supporter
}

func postReview(t *testing.T, taskID, uid, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/review", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	c.Set("uid", uid)

	createReview(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// profileAggregate runs the real profile read path and returns (count, avg).
func profileAggregate(t *testing.T, supporterID string) (int, *float64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/profiles/"+supporterID+"/reviews", nil)
	c.Params = gin.Params{{Key: "id", Value: supporterID}}

	listProfileReviews(c)

	var out struct {
		Reviews  []map[string]any `json:"reviews"`
		Count    int              `json:"count"`
		AvgStars *float64         `json:"avg_stars"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode aggregate: %v (body=%s)", err, w.Body.String())
	}
	if out.Count != len(out.Reviews) {
		t.Fatalf("count %d disagrees with %d listed reviews", out.Count, len(out.Reviews))
	}
	return out.Count, out.AvgStars
}

func countReviews(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM public.reviews WHERE task_id = $1::uuid`, taskID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestTraction3RequesterStarsReachSupporterProfile(t *testing.T) {
	setupTraction3DB(t)
	task, requester, supporter := seedCompletedTask(t, "completed")

	beforeCount, beforeAvg := profileAggregate(t, supporter)
	t.Logf("BEFORE  supporter profile: count=%d avg_stars=%v", beforeCount, beforeAvg)
	if beforeCount != 0 || beforeAvg != nil {
		t.Fatalf("expected an empty profile before the review, got count=%d avg=%v", beforeCount, beforeAvg)
	}

	code, body := postReview(t, task, requester, `{
		"stars": 5,
		"ease_rating": "very_easy",
		"would_use_again": "maybe_cost",
		"open_feedback": "Let me re-run a past task in one tap."
	}`)
	if code != http.StatusCreated {
		t.Fatalf("submit: want 201, got %d (%v)", code, body)
	}
	if body["rater_role"] != "requester" {
		t.Errorf("rater_role: want requester, got %v", body["rater_role"])
	}
	if body["supporter_id"] != supporter {
		t.Errorf("ratee: want the task's supporter %s, got %v", supporter, body["supporter_id"])
	}
	if body["stars"] != float64(5) {
		t.Errorf("stars: want 5, got %v", body["stars"])
	}

	afterCount, afterAvg := profileAggregate(t, supporter)
	t.Logf("AFTER   supporter profile: count=%d avg_stars=%v", afterCount, *afterAvg)
	if afterCount != 1 || afterAvg == nil || *afterAvg != 5 {
		t.Fatalf("stars did not reach the profile: count=%d avg=%v", afterCount, afterAvg)
	}

	// open_feedback is product feedback, not a public review comment: it must
	// be stored but must not appear in the profile payload.
	var stored string
	if err := db.QueryRow(context.Background(),
		`SELECT open_feedback FROM public.reviews WHERE task_id=$1::uuid`, task).Scan(&stored); err != nil {
		t.Fatalf("read open_feedback: %v", err)
	}
	if stored == "" {
		t.Error("open_feedback was not persisted")
	}
}

func TestTraction3SupporterRowRatesNobody(t *testing.T) {
	setupTraction3DB(t)
	task, requester, supporter := seedCompletedTask(t, "completed")

	if code, body := postReview(t, task, requester, `{"stars":4,"ease_rating":"easy","would_use_again":"yes"}`); code != http.StatusCreated {
		t.Fatalf("requester submit: want 201, got %d (%v)", code, body)
	}
	beforeCount, beforeAvg := profileAggregate(t, supporter)
	t.Logf("BEFORE  supporter's own questionnaire: profile count=%d avg=%v", beforeCount, *beforeAvg)

	// Same task, other side — must be accepted alongside the requester's row.
	code, body := postReview(t, task, supporter, `{
		"ease_rating": "neutral",
		"would_use_again": "maybe_task",
		"open_feedback": "Clock-out needs a confirmation step."
	}`)
	if code != http.StatusCreated {
		t.Fatalf("supporter submit: want 201, got %d (%v)", code, body)
	}
	if body["rater_role"] != "supporter" {
		t.Errorf("rater_role: want supporter, got %v", body["rater_role"])
	}
	if body["stars"] != nil {
		t.Errorf("stars: want null on a supporter row, got %v", body["stars"])
	}
	if body["supporter_id"] != nil {
		t.Errorf("ratee: want null on a supporter row, got %v", body["supporter_id"])
	}
	if n := countReviews(t, task); n != 2 {
		t.Errorf("want 2 rows on this task (one per side), got %d", n)
	}

	afterCount, afterAvg := profileAggregate(t, supporter)
	t.Logf("AFTER   supporter's own questionnaire: profile count=%d avg=%v", afterCount, *afterAvg)
	if afterCount != beforeCount || *afterAvg != *beforeAvg {
		t.Errorf("a starless row moved the aggregate: %d/%v -> %d/%v", beforeCount, *beforeAvg, afterCount, *afterAvg)
	}
}

func TestTraction3StarsSentBySupporterAreDropped(t *testing.T) {
	setupTraction3DB(t)
	task, _, supporter := seedCompletedTask(t, "completed")

	// A client claiming stars on the supporter path must not be able to rate
	// anyone — role and ratee come from the task, never from the body.
	code, body := postReview(t, task, supporter, `{"stars":5,"ease_rating":"easy","would_use_again":"yes"}`)
	if code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%v)", code, body)
	}
	if body["stars"] != nil || body["supporter_id"] != nil {
		t.Errorf("supporter row rated someone: stars=%v ratee=%v", body["stars"], body["supporter_id"])
	}
	if count, _ := profileAggregate(t, supporter); count != 0 {
		t.Errorf("self-rating leaked onto the profile: count=%d", count)
	}
}

func TestTraction3DoubleSubmitRejected(t *testing.T) {
	setupTraction3DB(t)
	task, requester, _ := seedCompletedTask(t, "completed")

	if code, _ := postReview(t, task, requester, `{"stars":3,"ease_rating":"easy","would_use_again":"yes"}`); code != http.StatusCreated {
		t.Fatalf("first submit should succeed, got %d", code)
	}
	code, body := postReview(t, task, requester, `{"stars":1,"ease_rating":"difficult","would_use_again":"no"}`)
	if code != http.StatusConflict {
		t.Fatalf("second submit: want 409, got %d (%v)", code, body)
	}
	if n := countReviews(t, task); n != 1 {
		t.Errorf("want 1 row after a rejected double submit, got %d", n)
	}
}

func TestTraction3SkipWritesNothing(t *testing.T) {
	setupTraction3DB(t)
	task, requester, supporter := seedCompletedTask(t, "completed")

	// Skip is the absence of a request. What matters is that nothing was
	// written, so a real submission is still possible afterwards — a
	// placeholder row would have burned the unique(task_id, reviewer_id) slot.
	if n := countReviews(t, task); n != 0 {
		t.Fatalf("skip left %d rows behind", n)
	}
	if count, _ := profileAggregate(t, supporter); count != 0 {
		t.Fatalf("skip moved the profile aggregate")
	}
	if code, _ := postReview(t, task, requester, `{"stars":5,"ease_rating":"easy","would_use_again":"yes"}`); code != http.StatusCreated {
		t.Fatalf("submitting after a skip: want 201, got %d", code)
	}
}

func TestTraction3AuthorizationAndState(t *testing.T) {
	setupTraction3DB(t)

	t.Run("non-participant is refused", func(t *testing.T) {
		task, _, _ := seedCompletedTask(t, "completed")
		var stranger string
		if err := db.QueryRow(context.Background(),
			`INSERT INTO public.users DEFAULT VALUES RETURNING id::text`).Scan(&stranger); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if code, _ := postReview(t, task, stranger, `{"stars":5,"ease_rating":"easy","would_use_again":"yes"}`); code != http.StatusForbidden {
			t.Errorf("want 403, got %d", code)
		}
	})

	t.Run("open task is refused", func(t *testing.T) {
		task, requester, _ := seedCompletedTask(t, "open")
		if code, _ := postReview(t, task, requester, `{"stars":5,"ease_rating":"easy","would_use_again":"yes"}`); code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", code)
		}
	})

	t.Run("bad slug is refused", func(t *testing.T) {
		task, requester, _ := seedCompletedTask(t, "completed")
		if code, _ := postReview(t, task, requester, `{"stars":5,"ease_rating":"super_easy","would_use_again":"yes"}`); code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", code)
		}
	})

	t.Run("requester must supply stars", func(t *testing.T) {
		task, requester, _ := seedCompletedTask(t, "completed")
		if code, _ := postReview(t, task, requester, `{"ease_rating":"easy","would_use_again":"yes"}`); code != http.StatusBadRequest {
			t.Errorf("want 400, got %d", code)
		}
	})
}

// The classic sheet (web's ReviewPage, and mobile outside the window) keeps
// working — and an omitted value_rating reaches the column as NULL rather than
// '' , which the CHECK constraint would have rejected.
func TestTraction3ClassicReviewStillWorks(t *testing.T) {
	setupTraction3DB(t)
	task, requester, supporter := seedCompletedTask(t, "completed")

	code, body := postReview(t, task, requester, `{"stars":4,"comment":"On time and friendly."}`)
	if code != http.StatusCreated {
		t.Fatalf("classic review: want 201, got %d (%v)", code, body)
	}
	if body["rater_role"] != "requester" {
		t.Errorf("rater_role backfill for a classic review: got %v", body["rater_role"])
	}
	count, avg := profileAggregate(t, supporter)
	if count != 1 || avg == nil || *avg != 4 {
		t.Errorf("classic review missing from the profile: count=%d avg=%v", count, avg)
	}
}

// The deployed-backend safety net. This is the INSERT the CURRENTLY DEPLOYED
// createReview issues, copied verbatim from before this change: it names no
// rater_role. It has to keep succeeding against the migrated schema, because
// the migration lands before the new binary does — that is the entire reason
// rater_role carries a default.
func TestTraction3OldBackendInsertStillWorksAfterMigration(t *testing.T) {
	setupTraction3DB(t)
	task, requester, supporter := seedCompletedTask(t, "completed")

	var id, raterRole string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO public.reviews (task_id, reviewer_id, supporter_id, stars, value_rating, would_rehire, comment)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7)
		RETURNING id::text, rater_role
	`, task, requester, supporter, 5, "great", true, "Old-binary review").Scan(&id, &raterRole); err != nil {
		t.Fatalf("the deployed backend can no longer write a review: %v", err)
	}
	if raterRole != "requester" {
		t.Errorf("default rater_role: want requester, got %q", raterRole)
	}
	count, avg := profileAggregate(t, supporter)
	if count != 1 || avg == nil || *avg != 5 {
		t.Errorf("old-binary review missing from the profile: count=%d avg=%v", count, avg)
	}
}
