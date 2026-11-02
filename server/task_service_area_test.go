package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"hora-auth/helpers"
	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

// fakeArea: the real checkStops over a table of addresses. Anything not listed
// is inside.
func fakeArea(t *testing.T, verdicts map[string]helpers.AreaVerdict, failing map[string]error) *[]string {
	t.Helper()
	var asked []string
	prevFn, prevCheck := serviceAreaFn, checkTaskArea
	serviceAreaFn = func(_ context.Context, stop string) (helpers.AreaVerdict, error) {
		asked = append(asked, stop)
		if err := failing[stop]; err != nil {
			return helpers.AreaUnclear, err
		}
		if v, ok := verdicts[stop]; ok {
			return v, nil
		}
		return helpers.AreaInside, nil
	}
	checkTaskArea = checkStops
	t.Cleanup(func() { serviceAreaFn, checkTaskArea = prevFn, prevCheck })
	return &asked
}

func TestTaskStops(t *testing.T) {
	for in, want := range map[string][]string{
		"12 Bedford Ave":            {"12 Bedford Ave"},
		"12 Bedford Ave | 1 Bay St": {"12 Bedford Ave", "1 Bay St"},
		"A → B":                     {"A", "B"},
		"A -> B | C":                {"A", "B", "C"},
		" | A |  | ":                {"A"},
		"":                          nil,
	} {
		if got := taskStops(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestCheckStopsEveryStopCounts(t *testing.T) {
	const jc, vague = "30 Hudson St, Jersey City", "the market"
	fakeArea(t, map[string]helpers.AreaVerdict{jc: helpers.AreaOutside, vague: helpers.AreaUnclear}, nil)
	ctx := context.Background()

	for _, text := range []string{"12 Bedford Ave", "12 Bedford Ave | 1 Bay St | 350 5th Ave"} {
		if got := checkStops(ctx, text); got.Verdict != helpers.AreaInside || got.Err != nil {
			t.Errorf("%q: %+v", text, got)
		}
	}
	for _, text := range []string{jc, jc + " | 12 Bedford Ave", "12 Bedford Ave | " + jc, "12 Bedford Ave | 1 Bay St | " + jc, "12 Bedford Ave → " + jc} {
		if got := checkStops(ctx, text); got.Verdict != helpers.AreaOutside || got.Stop != jc {
			t.Errorf("%q: %+v", text, got)
		}
	}
	if got := checkStops(ctx, "12 Bedford Ave | "+vague); got.Verdict != helpers.AreaUnclear || got.Stop != vague {
		t.Errorf("unclear stop: %+v", got)
	}
	// Outside is final, whatever came before it.
	if got := checkStops(ctx, vague+" | "+jc); got.Verdict != helpers.AreaOutside {
		t.Errorf("unclear then outside: %+v", got)
	}
	for _, text := range []string{"", "  ", " | "} {
		if got := checkStops(ctx, text); got.Verdict != helpers.AreaUnclear || got.Stop != "" {
			t.Errorf("no stops %q: %+v", text, got)
		}
	}
}

func TestCheckStopsBoundsTheLookups(t *testing.T) {
	asked := fakeArea(t, nil, nil)
	got := checkStops(context.Background(), strings.Repeat("a | ", maxTaskStops+1))
	if got.Verdict != helpers.AreaUnclear || len(*asked) != 0 {
		t.Fatalf("%+v, %d lookups", got, len(*asked))
	}
}

func TestCheckStopsAFailedCheckBooksNothing(t *testing.T) {
	denied := errors.New("geocode: status REQUEST_DENIED")
	fakeArea(t, map[string]helpers.AreaVerdict{"Hoboken": helpers.AreaOutside}, map[string]error{"down": denied})
	if got := checkStops(context.Background(), "12 Bedford Ave | down"); got.Err == nil || got.Verdict != helpers.AreaUnclear {
		t.Fatalf("%+v", got)
	}
	if got := checkStops(context.Background(), "down | Hoboken"); got.Verdict != helpers.AreaOutside {
		t.Fatalf("outside is known even so: %+v", got)
	}
}

// The chat's door: both of a draft's stops, by the same check.
func TestChatBookerCheckArea(t *testing.T) {
	fakeArea(t, map[string]helpers.AreaVerdict{"Yonkers": helpers.AreaOutside, "the market": helpers.AreaUnclear},
		map[string]error{"down": errors.New("geocode: status OVER_QUERY_LIMIT")})
	b := &chatBooker{}
	for _, c := range []struct {
		l1, l2 string
		want   channel.AreaOutcome
	}{
		{"12 Bedford Ave", "", channel.AreaInside},
		{"12 Bedford Ave", "1 Bay St", channel.AreaInside},
		{"12 Bedford Ave", "Yonkers", channel.AreaOutside},
		{"Yonkers", "12 Bedford Ave", channel.AreaOutside},
		{"12 Bedford Ave", "the market", channel.AreaUnclear},
		{"12 Bedford Ave", "down", channel.AreaUnavailable},
	} {
		if got := b.CheckArea(context.Background(), llm.TaskDraft{Location1: c.l1, Location2: c.l2}); got.Outcome != c.want {
			t.Errorf("%q → %q: %+v", c.l1, c.l2, got)
		}
	}
}

func TestPostResultFromAreaRefusals(t *testing.T) {
	outside, _ := json.Marshal(gin.H{"error": areaMsgOutside, "code": areaCodeOutside})
	if got := postResultFrom(422, outside); got.Outcome != channel.PostOutOfArea {
		t.Errorf("%+v", got)
	}
	unclear, _ := json.Marshal(gin.H{"error": "x", "code": areaCodeUnclear, "stop": "the market"})
	if got := postResultFrom(422, unclear); got.Outcome != channel.PostAddressUnclear || got.Message != "the market" {
		t.Errorf("%+v", got)
	}
	// Google down at the post is the retryable failure: YES tries again.
	down, _ := json.Marshal(gin.H{"error": areaMsgUnavailable, "code": areaCodeUnavailable})
	if got := postResultFrom(503, down); got.Outcome != channel.PostFailed {
		t.Errorf("%+v", got)
	}
}

// ── createTask / updateTask against a real Postgres ─────────────────────────

func taskCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), `select count(*) from public.tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateTaskRefusesOutsideTheArea(t *testing.T) {
	setupCreatedViaDB(t)
	const jc = "30 Hudson St, Jersey City"
	fakeArea(t, map[string]helpers.AreaVerdict{jc: helpers.AreaOutside, "the market": helpers.AreaUnclear},
		map[string]error{"down": errors.New("geocode: status REQUEST_DENIED")})
	post := func(loc string) (int, map[string]any) {
		b, _ := json.Marshal(gin.H{"title": "Parcel", "category": "delivery", "is_immediate": true, "location_text": loc})
		return postTaskJSON(t, string(b))
	}

	code, body := post("12 Bedford Ave | 1 Bay St")
	if code != 201 {
		t.Fatalf("all inside: %d %v", code, body)
	}
	before := taskCount(t)

	// Enforced with no Stripe key, every post that reaches the payments gate
	// answers 503 payments_unavailable: these show the check comes first.
	t.Setenv("PAYMENTS_ENFORCED", "true")
	for _, c := range []struct {
		loc  string
		want int
		code string
	}{
		{jc, 422, areaCodeOutside},
		{"12 Bedford Ave | " + jc, 422, areaCodeOutside},
		{"the market", 422, areaCodeUnclear},
		{"", 422, areaCodeUnclear},
		{"down", 503, areaCodeUnavailable},
	} {
		code, body := post(c.loc)
		if code != c.want || body["code"] != c.code {
			t.Errorf("%q: %d %v", c.loc, code, body)
		}
		// Shipped mobile builds print `error`, web prints `message`.
		msg, _ := body["error"].(string)
		if !strings.Contains(msg, " ") || body["message"] != msg {
			t.Errorf("%q: not a sentence for both clients: %v", c.loc, body)
		}
	}
	if n := taskCount(t); n != before {
		t.Fatalf("a refused post wrote %d task(s)", n-before)
	}
}

// The review account is told it can post from anywhere (mobile/APP_REVIEW_NOTES.md).
func TestCreateTaskSandboxIsExempt(t *testing.T) {
	setupCreatedViaDB(t)
	const elsewhere = "1 Infinite Loop, Cupertino"
	fakeArea(t, map[string]helpers.AreaVerdict{elsewhere: helpers.AreaOutside}, nil)
	var uid string
	if err := db.QueryRow(context.Background(),
		`insert into public.users (email, is_sandbox) values ($1, true) returning id::text`, createdViaEmail).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks",
		strings.NewReader(`{"title":"Parcel","category":"delivery","is_immediate":true,"location_text":"`+elsewhere+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("uid", uid)
	c.Set("email", createdViaEmail)
	createTask(c)
	// Past the area check. A sandbox post is always payment-enforced, and with
	// no test Stripe key here it stops at the next gate.
	if w.Code != 503 || !strings.Contains(w.Body.String(), "payments_unavailable") {
		t.Fatalf("sandbox: %d %s", w.Code, w.Body.String())
	}
}

func TestUpdateTaskCannotLeaveTheArea(t *testing.T) {
	setupCreatedViaDB(t)
	const jc = "30 Hudson St, Jersey City"
	asked := fakeArea(t, map[string]helpers.AreaVerdict{jc: helpers.AreaOutside}, nil)
	code, body := postTaskJSON(t, `{"title":"Parcel","category":"delivery","is_immediate":true,"location_text":"12 Bedford Ave"}`)
	if code != 201 {
		t.Fatalf("seed: %d %v", code, body)
	}
	id, _ := body["id"].(string)
	edit := func(loc string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(gin.H{"title": "Parcel, edited", "category": "delivery", "is_immediate": true, "location_text": loc})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPatch, "/tasks/"+id, strings.NewReader(string(b)))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Set("email", createdViaEmail)
		updateTask(c)
		return w
	}
	if w := edit("12 Bedford Ave | " + jc); w.Code != 422 {
		t.Fatalf("edit out: %d %s", w.Code, w.Body.String())
	}
	var stored string
	if err := db.QueryRow(context.Background(), `select location_text from public.tasks where id=$1::uuid`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "12 Bedford Ave" {
		t.Fatalf("stored %q", stored)
	}
	// An older task outside the area can still have its title fixed.
	if _, err := db.Exec(context.Background(), `update public.tasks set location_text=$1 where id=$2::uuid`, jc, id); err != nil {
		t.Fatal(err)
	}
	*asked = nil
	if w := edit(jc); w.Code != 200 || len(*asked) != 0 {
		t.Fatalf("unchanged address: %d, asked %q, %s", w.Code, *asked, w.Body.String())
	}
}
