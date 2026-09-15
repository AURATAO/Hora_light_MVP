package main

// Stripe Phase 2a: the hold, wired into the task lifecycle.
//
// Three layers, and they are tested separately on purpose:
//
//   1. Arithmetic (no DB, no network). The auto-extend coverage proof from
//      billing.go, and the decline-code translation. Both are claims made in
//      comments elsewhere in the codebase; these are what make them checkable.
//
//   2. The flag OFF (DB, no network). The regression guard that matters most
//      right now: PAYMENTS_ENFORCED is off in production, and posting must
//      behave exactly as it did before any of this existed.
//
//   3. The lifecycle (DB, no network). Cancel and admin-remove release; a
//      reassign does not. These run without Stripe configured, which is what
//      lets them assert the SHAPE of the wiring — that release is attempted on
//      exactly these paths and on no others — without a network round trip.
//
// The fourth layer, a real card against the real Stripe test API, is
// payments_smoke_test.go: it needs STRIPE_SMOKE=1 and skips otherwise, so
// `go test ./...` stays hermetic.
//
//	docker run -d --rm --name hora-p2a-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run 'Phase2a|PreAuth|Decline|PaymentsEnforced' -v

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
)

// ── 1. Arithmetic ──────────────────────────────────────────────────────────

// THE hold invariant, and the whole point of the restructure: what is reserved
// is exactly what the requester was shown.
//
//	hold == base fee + estimated time cost + shopping budget
//
// No multiplier, no buffer, no headroom. What this replaced held 1.5x the time
// estimate plus the budget plus $5, so somebody quoted $19.50 watched $34.25
// leave their available balance with nothing anywhere explaining the gap.
func TestPreAuthHoldIsExactlyTheEstimatePlusBudget(t *testing.T) {
	for _, category := range []string{"delivery", "companion"} {
		for _, minutes := range []int{0, 1, 5, 15, 16, 30, 45, 60, 90, 120, 240, 480} {
			// Deliberately includes budgets far above the old $30 cap: there
			// is no cap any more, and a $2,500 budget must hold $2,500.
			for _, budget := range []int{0, 500, 3000, 50000, 250000} {
				for _, rate := range []int{Billing.PerMinuteRateCents, Billing.SurgeRateCentsPerMin} {
					want := baseFeeCents(category) + timeCostCents(minutes, rate) + budget
					got := preAuthAmountCents(category, minutes, budget, rate)
					if got != want {
						t.Errorf("%s %dmin budget=%s rate=%d: hold %s, want %s",
							category, minutes, formatCentsUSD(budget), rate,
							formatCentsUSD(got), formatCentsUSD(want))
					}
				}
			}
		}
	}

	// And the quote a requester is SHOWN is the hold, to the cent. Two
	// different functions that must not be able to disagree — that
	// disagreement is precisely the defect being removed.
	for _, minutes := range []int{15, 30, 90} {
		for _, budget := range []int{0, 2000, 120000} {
			quoted := quoteTask("delivery", minutes, budget, Billing.PerMinuteRateCents).TotalCents
			held := preAuthAmountCents("delivery", minutes, budget, Billing.PerMinuteRateCents)
			if quoted != held {
				t.Errorf("%dmin budget=%s: quoted %s but held %s",
					minutes, formatCentsUSD(budget), formatCentsUSD(quoted), formatCentsUSD(held))
			}
		}
	}
}

// Auto-extend consent must not change the hold by so much as a cent.
//
// It governs whether a supporter may keep working past the estimate without
// asking; it has nothing to do with what is reserved, and two requesters who
// asked for the same task must see the same number. The old hold quietly
// charged a larger authorization for that convenience.
//
// preAuthAmountCents takes no consent argument at all now, which is the
// structural guarantee. This is the behavioural one: the hold must be strictly
// SMALLER than a hold sized to cover the auto-extend window, i.e. that headroom
// is genuinely gone rather than folded in somewhere else.
func TestHoldIsIdenticalWithAndWithoutAutoExtendConsent(t *testing.T) {
	for _, minutes := range []int{16, 30, 60, 240} {
		for _, budget := range []int{0, 2000} {
			held := preAuthAmountCents("delivery", minutes, budget, Billing.PerMinuteRateCents)
			padded := baseFeeCents("delivery") +
				timeCostCents(minutes+Billing.AutoExtendMinutes, Billing.PerMinuteRateCents) + budget
			if held >= padded {
				t.Errorf("%dmin budget=%s: hold %s still carries auto-extend headroom (padded would be %s)",
					minutes, formatCentsUSD(budget), formatCentsUSD(held), formatCentsUSD(padded))
			}
		}
	}
}

// A requester reads these. Two things are being checked: that a known decline
// code produces instructions rather than a restatement, and that the fraud
// codes produce the SAME generic wording as a plain decline — telling somebody
// holding a card that their issuer reported it stolen is a tip-off, and the
// networks' own guidance is to say nothing beyond "declined".
func TestDeclineMessagesAreActionableAndDiscreet(t *testing.T) {
	generic := declineMessage(&stripe.Error{Code: stripe.ErrorCodeCardDeclined})

	for _, code := range []string{"lost_card", "stolen_card", "fraudulent", "pickup_card"} {
		got := declineMessage(&stripe.Error{Code: stripe.ErrorCodeCardDeclined, DeclineCode: stripe.DeclineCode(code)})
		if got != generic {
			t.Errorf("%s leaks the issuer's reason: %q", code, got)
		}
	}

	specific := map[string]string{
		"insufficient_funds": "funds",
		"expired_card":       "expired",
	}
	for code, want := range specific {
		got := declineMessage(&stripe.Error{Code: stripe.ErrorCodeCardDeclined, DeclineCode: stripe.DeclineCode(code)})
		if !strings.Contains(strings.ToLower(got), want) {
			t.Errorf("%s: message %q says nothing about %q", code, got, want)
		}
		if got == generic {
			t.Errorf("%s falls through to the generic message", code)
		}
	}

	// Anything at all, including a bare network failure, has to produce
	// something showable — an empty string would render as a blank error row.
	for name, err := range map[string]error{
		"not a stripe error": context.DeadlineExceeded,
		"empty stripe error": &stripe.Error{},
	} {
		if pe := classifyPreAuthError(err); strings.TrimSpace(pe.Message) == "" {
			t.Errorf("%s produced an empty message", name)
		}
	}
}

// The one distinction the post path acts on. A 3DS challenge arrives as an
// ERROR carrying a good PaymentIntent; read as a decline it would discard a
// recoverable attempt and tell the requester their card was refused.
func TestClassifyPreAuthErrorSeparates3DSFromDecline(t *testing.T) {
	threeDS := classifyPreAuthError(&stripe.Error{
		Code: stripe.ErrorCodeAuthenticationRequired,
		PaymentIntent: &stripe.PaymentIntent{
			ID:           "pi_test_3ds",
			ClientSecret: "pi_test_3ds_secret_abc",
			Status:       stripe.PaymentIntentStatusRequiresAction,
		},
	})
	if !threeDS.RequiresAction {
		t.Fatal("authentication_required was not classified as requiring action")
	}
	if threeDS.PaymentIntentID != "pi_test_3ds" || threeDS.ClientSecret == "" {
		t.Fatalf("3DS error lost the intent the client needs: %+v", threeDS)
	}

	declined := classifyPreAuthError(&stripe.Error{
		Code:        stripe.ErrorCodeCardDeclined,
		DeclineCode: "insufficient_funds",
		PaymentIntent: &stripe.PaymentIntent{
			ID:     "pi_test_declined",
			Status: stripe.PaymentIntentStatusRequiresPaymentMethod,
		},
	})
	if declined.RequiresAction {
		t.Fatal("a plain decline was classified as requiring action")
	}
	if declined.DeclineCode != "insufficient_funds" {
		t.Fatalf("decline code lost: %+v", declined)
	}
}

// The flag itself. Default off is the whole safety property of this phase:
// an operator has to opt in, and no spelling of "not set" turns it on.
func TestPaymentsEnforcedDefaultsOff(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "off", "maybe", "  "} {
		t.Setenv("PAYMENTS_ENFORCED", v)
		if paymentsEnforced() {
			t.Errorf("PAYMENTS_ENFORCED=%q enabled enforcement", v)
		}
	}
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " True "} {
		t.Setenv("PAYMENTS_ENFORCED", v)
		if !paymentsEnforced() {
			t.Errorf("PAYMENTS_ENFORCED=%q did not enable enforcement", v)
		}
	}
}

// ── 2. The flag OFF — the regression guard ─────────────────────────────────

// What production does today. Posting with PAYMENTS_ENFORCED off must create
// the task, write no payments row, and leave payment_id null — the behaviour
// every beta user has, unchanged by a phase that exists to add payment.
func TestPostWithPaymentsDisabledCreatesTaskAndNoPaymentRow(t *testing.T) {
	setupCreatedViaDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != 201 {
		t.Fatalf("expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatal("no task id returned")
	}

	var status string
	var paymentID *string
	if err := db.QueryRow(context.Background(),
		`select status, payment_id::text from public.tasks where id = $1::uuid`, id,
	).Scan(&status, &paymentID); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != "open" {
		t.Errorf("task status = %q, want \"open\" — an unenforced post must be live immediately", status)
	}
	if paymentID != nil {
		t.Errorf("task carries payment_id %q with payments disabled", *paymentID)
	}
	if n := countPayments(t, id); n != 0 {
		t.Errorf("%d payments row(s) written with payments disabled, want 0", n)
	}
}

// Enforcement on with no Stripe key is a misconfiguration, and the safe
// failure is to refuse the post — not to fall through to the unenforced path,
// which would post tasks nobody is holding money for while the operator
// believes payments are on.
func TestPostWithEnforcementButNoStripeKeyRefuses(t *testing.T) {
	setupCreatedViaDB(t)
	if paymentsEnabled() {
		t.Skip("STRIPE_SECRET_KEY is set in this environment — cannot exercise the unconfigured branch")
	}
	t.Setenv("PAYMENTS_ENFORCED", "1")

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (%v)", code, body)
	}
	if body["error"] != "payments_unavailable" {
		t.Errorf("error = %v, want payments_unavailable", body["error"])
	}
	// And nothing was written on the way to that answer.
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.tasks where title = 'Coffee run'`).Scan(&n); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if n != 0 {
		t.Errorf("%d task row(s) left behind by a refused post", n)
	}
}

// The consent checkbox. Absent means unchanged (the column's own default,
// true) rather than refused — web and every shipped mobile build send nothing,
// and reading their silence as a denial would mark them all as having declined
// something they were never asked.
func TestAutoExtendConsentDefaultsTrueAndRoundTrips(t *testing.T) {
	setupCreatedViaDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	cases := []struct {
		name string
		body string
		want bool
	}{
		{"omitted", `{"title":"A","category":"delivery","estimated_minutes":30,"is_immediate":true}`, true},
		{"true", `{"title":"B","category":"delivery","estimated_minutes":30,"is_immediate":true,"auto_extend_consent":true}`, true},
		{"false", `{"title":"C","category":"delivery","estimated_minutes":30,"is_immediate":true,"auto_extend_consent":false}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postTaskJSON(t, tc.body)
			if code != 201 {
				t.Fatalf("expected 201, got %d (%v)", code, body)
			}
			id, _ := body["id"].(string)
			var got bool
			if err := db.QueryRow(context.Background(),
				`select auto_extend_consent from public.tasks where id = $1::uuid`, id,
			).Scan(&got); err != nil {
				t.Fatalf("read consent: %v", err)
			}
			if got != tc.want {
				t.Errorf("auto_extend_consent = %v, want %v", got, tc.want)
			}
		})
	}
}

// A pending_payment task is not a posted task, and the requester's own list is
// the one place it could plausibly leak: listMyTasks has no status filter of
// its own and returns every other status.
func TestPendingPaymentTaskIsInvisibleToItsRequester(t *testing.T) {
	setupCreatedViaDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	requesterID, _ := body["requester_id"].(string)

	if _, err := db.Exec(context.Background(),
		`update public.tasks set status = 'pending_payment' where id = $1::uuid`, id,
	); err != nil {
		t.Fatalf("park task: %v — the phase 2a migration's status CHECK may be missing", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/tasks", nil)
	c.Set("uid", requesterID)
	c.Set("email", createdViaEmail)
	listMyTasks(c)

	if w.Code != 200 {
		t.Fatalf("listMyTasks: %d (%s)", w.Code, w.Body.String())
	}
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, item := range out.Items {
		if item.ID == id {
			t.Fatal("a pending_payment task appeared in its requester's own list")
		}
	}
}

func countPayments(t *testing.T, taskID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.payments where task_id = $1::uuid`, taskID,
	).Scan(&n); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	return n
}

// ── 3. The lifecycle ───────────────────────────────────────────────────────

// seedAuthorizedHold writes a payments row in 'authorized' with an intent id
// that does not exist at Stripe.
//
// That is deliberate and it is what these tests turn on: with Stripe
// unconfigured, releaseTaskHold returns before touching the network, so what
// is being asserted is the WIRING — which handlers attempt a release and which
// do not — rather than Stripe's behaviour, which the smoke test covers against
// the real API.
func seedAuthorizedHold(t *testing.T, taskID, requesterID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, authorized_cents)
		values ($1::uuid, $2::uuid, $3, 'authorized', $4, 7675)
		returning id::text
	`, taskID, requesterID, paymentKindTaskPayment, "pi_seed_"+taskID[:8]).Scan(&id); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	if _, err := db.Exec(context.Background(),
		`update public.tasks set payment_id = $2::uuid where id = $1::uuid`, taskID, id,
	); err != nil {
		t.Fatalf("link hold: %v", err)
	}
	return id
}

func paymentStatusByID(t *testing.T, paymentID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(context.Background(),
		`select status from public.payments where id = $1::uuid`, paymentID,
	).Scan(&s); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	return s
}

// Reassigning a supporter is a supporter-side change. The hold is the
// REQUESTER's money for the same task at the same price, so nothing about it
// may move — not its status, not its amount, not the task's link to it.
//
// This is the test the phase brief asked for by name, and it is worth having
// precisely because "release the hold" is the reflex on every other admin
// action in this file.
func TestPhase2aReassignDoesNotTouchThePayment(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)

	before := paymentStatusByID(t, paymentID)

	newSupporter := seedUser(t, newSupporterEmail, "Nina Supporter", true)
	code, body := callAdminOps(t, adminReassignTask, "reassign", w.taskID, w.adminID, adminEmail,
		`{"supporter_id":"`+newSupporter+`"}`)
	if code != http.StatusOK {
		t.Fatalf("reassign: expected 200, got %d (%v)", code, body)
	}

	if after := paymentStatusByID(t, paymentID); after != before {
		t.Errorf("reassign moved the payment: %q → %q", before, after)
	}
	if after := paymentStatusByID(t, paymentID); after != paymentStatusAuthorized {
		t.Errorf("payment status = %q, want %q", after, paymentStatusAuthorized)
	}

	var linked *string
	if err := db.QueryRow(context.Background(),
		`select payment_id::text from public.tasks where id = $1::uuid`, w.taskID,
	).Scan(&linked); err != nil {
		t.Fatalf("read task linkage: %v", err)
	}
	if linked == nil || *linked != paymentID {
		t.Errorf("reassign broke the task→payment link: %v", linked)
	}
}

// Release is idempotent by intent, and it has to be: cancel paths run more
// than once in practice (a retried request, or an admin cancelling a task the
// requester just cancelled), and the second one must not error.
//
// The offline half of that property is what is checked here: once a hold is
// canceled it is no longer a LIVE payment, so the second release finds nothing
// to do and leaves the row exactly as it was. The Stripe half — that
// paymentintent.Cancel on an already-canceled intent succeeds — needs the real
// API and lives in payments_smoke_test.go.
func TestPhase2aSecondReleaseIsANoOp(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)

	// Put the row where a completed release leaves it, then ask again.
	if err := updatePaymentStatus(context.Background(), paymentID, paymentStatusCanceled, nil); err != nil {
		t.Fatalf("pre-cancel: %v", err)
	}

	// livePaymentForTask only sees requires_auth/authorized rows, so a hold
	// that is already released reads as "no live payment" — the same answer as
	// a task that never had one, which is correct for both and is precisely
	// what makes the second call a no-op rather than an error.
	if _, err := livePaymentForTask(context.Background(), w.taskID); err == nil {
		t.Fatal("a canceled hold is still being returned as the task's live payment")
	}
	if got := releaseTaskHold(context.Background(), w.taskID, w.requesterID, "repeat"); got != nil {
		t.Errorf("second release returned a payment: %+v", got)
	}
	if got := paymentStatusByID(t, paymentID); got != paymentStatusCanceled {
		t.Errorf("status changed under a repeat release: %q", got)
	}
}

// A task that never had a hold — every task posted with PAYMENTS_ENFORCED off,
// which is all of them today — must cancel cleanly. releaseTaskHold returning
// nil rather than an error is what keeps the existing cancel path unchanged.
func TestPhase2aReleaseOnTaskWithNoHoldIsSilent(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")

	if got := releaseTaskHold(context.Background(), w.taskID, w.requesterID, "test"); got != nil {
		t.Errorf("releaseTaskHold invented a payment for a task with none: %+v", got)
	}
}

// The webhook's task-side sync, which Phase 1 could not exercise because
// nothing generated these events against a real task.
//
// A hold that fails while its task is still parked in pending_payment means
// the post never happened: both rows go, so nothing is left for an operator or
// a requester to trip over.
func TestPhase2aLostHoldDiscardsAnUnpostedTask(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)

	if _, err := db.Exec(context.Background(),
		`update public.tasks set status = 'pending_payment' where id = $1::uuid`, w.taskID,
	); err != nil {
		t.Fatalf("park task: %v", err)
	}

	p, err := paymentByIntentID(context.Background(), "pi_seed_"+w.taskID[:8])
	if err != nil {
		t.Fatalf("load payment: %v", err)
	}
	syncTaskToLostHold(context.Background(), p, paymentStatusFailed)

	var tasks, payments int
	_ = db.QueryRow(context.Background(),
		`select count(*) from public.tasks where id = $1::uuid`, w.taskID).Scan(&tasks)
	_ = db.QueryRow(context.Background(),
		`select count(*) from public.payments where id = $1::uuid`, paymentID).Scan(&payments)
	if tasks != 0 {
		t.Errorf("unposted task survived a failed hold")
	}
	if payments != 0 {
		t.Errorf("orphan payments row survived a failed hold")
	}
}

// The other half, and the more important one: a task that IS posted stays
// posted. A supporter may already be travelling, and cancelling the task out
// from under them because a bank reversed an authorization is a worse outcome
// than a settlement somebody has to do by hand. The audit row is how that hand
// gets told.
func TestPhase2aLostHoldOnAnOpenTaskLeavesItPostedAndAudits(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedAuthorizedHold(t, w.taskID, w.requesterID)

	p, err := paymentByIntentID(context.Background(), "pi_seed_"+w.taskID[:8])
	if err != nil {
		t.Fatalf("load payment: %v", err)
	}
	syncTaskToLostHold(context.Background(), p, paymentStatusCanceled)

	var status string
	if err := db.QueryRow(context.Background(),
		`select status from public.tasks where id = $1::uuid`, w.taskID).Scan(&status); err != nil {
		t.Fatalf("task disappeared: %v", err)
	}
	if status != "open" {
		t.Errorf("open task was %q after its hold was lost — it must stay posted", status)
	}

	var audits int
	if err := db.QueryRow(context.Background(),
		`select count(*) from public.audit_logs where job_id = $1::uuid and action = 'PAYMENT_HOLD_LOST'`,
		w.taskID).Scan(&audits); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 1 {
		t.Errorf("%d PAYMENT_HOLD_LOST audit rows, want 1 — nobody will find this task otherwise", audits)
	}
}

// The edit guard. A hold is sized from the estimate and budget at post time;
// raising either afterwards would leave a settlement the hold cannot cover,
// and Capture clamps to the hold, so the difference is money the supporter is
// owed and the platform cannot collect.
func TestPhase2aEditCannotOutgrowTheHold(t *testing.T) {
	setupCreatedViaDB(t)
	t.Setenv("PAYMENTS_ENFORCED", "")

	code, body := postTaskJSON(t, `{"title":"Coffee run","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != 201 {
		t.Fatalf("seed: expected 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	requesterID, _ := body["requester_id"].(string)

	// A hold sized for exactly this task as posted.
	held := preAuthAmountCents("delivery", 30, 0, Billing.PerMinuteRateCents)
	if _, err := db.Exec(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, authorized_cents)
		values ($1::uuid, $2::uuid, $3, 'authorized', $4, $5)
	`, id, requesterID, paymentKindTaskPayment, "pi_edit_guard", held); err != nil {
		t.Fatalf("seed hold: %v", err)
	}

	edit := func(minutes int, budgetCents int) (int, map[string]any) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		payload := `{"title":"Coffee run","category":"delivery","estimated_minutes":` +
			itoa(minutes) + `,"prepay_amount_cents":` + itoa(budgetCents) + `,"is_immediate":true}`
		c.Request = httptest.NewRequest(http.MethodPatch, "/tasks/"+id, strings.NewReader(payload))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: id}}
		c.Set("email", createdViaEmail)
		updateTask(c)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// Shrinking is fine: over-holding is harmless, the remainder is released.
	if code, body := edit(20, 0); code != http.StatusOK {
		t.Errorf("shrinking the estimate was refused: %d (%v)", code, body)
	}
	// Growing past the hold is not.
	code, out := edit(180, 2500)
	if code != http.StatusConflict {
		t.Fatalf("an edit that outgrows the hold returned %d, want 409 (%v)", code, out)
	}
	if out["error"] != "exceeds_authorized_hold" {
		t.Errorf("error = %v, want exceeds_authorized_hold", out["error"])
	}
	// The message has to name the number, or the requester cannot act on it.
	if msg, _ := out["message"].(string); !strings.Contains(msg, formatCentsUSD(held)) {
		t.Errorf("message %q does not name the held amount %s", msg, formatCentsUSD(held))
	}
}

// itoa without pulling strconv into the import block for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// discardUnpaidTask must not be able to delete the payments row of a task that
// has been POSTED. Every statement in it re-tests the status for exactly this
// reason: deleting a live task's payments row would leave a real hold on
// somebody's card with nothing in the database naming it, which is the one
// outcome the whole payments design is built around avoiding.
//
// A comment saying "only ever called on an unposted task" is not what should
// be standing between a future caller and that, so this is the test that makes
// it structural.
func TestPhase2aDiscardRefusesAPostedTask(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open") // 'open' — a posted task
	paymentID := seedAuthorizedHold(t, w.taskID, w.requesterID)

	discardUnpaidTask(context.Background(), w.taskID)

	var tasks, payments int
	_ = db.QueryRow(context.Background(),
		`select count(*) from public.tasks where id = $1::uuid`, w.taskID).Scan(&tasks)
	_ = db.QueryRow(context.Background(),
		`select count(*) from public.payments where id = $1::uuid`, paymentID).Scan(&payments)

	if tasks != 1 {
		t.Error("a posted task was deleted by the unpaid-task discard path")
	}
	if payments != 1 {
		t.Error("a posted task's payments row was deleted — its hold is now unnameable")
	}
	if got := paymentStatusByID(t, paymentID); got != paymentStatusAuthorized {
		t.Errorf("payment status = %q, want %q", got, paymentStatusAuthorized)
	}
	var linked *string
	_ = db.QueryRow(context.Background(),
		`select payment_id::text from public.tasks where id = $1::uuid`, w.taskID).Scan(&linked)
	if linked == nil || *linked != paymentID {
		t.Errorf("task→payment link was cut: %v", linked)
	}
}
