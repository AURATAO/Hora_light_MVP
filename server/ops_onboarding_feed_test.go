package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func callOnboardingFeed(t *testing.T, auth string) (int, map[string]any, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/ops/automation/onboarding", nil)
	if auth != "" {
		c.Request.Header.Set("Authorization", auth)
	}
	getOnboardingFeed(c)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestOnboardingFeedIsOffWithoutAToken(t *testing.T) {
	t.Setenv("OPS_AUTOMATION_TOKEN", "")
	if code, _, _ := callOnboardingFeed(t, "Bearer anything"); code != http.StatusNotFound {
		t.Fatalf("unset token: want 404, got %d", code)
	}
}

func TestOnboardingFeedRefusesAWrongToken(t *testing.T) {
	t.Setenv("OPS_AUTOMATION_TOKEN", "s3cret-token")
	for _, auth := range []string{"", "Bearer ", "Bearer wrong", "s3cret-token-plus", "Basic s3cret-token"} {
		if code, _, _ := callOnboardingFeed(t, auth); code != http.StatusUnauthorized {
			t.Errorf("auth %q: want 401, got %d", auth, code)
		}
	}
}

func TestOnboardingFeedReportsEachApplicantsStage(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("OPS_AUTOMATION_TOKEN", "s3cret-token")
	ctx := context.Background()
	// The payments fixture predates the applicant columns; add what the feed
	// reads, as the production migrations define them.
	if _, err := db.Exec(ctx, `
		alter table public.profiles
		  add column if not exists supporter_applied_at timestamptz,
		  add column if not exists supporter_rejected_at timestamptz;
		alter table public.users
		  add column if not exists stripe_payouts_enabled boolean default false`); err != nil {
		t.Fatalf("fixture columns: %v", err)
	}

	seed := func(email string, applied, approved, rejected, payouts bool) string {
		uid := seedUser(t, email, "Someone", approved)
		if _, err := db.Exec(ctx, `
			update public.profiles
			   set supporter_applied_at = case when $2 then now() - interval '3 days' end,
			       supporter_rejected_at = case when $3 then now() end
			 where id = $1::uuid`, uid, applied, rejected); err != nil {
			t.Fatalf("seed profile %s: %v", email, err)
		}
		if _, err := db.Exec(ctx, `update public.users set stripe_payouts_enabled = $2 where id = $1::uuid`, uid, payouts); err != nil {
			t.Fatalf("seed payouts %s: %v", email, err)
		}
		return uid
	}
	seed("applied@x.test", true, false, false, false)
	seed("rejected@x.test", true, false, true, false)
	seed("approved@x.test", true, true, false, false)
	seed("ready@x.test", true, true, false, true)
	worker := seed("Active@X.test", false, true, false, true) // approved without an application; email in mixed case
	seed("customer@x.test", false, false, false, false)       // a customer: must not appear

	customer := seedUser(t, "requester@x.test", "Requester", false)
	if _, err := db.Exec(ctx, `
		insert into public.tasks (title, description, category, location_text, estimated_minutes,
		                          requester, requester_id, assigned_to, assigned_to_id, status, completed_at)
		values ('Coffee', 'd', 'task', 'NYC', 30, 'requester@x.test', $1::uuid, 'Active@X.test', $2::uuid, 'completed', now())`,
		customer, worker); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	code, out, raw := callOnboardingFeed(t, "Bearer s3cret-token")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, raw)
	}
	got := map[string]string{}
	for _, a := range out["applicants"].([]any) {
		row := a.(map[string]any)
		got[row["email"].(string)] = row["app_status"].(string)
	}
	want := map[string]string{
		"applied@x.test":  "applied",
		"rejected@x.test": "rejected",
		"approved@x.test": "approved",
		"ready@x.test":    "payouts_ready",
		"active@x.test":   "active", // lower-cased: the roster join key
	}
	for email, status := range want {
		if got[email] != status {
			t.Errorf("%s: app_status = %q, want %q", email, got[email], status)
		}
	}
	for _, outsider := range []string{"customer@x.test", "requester@x.test"} {
		if _, ok := got[outsider]; ok {
			t.Errorf("%s is not an applicant and must not be in the feed", outsider)
		}
	}
	// Nothing beyond the agreed fields leaves the API.
	for _, leak := range []string{`"name"`, `"phone"`, `"id"`, "Someone", "Rome"} {
		if strings.Contains(raw, leak) {
			t.Errorf("response contains %s", leak)
		}
	}
}
