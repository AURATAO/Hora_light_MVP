package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// Report / Block (App Store Guideline 1.2) and the App Review sandbox, against
// a real Postgres. Skipped unless TEST_DATABASE_URL is set — see
// admin_reassign_test.go for the throwaway-container recipe.

const safetyMigrationPath = "../supabase/migrations/20260927120000_user_safety_and_review_sandbox.sql"

// applySafetyMigration applies the real migration on top of whichever fixture
// the caller built. Every fixture that reaches accept, the feed, a task read,
// a reassign or a Stripe key needs it: those paths now read users.is_sandbox
// and user_blocks.
func applySafetyMigration(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	// The fixtures drop users/tasks CASCADE but not these, so rows from an
	// earlier run would survive into a fresh fixture.
	if _, err := db.Exec(ctx, `
		DROP TABLE IF EXISTS public.user_blocks CASCADE;
		DROP TABLE IF EXISTS public.user_reports CASCADE;
		DO $$ BEGIN
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='anon') THEN CREATE ROLE anon NOLOGIN; END IF;
		  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='authenticated') THEN CREATE ROLE authenticated NOLOGIN; END IF;
		END $$;
		CREATE TABLE IF NOT EXISTS public.profiles (id uuid, email text PRIMARY KEY);
	`); err != nil {
		t.Fatalf("prepare safety migration: %v", err)
	}
	migration, err := os.ReadFile(safetyMigrationPath)
	if err != nil {
		t.Fatalf("read %s: %v", safetyMigrationPath, err)
	}
	if _, err := db.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply %s: %v", safetyMigrationPath, err)
	}
}

type capturedOpsEmail struct{ subject, html string }

func captureSafetyEmails(t *testing.T) *[]capturedOpsEmail {
	t.Helper()
	var mu sync.Mutex
	got := &[]capturedOpsEmail{}
	prev := safetyEmailOps
	safetyEmailOps = func(subject, html string) {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, capturedOpsEmail{subject, html})
	}
	t.Cleanup(func() { safetyEmailOps = prev })
	return got
}

func callSafety(t *testing.T, h gin.HandlerFunc, path, uid, email, body string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, h, http.MethodPost, path, "", uid, email, body, nil)
	return code, decodeFirstJSON(t, w)
}

func blockBody(userID, taskID string) string {
	return `{"user_id":"` + userID + `","task_id":"` + taskID + `"}`
}

func seedOpenTask(t *testing.T, requesterID, requesterEmail string) string {
	t.Helper()
	return seedReassignTask(t, "open", requesterID, requesterEmail, "", "")
}

func feedContains(t *testing.T, uid, email, taskID string) bool {
	t.Helper()
	code, w := callTaskHandler(t, listAvailableTasks, http.MethodGet, "/tasks/available", "", uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("feed for %s: %d %s", email, code, w.Body.String())
	}
	return strings.Contains(w.Body.String(), taskID)
}

func readTaskAs(t *testing.T, taskID, uid, email string) (int, map[string]any) {
	t.Helper()
	code, w := callTaskHandler(t, getTask, http.MethodGet, "/tasks/"+taskID, taskID, uid, email, "", nil)
	return code, decodeFirstJSON(t, w)
}

// ── Report ─────────────────────────────────────────────────────────────────

func TestReportCreatesReportAuditRowAndOpsNotification(t *testing.T) {
	setupAdminOpsDB(t)
	emails := captureSafetyEmails(t)
	w := seedOpsWorld(t, "completed")

	code, body := callSafety(t, reportUserHandler, "/safety/report", w.requesterID, requesterEmail,
		`{"user_id":"`+w.supporterID+`","task_id":"`+w.taskID+`","reason_code":"harassment","details":"rude in chat"}`)
	if code != http.StatusCreated {
		t.Fatalf("report: %d %v", code, body)
	}

	var reporter, reported, reason, details string
	if err := db.QueryRow(context.Background(), `
		select reporter_id::text, reported_id::text, reason_code, coalesce(details,'')
		  from public.user_reports where task_id = $1::uuid`, w.taskID,
	).Scan(&reporter, &reported, &reason, &details); err != nil {
		t.Fatalf("read report row: %v", err)
	}
	if reporter != w.requesterID || reported != w.supporterID || reason != "harassment" || details != "rude in chat" {
		t.Errorf("report row = %s→%s %s %q", reporter, reported, reason, details)
	}

	rows := auditRows(t, w.taskID, "USER_REPORTED")
	if len(rows) != 1 {
		t.Fatalf("want 1 USER_REPORTED audit row, got %d", len(rows))
	}
	if len(*emails) != 1 || !strings.Contains((*emails)[0].subject, "User report") ||
		!strings.Contains((*emails)[0].html, "Harassment") {
		t.Fatalf("ops notification = %+v", *emails)
	}

	// A double tap is one report, one audit row, one email.
	code, _ = callSafety(t, reportUserHandler, "/safety/report", w.requesterID, requesterEmail,
		`{"user_id":"`+w.supporterID+`","task_id":"`+w.taskID+`","reason_code":"harassment"}`)
	if code != http.StatusCreated {
		t.Fatalf("repeat report: %d", code)
	}
	if n := len(auditRows(t, w.taskID, "USER_REPORTED")); n != 1 {
		t.Errorf("repeat report wrote %d audit rows, want 1", n)
	}
	if len(*emails) != 1 {
		t.Errorf("repeat report sent %d emails, want 1", len(*emails))
	}

	// The supporter can report the requester too — both roles.
	code, body = callSafety(t, reportUserHandler, "/safety/report", w.supporterID, oldSupporterEmail,
		`{"user_id":"`+w.requesterID+`","task_id":"`+w.taskID+`","reason_code":"no_show"}`)
	if code != http.StatusCreated {
		t.Fatalf("supporter report: %d %v", code, body)
	}
}

func TestReportIsRefusedOutsideTheClosedSetAndTheMatch(t *testing.T) {
	setupAdminOpsDB(t)
	captureSafetyEmails(t)
	w := seedOpsWorld(t, "open")
	stranger := seedUser(t, "stranger@example.com", "Stan Stranger", true)

	cases := []struct {
		name, uid, email, body string
		want                   int
	}{
		{"unknown reason", w.requesterID, requesterEmail,
			`{"user_id":"` + w.supporterID + `","task_id":"` + w.taskID + `","reason_code":"made_up"}`, http.StatusBadRequest},
		{"self", w.requesterID, requesterEmail,
			`{"user_id":"` + w.requesterID + `","task_id":"` + w.taskID + `","reason_code":"spam"}`, http.StatusBadRequest},
		{"stranger reporting a party", stranger, "stranger@example.com",
			`{"user_id":"` + w.requesterID + `","task_id":"` + w.taskID + `","reason_code":"spam"}`, http.StatusNotFound},
		{"party reporting a stranger", w.requesterID, requesterEmail,
			`{"user_id":"` + stranger + `","task_id":"` + w.taskID + `","reason_code":"spam"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		code, body := callSafety(t, reportUserHandler, "/safety/report", tc.uid, tc.email, tc.body)
		if code != tc.want {
			t.Errorf("%s: %d %v, want %d", tc.name, code, body, tc.want)
		}
	}
	var n int
	_ = db.QueryRow(context.Background(), `select count(*) from public.user_reports`).Scan(&n)
	if n != 0 {
		t.Errorf("refused reports wrote %d rows", n)
	}
}

// ── Block ──────────────────────────────────────────────────────────────────

// The block is stored one way and enforced both ways: after the REQUESTER
// blocks the supporter, neither can see or take the other's new tasks, and
// the task they shared reports the chat as blocked to BOTH of them.
func TestBlockIsMutualAndPreventsMatching(t *testing.T) {
	setupAdminOpsDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")
	emails := captureSafetyEmails(t)
	w := seedOpsWorld(t, "completed")
	// The requester is an approved supporter too, so the reverse direction
	// (supporter posts, requester browses) can be exercised.
	mustExec(t, `update public.profiles set is_verified_supporter = true where email = $1`, requesterEmail)

	code, body := callSafety(t, blockUserHandler, "/safety/block", w.requesterID, requesterEmail,
		blockBody(w.supporterID, w.taskID))
	if code != http.StatusOK {
		t.Fatalf("block: %d %v", code, body)
	}
	if n := len(auditRows(t, w.taskID, "USER_BLOCKED")); n != 1 {
		t.Errorf("want 1 USER_BLOCKED audit row, got %d", n)
	}
	if len(*emails) != 1 {
		t.Errorf("want 1 ops email for the block, got %d", len(*emails))
	}

	// Blocker's new task: invisible to and un-acceptable by the blocked user.
	byBlocker := seedOpenTask(t, w.requesterID, requesterEmail)
	if feedContains(t, w.supporterID, oldSupporterEmail, byBlocker) {
		t.Errorf("blocked supporter still sees the blocker's task in the feed")
	}
	if code, _ := acceptAs(t, byBlocker, w.supporterID, oldSupporterEmail); code != http.StatusBadRequest {
		t.Errorf("blocked supporter accepting the blocker's task: %d, want 400", code)
	}
	if got := assignedTo(t, byBlocker); got != "" {
		t.Fatalf("blocked supporter was assigned the blocker's task")
	}
	if code, _ := readTaskAs(t, byBlocker, w.supporterID, oldSupporterEmail); code != http.StatusNotFound {
		t.Errorf("blocked supporter reading the blocker's open task: %d, want 404", code)
	}

	// Mutual: the BLOCKED user's new task is just as unreachable for the blocker.
	byBlocked := seedOpenTask(t, w.supporterID, oldSupporterEmail)
	if feedContains(t, w.requesterID, requesterEmail, byBlocked) {
		t.Errorf("blocker still sees the blocked user's task in the feed")
	}
	if code, _ := acceptAs(t, byBlocked, w.requesterID, requesterEmail); code != http.StatusBadRequest {
		t.Errorf("blocker accepting the blocked user's task: %d, want 400", code)
	}

	// Somebody else is unaffected.
	other := seedUser(t, "third@example.com", "Theo Third", true)
	if !feedContains(t, other, "third@example.com", byBlocker) {
		t.Errorf("an unrelated supporter lost the task from their feed")
	}

	// The shared thread is read-only for both.
	for _, who := range []struct{ uid, email string }{{w.requesterID, requesterEmail}, {w.supporterID, oldSupporterEmail}} {
		code, task := readTaskAs(t, w.taskID, who.uid, who.email)
		if code != http.StatusOK || task["chat_blocked"] != true {
			t.Errorf("%s: shared task chat_blocked = %v (%d), want true", who.email, task["chat_blocked"], code)
		}
	}

	// Blocking twice is a no-op, not a second audit row.
	callSafety(t, blockUserHandler, "/safety/block", w.requesterID, requesterEmail, blockBody(w.supporterID, w.taskID))
	if n := len(auditRows(t, w.taskID, "USER_BLOCKED")); n != 1 {
		t.Errorf("repeat block wrote %d audit rows, want 1", n)
	}
}

// Ops cannot put a blocked pair back together by hand either.
func TestBlockedPairCannotBeAssignedByAdmin(t *testing.T) {
	setupAdminOpsDB(t)
	captureSafetyEmails(t)
	w := seedOpsWorld(t, "completed")
	if code, body := callSafety(t, blockUserHandler, "/safety/block", w.supporterID, oldSupporterEmail,
		blockBody(w.requesterID, w.taskID)); code != http.StatusOK {
		t.Fatalf("block: %d %v", code, body)
	}

	task := seedOpenTask(t, w.requesterID, requesterEmail)
	code, body := callReassign(t, task, w.adminID, adminEmail, `{"supporter_id":"`+w.supporterID+`"}`)
	if code != http.StatusBadRequest || body["error"] != "users_blocked" {
		t.Fatalf("admin assigning a blocked pair: %d %v, want 400 users_blocked", code, body)
	}
	if got := assignedTo(t, task); got != "" {
		t.Fatalf("blocked pair was assigned by admin")
	}
}

func TestBlockRequiresAMatch(t *testing.T) {
	setupAdminOpsDB(t)
	captureSafetyEmails(t)
	w := seedOpsWorld(t, "open")
	stranger := seedUser(t, "stranger@example.com", "Stan Stranger", true)
	if code, _ := callSafety(t, blockUserHandler, "/safety/block", stranger, "stranger@example.com",
		blockBody(w.requesterID, w.taskID)); code != http.StatusNotFound {
		t.Errorf("stranger blocking a party: %d, want 404", code)
	}
	if code, _ := callSafety(t, blockUserHandler, "/safety/block", w.requesterID, requesterEmail,
		blockBody(w.requesterID, w.taskID)); code != http.StatusBadRequest {
		t.Errorf("self block: %d, want 400", code)
	}
}

// ── The review sandbox ─────────────────────────────────────────────────────

const sandboxEmail = "appreview@example.com"

func seedSandboxUser(t *testing.T) string {
	t.Helper()
	uid := seedUser(t, sandboxEmail, "Hora Review", true)
	mustExec(t, `update public.users set is_sandbox = true where id = $1::uuid`, uid)
	return uid
}

// One login walks both sides: the sandbox account sees its own open task on
// the Work board, is told it may accept it, and can.
func TestSandboxCanAcceptItsOwnTask(t *testing.T) {
	setupStripeWebhookDB(t) // the Connect columns the payout gate reads
	t.Setenv("PAYMENTS_ENFORCED", "")
	sb := seedSandboxUser(t)
	task := seedOpenTask(t, sb, sandboxEmail)

	if !feedContains(t, sb, sandboxEmail, task) {
		t.Fatalf("sandbox account does not see its own task on the Work board")
	}
	code, body := readTaskAs(t, task, sb, sandboxEmail)
	if code != http.StatusOK || body["can_self_accept"] != true {
		t.Fatalf("can_self_accept = %v (%d)", body["can_self_accept"], code)
	}
	// Payments are enforced for the sandbox whatever the flag says, so the
	// payout gate is live: the account needs a payable connected account.
	mustExec(t, `update public.users set stripe_payouts_enabled = true, stripe_transfers_active = true,
		stripe_account_id = 'acct_sandbox' where id = $1::uuid`, sb)
	if code, body := acceptAs(t, task, sb, sandboxEmail); code != http.StatusOK {
		t.Fatalf("sandbox self-accept: %d %v", code, body)
	}
	if got := assignedTo(t, task); got != sb {
		t.Fatalf("assigned to %q, want the sandbox account", got)
	}
}

// Nobody else gets the exemption.
func TestRealAccountStillCannotAcceptItsOwnTask(t *testing.T) {
	setupAdminOpsDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")
	w := seedOpsWorld(t, "open")
	mustExec(t, `update public.profiles set is_verified_supporter = true where email = $1`, requesterEmail)
	task := seedOpenTask(t, w.requesterID, requesterEmail)
	if feedContains(t, w.requesterID, requesterEmail, task) {
		t.Errorf("a real account sees its own task on the Work board")
	}
	if code, _ := acceptAs(t, task, w.requesterID, requesterEmail); code != http.StatusBadRequest {
		t.Errorf("real self-accept: %d, want 400", code)
	}
	if _, body := readTaskAs(t, task, w.requesterID, requesterEmail); body["can_self_accept"] == true {
		t.Errorf("real account told it can self-accept")
	}
}

// The wall, both ways: a reviewer's task never reaches a real supporter, and a
// real requester's task never reaches the reviewer.
func TestSandboxTasksAndRealTasksNeverMeet(t *testing.T) {
	setupAdminOpsDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")
	w := seedOpsWorld(t, "open")
	sb := seedSandboxUser(t)

	sandboxTask := seedOpenTask(t, sb, sandboxEmail)
	if feedContains(t, w.supporterID, oldSupporterEmail, sandboxTask) {
		t.Errorf("a real supporter sees the review account's task")
	}
	if code, _ := acceptAs(t, sandboxTask, w.supporterID, oldSupporterEmail); code != http.StatusBadRequest {
		t.Errorf("real supporter accepting a sandbox task: %d, want 400", code)
	}
	if code, _ := readTaskAs(t, sandboxTask, w.supporterID, oldSupporterEmail); code != http.StatusNotFound {
		t.Errorf("real supporter reading a sandbox task: %d, want 404", code)
	}

	realTask := seedOpenTask(t, w.requesterID, requesterEmail)
	if feedContains(t, sb, sandboxEmail, realTask) {
		t.Errorf("the review account sees a real requester's task")
	}
	if code, _ := acceptAs(t, realTask, sb, sandboxEmail); code != http.StatusBadRequest {
		t.Errorf("review account accepting a real task: %d, want 400", code)
	}

	code, body := callReassign(t, sandboxTask, w.adminID, adminEmail, `{"supporter_id":"`+w.supporterID+`"}`)
	if code != http.StatusBadRequest || body["error"] != "sandbox_mismatch" {
		t.Errorf("admin assigning a real supporter to a sandbox task: %d %v", code, body)
	}
}

// ── Test keys, always, for the sandbox ─────────────────────────────────────

func TestSandboxStripeKeyIsAlwaysATestKey(t *testing.T) {
	setupAdminOpsDB(t)
	w := seedOpsWorld(t, "open")
	sb := seedSandboxUser(t)
	ctx := context.Background()

	// Today's posture: the platform key is a test key and the sandbox shares it.
	forceStripeKey(t, "sk_test_platform")
	t.Setenv("STRIPE_SANDBOX_SECRET_KEY", "")
	if k, err := stripeKeyForUser(ctx, sb); err != nil || k != "sk_test_platform" {
		t.Errorf("sandbox on a test platform: %q %v", k, err)
	}

	// After October: live platform key. With no sandbox key configured the
	// sandbox REFUSES rather than falling back to live.
	forceStripeKey(t, "sk_live_platform")
	if k, err := stripeKeyForUser(ctx, sb); !errors.Is(err, errSandboxKeyUnavailable) || k != "" {
		t.Errorf("sandbox on a live platform with no test key: %q %v, want refusal", k, err)
	}
	if k, _ := stripeKeyForUser(ctx, w.requesterID); k != "sk_live_platform" {
		t.Errorf("a real user on the live platform got %q", k)
	}

	// With the sandbox key set, the sandbox — and only the sandbox — uses it.
	t.Setenv("STRIPE_SANDBOX_SECRET_KEY", "sk_test_sandbox")
	if k, err := stripeKeyForUser(ctx, sb); err != nil || k != "sk_test_sandbox" {
		t.Errorf("sandbox with its own key: %q %v", k, err)
	}
	task := seedOpenTask(t, sb, sandboxEmail)
	if k, err := stripeKeyForTask(ctx, task); err != nil || k != "sk_test_sandbox" {
		t.Errorf("sandbox task key: %q %v", k, err)
	}
	if k, _ := stripeKeyForTask(ctx, w.taskID); k != "sk_live_platform" {
		t.Errorf("real task key: %q", k)
	}

	// A LIVE key put in the sandbox slot by mistake is refused, not used.
	t.Setenv("STRIPE_SANDBOX_SECRET_KEY", "sk_live_oops")
	if k, err := stripeKeyForUser(ctx, sb); err == nil || k != "" {
		t.Errorf("live key in the sandbox slot was used: %q", k)
	}

	// Payments are enforced for the sandbox regardless of the flag.
	t.Setenv("PAYMENTS_ENFORCED", "")
	if !paymentsEnforcedFor(ctx, sb) {
		t.Errorf("payments not enforced for the sandbox with the flag off")
	}
	if paymentsEnforcedFor(ctx, w.requesterID) {
		t.Errorf("payments enforced for a real user with the flag off")
	}
}

// The hold on a sandbox task is placed with the sandbox key — the one call
// that would otherwise charge a reviewer's card on the live platform.
func TestSandboxHoldUsesTheSandboxKey(t *testing.T) {
	setupStripeWebhookDB(t)
	sb := seedSandboxUser(t)
	forceStripeKey(t, "sk_live_platform")
	t.Setenv("STRIPE_SANDBOX_SECRET_KEY", "sk_test_sandbox")

	var usedKey string
	prev := stripeCreatePaymentIntent
	stripeCreatePaymentIntent = func(key string, params *stripe.PaymentIntentParams) (*stripe.PaymentIntent, error) {
		usedKey = key
		return &stripe.PaymentIntent{ID: "pi_sandbox", Amount: *params.Amount,
			Status: stripe.PaymentIntentStatusRequiresCapture}, nil
	}
	t.Cleanup(func() { stripeCreatePaymentIntent = prev })

	task := seedOpenTask(t, sb, sandboxEmail)
	if _, err := CreatePreAuth(context.Background(), PreAuthInput{
		TaskID: task, RequesterID: sb, Category: "quick_errand", EstimatedMinutes: 30,
		RateCents: 50, StripeCustomerID: "cus_x", StripePaymentMethodID: "pm_x",
	}); err != nil {
		t.Fatalf("pre-auth: %v", err)
	}
	if usedKey != "sk_test_sandbox" {
		t.Fatalf("sandbox hold placed with %q, want the sandbox test key", usedKey)
	}
}
