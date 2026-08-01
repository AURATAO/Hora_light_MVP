package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestLoadReviewAccountDisabledUnlessBothHalvesSet(t *testing.T) {
	cases := []struct {
		name, email, code string
		wantOK            bool
	}{
		{"both set", "review@my-hora.com", "000000", true},
		{"neither set", "", "", false},
		{"email only", "review@my-hora.com", "", false},
		{"code only", "", "000000", false},
		{"whitespace-only code", "review@my-hora.com", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("REVIEW_ACCOUNT_EMAIL", tc.email)
			t.Setenv("REVIEW_ACCOUNT_CODE", tc.code)
			if _, ok := loadReviewAccount(); ok != tc.wantOK {
				t.Fatalf("loadReviewAccount() ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

func TestLoadReviewAccountProfileDefaults(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "review@my-hora.com")
	t.Setenv("REVIEW_ACCOUNT_CODE", "000000")
	t.Setenv("REVIEW_ACCOUNT_NAME", "")
	t.Setenv("REVIEW_ACCOUNT_CITY", "Boston")
	t.Setenv("REVIEW_ACCOUNT_PHONE", "")
	t.Setenv("REVIEW_ACCOUNT_AVATAR_URL", "")

	ra, ok := loadReviewAccount()
	if !ok {
		t.Fatal("expected the review account to be enabled")
	}
	if ra.Name != defaultReviewName {
		t.Errorf("Name = %q, want the default %q", ra.Name, defaultReviewName)
	}
	if ra.Phone != defaultReviewPhone {
		t.Errorf("Phone = %q, want the default %q", ra.Phone, defaultReviewPhone)
	}
	if ra.City != "Boston" {
		t.Errorf("City = %q, want the env override %q", ra.City, "Boston")
	}
	if ra.AvatarURL != "" {
		t.Errorf("AvatarURL = %q, want empty (no default)", ra.AvatarURL)
	}
}

func TestLoadReviewAccountNormalizesEmail(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "  Review@My-Hora.com  ")
	t.Setenv("REVIEW_ACCOUNT_CODE", " 000000 ")

	ra, ok := loadReviewAccount()
	if !ok {
		t.Fatal("expected the review account to be enabled")
	}
	if ra.Email != "review@my-hora.com" {
		t.Errorf("Email = %q, want it trimmed and lower-cased", ra.Email)
	}
	if ra.Code != "000000" {
		t.Errorf("Code = %q, want it trimmed", ra.Code)
	}
}

func TestReviewAccountMatches(t *testing.T) {
	ra := reviewAccount{Email: "review@my-hora.com", Code: "000000"}

	cases := []struct {
		name, email, code string
		want              bool
	}{
		{"exact", "review@my-hora.com", "000000", true},
		{"email case and padding are normalized", " Review@My-Hora.COM ", " 000000 ", true},
		{"right email, wrong code", "review@my-hora.com", "000001", false},
		{"right email, empty code", "review@my-hora.com", "", false},
		{"right code, another user", "someone@else.com", "000000", false},
		// The whole point: no other account can reach the bypass, however close
		// the address looks.
		{"lookalike domain", "review@my-hora.co", "000000", false},
		{"lookalike local part", "reviews@my-hora.com", "000000", false},
		{"prefix of the code", "review@my-hora.com", "00000", false},
		{"code with a suffix", "review@my-hora.com", "0000000", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ra.matches(tc.email, tc.code); got != tc.want {
				t.Fatalf("matches(%q, %q) = %v, want %v", tc.email, tc.code, got, tc.want)
			}
		})
	}
}

// An unconfigured reviewAccount must never match, including the zero-value
// email/code pair — otherwise a missing env var would turn into an open door.
func TestReviewAccountUnconfiguredNeverMatches(t *testing.T) {
	for _, ra := range []reviewAccount{
		{},
		{Email: "review@my-hora.com"},
		{Code: "000000"},
	} {
		if ra.matches("", "") {
			t.Errorf("%+v matched empty credentials", ra)
		}
		if ra.matches("review@my-hora.com", "000000") {
			t.Errorf("%+v matched real-looking credentials while unconfigured", ra)
		}
	}
}

// postReviewLogin registers the route against a bare engine and posts to it.
// sqldb is deliberately nil: nothing below is meant to reach the database, so
// a panic here would mean a request got further than it should have.
func postReviewLogin(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerReviewAccountRoute(r, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/review-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// With the env unset the route must not exist at all — that's the off switch
// we rely on once App Review is done.
func TestReviewLoginRouteAbsentWhenDisabled(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "")
	t.Setenv("REVIEW_ACCOUNT_CODE", "")

	if got := postReviewLogin(t, `{"email":"review@my-hora.com","code":"000000"}`).Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (route should not be registered)", got, http.StatusNotFound)
	}
}

func TestReviewLoginRejectsEveryoneElse(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "review@my-hora.com")
	t.Setenv("REVIEW_ACCOUNT_CODE", "000000")

	cases := []struct {
		name, body string
		want       int
	}{
		{"another account, right code", `{"email":"someone@else.com","code":"000000"}`, http.StatusUnauthorized},
		{"review account, wrong code", `{"email":"review@my-hora.com","code":"123456"}`, http.StatusUnauthorized},
		{"no code", `{"email":"review@my-hora.com"}`, http.StatusUnauthorized},
		{"empty body object", `{}`, http.StatusUnauthorized},
		{"malformed json", `not json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postReviewLogin(t, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if cookie := w.Header().Get("Set-Cookie"); strings.Contains(cookie, "hora_session=") {
				t.Fatalf("a rejected review login set a session cookie: %q", cookie)
			}
		})
	}
}

func TestReviewThrottle(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	th := &reviewThrottle{}

	for i := 0; i < reviewMaxFailures; i++ {
		if !th.allow(start) {
			t.Fatalf("attempt %d was blocked; the budget is %d", i+1, reviewMaxFailures)
		}
		th.recordFailure(start)
	}
	if th.allow(start) {
		t.Fatal("the budget should be spent after reviewMaxFailures misses")
	}

	// Still spent just before the window closes, open again just after.
	if th.allow(start.Add(reviewFailureWindow - time.Second)) {
		t.Error("the window reopened early")
	}
	if !th.allow(start.Add(reviewFailureWindow)) {
		t.Error("the window did not reopen after it elapsed")
	}
}

func TestReviewThrottleResetOnSuccess(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	th := &reviewThrottle{}

	for i := 0; i < reviewMaxFailures; i++ {
		th.recordFailure(now)
	}
	if th.allow(now) {
		t.Fatal("the budget should be spent")
	}

	// A reviewer who mistypes a few times and then gets in must not be locked
	// out by those earlier misses on their next sign-in.
	th.reset()
	if !th.allow(now) {
		t.Error("a successful login did not clear the budget")
	}
}

func TestReviewLoginThrottlesRepeatedWrongCodes(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "review@my-hora.com")
	t.Setenv("REVIEW_ACCOUNT_CODE", "000000")
	gin.SetMode(gin.TestMode)

	// One engine, so the attempts share a throttle.
	r := gin.New()
	registerReviewAccountRoute(r, nil)

	post := func(body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/review-login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}

	wrong := `{"email":"review@my-hora.com","code":"123456"}`
	for i := 0; i < reviewMaxFailures; i++ {
		if got := post(wrong); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, got, http.StatusUnauthorized)
		}
	}
	if got := post(wrong); got != http.StatusTooManyRequests {
		t.Fatalf("status after the budget ran out = %d, want %d", got, http.StatusTooManyRequests)
	}

	// A different address must be unaffected — the mobile client sends every
	// rejected OTP here, and those users still need their own 401.
	if got := post(`{"email":"someone@else.com","code":"111111"}`); got != http.StatusUnauthorized {
		t.Fatalf("another account's status = %d, want %d (throttle must not apply)", got, http.StatusUnauthorized)
	}
}

func TestReviewLoginOtherAccountsNeverSpendTheBudget(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "review@my-hora.com")
	t.Setenv("REVIEW_ACCOUNT_CODE", "000000")
	gin.SetMode(gin.TestMode)

	r := gin.New()
	registerReviewAccountRoute(r, nil)

	post := func(body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth/review-login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Ordinary users mistyping their own OTP, many times over.
	for i := 0; i < reviewMaxFailures*3; i++ {
		if got := post(`{"email":"someone@else.com","code":"111111"}`); got != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
		}
	}
	// The reviewer's own budget is untouched: a wrong code is still 401, not 429.
	if got := post(`{"email":"review@my-hora.com","code":"123456"}`); got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d — other accounts drained the review budget", got, http.StatusUnauthorized)
	}
}

// A correct credential that then fails in the database must report *why*. The
// bare "user upsert failed" this replaced cost a redeploy to diagnose, because
// the real error only ever reached the host's logs.
func TestReviewLoginSurfacesTheUnderlyingDBError(t *testing.T) {
	t.Setenv("REVIEW_ACCOUNT_EMAIL", "review@my-hora.com")
	t.Setenv("REVIEW_ACCOUNT_CODE", "000000")
	gin.SetMode(gin.TestMode)

	// Opens lazily, so the failure lands on the query — the same shape as a
	// database that is up but refusing the statement.
	dead, err := sql.Open("pgx", "postgres://127.0.0.1:1/nope?connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer dead.Close()

	r := gin.New()
	registerReviewAccountRoute(r, dead)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/review-login",
		strings.NewReader(`{"email":"review@my-hora.com","code":"000000"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	if body.Detail == "" {
		t.Fatal("the 500 carried no detail — the DB error is invisible again")
	}
	// The wrapper must name the step, so the next failure says where it broke
	// rather than just that it broke.
	if !strings.Contains(body.Detail, "users row") && !strings.Contains(body.Detail, "seed profile") {
		t.Errorf("detail %q names no step", body.Detail)
	}
}

func TestReviewAccountIsEmail(t *testing.T) {
	ra := reviewAccount{Email: "review@my-hora.com", Code: "000000"}

	if !ra.isEmail("REVIEW@my-hora.com") {
		t.Error("isEmail should ignore case")
	}
	if ra.isEmail("someone@else.com") {
		t.Error("isEmail matched a different address")
	}
	if (reviewAccount{}).isEmail("") {
		t.Error("an unconfigured account should not claim the empty address")
	}
}
