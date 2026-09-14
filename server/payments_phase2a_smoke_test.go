package main

// Phase 2a against the real Stripe test-mode API.
//
// Same contract as payments_smoke_test.go: needs BOTH STRIPE_SMOKE=1 and a
// real test STRIPE_SECRET_KEY, and skips otherwise, so `go test ./...` stays
// hermetic and offline. It refuses outright to run against a live key.
//
// What only this can prove: that a card can be saved and listed and removed
// through our own endpoints, that an off-session hold against a saved card
// actually authorizes for the amount BillingConfig says it should, and that a
// declined card leaves neither a task nor a payments row behind. Every one of
// those is a claim about Stripe's behaviour, not ours, and a fake would be
// asserting our own assumptions back at us.
//
//	docker run -d --rm --name hora-p2a-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55434:5432 postgres:16
//	STRIPE_SMOKE=1 \
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55434/horatest' \
//	  go test ./ -run Phase2aSmoke -v
//
// Stripe's canned test PaymentMethods are used rather than raw card numbers:
// pm_card_visa always succeeds, pm_card_chargeDeclined always declines. Raw
// PANs through the API need PCI clearance the account does not have, and
// hardcoding 4242… here would test a path no client uses anyway — both
// clients hand their card to Stripe's own sheet.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/customer"
	"github.com/stripe/stripe-go/v86/paymentintent"
	"github.com/stripe/stripe-go/v86/paymentmethod"
	"github.com/stripe/stripe-go/v86/setupintent"
)

// Canned test payment methods. Stripe keeps these stable; they are the
// documented way to exercise an outcome without handling a card number.
const (
	testPMVisa = "pm_card_visa"
	// Attaches to a Customer fine and then declines when charged. NOT
	// pm_card_chargeDeclined, which declines at ATTACH time (Stripe runs a
	// zero-amount authorization there) and so never reaches the code under
	// test — the failure it produces is "could not save the card", which is a
	// different path with a different answer.
	testPMDeclined = "pm_card_chargeCustomerFail"
)

func requireStripeSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("STRIPE_SMOKE") != "1" {
		t.Skip("STRIPE_SMOKE != 1 — skipping live Stripe smoke test")
	}
	if os.Getenv("STRIPE_SECRET_KEY") == "" {
		t.Skip("STRIPE_SECRET_KEY not set — skipping live Stripe smoke test")
	}
	if !stripeIsTestMode() {
		t.Fatal("STRIPE_SECRET_KEY is not a test key (sk_test_/rk_test_) — refusing to run")
	}
}

// seedRequesterWithCard creates a user, its Stripe Customer, and attaches one
// canned card. The Customer is deleted on cleanup — test-mode customers are
// free but they accumulate, and a smoke suite that litters someone's dashboard
// stops being run.
func seedRequesterWithCard(t *testing.T, email, pmID string) (uid, customerID string) {
	t.Helper()
	ctx := context.Background()

	if err := db.QueryRow(ctx,
		`insert into public.users (email, name) values ($1, $2) returning id::text`,
		email, "Smoke Requester",
	).Scan(&uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	customerID, err := stripeCustomerFor(ctx, uid, email)
	if err != nil {
		t.Fatalf("stripeCustomerFor: %v", err)
	}
	t.Cleanup(func() {
		if _, err := customer.Del(customerID, nil); err != nil {
			t.Logf("could not delete test customer %s: %v", customerID, err)
		}
	})

	if _, err := paymentmethod.Attach(pmID, &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(customerID),
	}); err != nil {
		t.Fatalf("attach %s: %v", pmID, err)
	}
	return uid, customerID
}

// callWithSession runs a handler with the identity gin's auth middleware would
// have set, and nothing else — the same shape as callAdminOps.
func callWithSession(t *testing.T, h gin.HandlerFunc, method, path, uid, email, body string, params gin.Params) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("uid", uid)
	c.Set("email", email)

	h(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// ── A. Card on file, end to end ────────────────────────────────────────────

// The three card endpoints as a requester meets them: ask for a SetupIntent,
// see the saved card listed, remove it.
//
// The SetupIntent is checked for the two properties the whole off-session
// model rests on — that it is attached to OUR customer, and that its usage is
// off_session. A SetupIntent saved as on_session produces a PaymentMethod the
// issuer expects a challenge for on every use, which turns every post into a
// 3DS prompt.
func TestPhase2aSmokeCardOnFileRoundTrip(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "card.roundtrip@example.test"
	uid, customerID := seedRequesterWithCard(t, email, testPMVisa)

	// ── setup intent ────────────────────────────────────────────────────────
	code, body := callWithSession(t, createSetupIntentHandler,
		http.MethodPost, "/payments/setup-intent", uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("setup-intent: expected 200, got %d (%v)", code, body)
	}
	secret, _ := body["client_secret"].(string)
	if !strings.HasPrefix(secret, "seti_") {
		t.Errorf("client_secret = %q, want a seti_… secret", secret)
	}
	if got, _ := body["customer_id"].(string); got != customerID {
		t.Errorf("customer_id = %q, want %q", got, customerID)
	}
	if key, _ := body["ephemeral_key"].(string); key == "" {
		t.Error("no ephemeral key — PaymentSheet cannot present without one")
	}

	// The SetupIntent itself, read back from Stripe rather than trusted.
	intentID := strings.SplitN(secret, "_secret_", 2)[0]
	si, err := setupintent.Get(intentID, nil)
	if err != nil {
		t.Fatalf("fetch setup intent: %v", err)
	}
	if si.Customer == nil || si.Customer.ID != customerID {
		t.Errorf("setup intent is not attached to our customer: %+v", si.Customer)
	}
	if si.Usage != "off_session" {
		t.Errorf("usage = %q, want off_session — the saved card must work at post time with nobody present", si.Usage)
	}

	// ── list ────────────────────────────────────────────────────────────────
	code, body = callWithSession(t, listPaymentMethodsHandler,
		http.MethodGet, "/payments/payment-methods", uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d (%v)", code, body)
	}
	if has, _ := body["has_card"].(bool); !has {
		t.Error("has_card is false with a card attached")
	}
	cards, _ := body["cards"].([]any)
	if len(cards) != 1 {
		t.Fatalf("expected exactly 1 card, got %d (%v)", len(cards), body["cards"])
	}
	card, _ := cards[0].(map[string]any)
	pmID, _ := card["id"].(string)
	if brand, _ := card["brand"].(string); brand != "visa" {
		t.Errorf("brand = %q, want visa", brand)
	}
	if last4, _ := card["last4"].(string); last4 != "4242" {
		t.Errorf("last4 = %q, want 4242", last4)
	}
	if isDefault, _ := card["is_default"].(bool); !isDefault {
		t.Error("the only card on file is not marked default — a pre-auth would have nothing to charge")
	}

	// ── remove ──────────────────────────────────────────────────────────────
	code, body = callWithSession(t, deletePaymentMethodHandler,
		http.MethodDelete, "/payments/payment-methods/"+pmID, uid, email, "",
		gin.Params{{Key: "id", Value: pmID}})
	if code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d (%v)", code, body)
	}

	code, body = callWithSession(t, listPaymentMethodsHandler,
		http.MethodGet, "/payments/payment-methods", uid, email, "", nil)
	if code != http.StatusOK {
		t.Fatalf("list after delete: %d (%v)", code, body)
	}
	if has, _ := body["has_card"].(bool); has {
		t.Error("card still listed after removal")
	}
}

// A pm_… belonging to somebody else is a 404, not a 403 and certainly not a
// detach. The id is the only thing naming the card, so without this check
// "remove by id" is a way to remove strangers' cards.
func TestPhase2aSmokeCannotRemoveAnotherAccountsCard(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	ownerUID, ownerCustomer := seedRequesterWithCard(t, "owner@example.test", testPMVisa)
	cards, _, err := savedCardsFor(ownerCustomer)
	if err != nil || len(cards) == 0 {
		t.Fatalf("owner has no card: %v", err)
	}
	victimPM := cards[0].ID

	attackerUID, _ := seedRequesterWithCard(t, "attacker@example.test", testPMVisa)
	if attackerUID == ownerUID {
		t.Fatal("fixture error: same user")
	}

	code, body := callWithSession(t, deletePaymentMethodHandler,
		http.MethodDelete, "/payments/payment-methods/"+victimPM,
		attackerUID, "attacker@example.test", "",
		gin.Params{{Key: "id", Value: victimPM}})
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%v)", code, body)
	}

	// And the card is still there.
	after, _, err := savedCardsFor(ownerCustomer)
	if err != nil {
		t.Fatalf("re-list owner cards: %v", err)
	}
	if len(after) != len(cards) {
		t.Errorf("owner lost a card: %d → %d", len(cards), len(after))
	}
}

// ── B. Posting with the flag ON ────────────────────────────────────────────

// The happy path, end to end: a requester with a good card posts, the hold
// lands for exactly the amount BillingConfig computes, and the task is open
// and linked to its payment.
func TestPhase2aSmokePostPlacesHoldForTheBillingAmount(t *testing.T) {
	requireStripeSmoke(t)
	setupCreatedViaDB(t)

	const email = "post.happy@example.test"
	uid, _ := seedRequesterWithCard(t, email, testPMVisa)
	seedProfileFor(t, uid, email)

	t.Setenv("PAYMENTS_ENFORCED", "1")

	// 60 minutes, $20 budget: ($12.00 base + $22.50 time) x 1.5 + $20 + $5.
	const minutes, budgetCents = 60, 2000
	want := preAuthAmountCents("delivery", minutes, budgetCents)

	code, body := postTaskJSONAs(t, email,
		`{"title":"Grocery run","category":"delivery","estimated_minutes":60,"prepay_amount_cents":2000,"is_immediate":true,"auto_extend_consent":true}`)
	if code != 201 {
		t.Fatalf("expected 201, got %d (%v)", code, body)
	}
	taskID, _ := body["id"].(string)

	var status string
	var paymentID *string
	if err := db.QueryRow(context.Background(),
		`select status, payment_id::text from public.tasks where id = $1::uuid`, taskID,
	).Scan(&status, &paymentID); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != "open" {
		t.Errorf("task status = %q, want open", status)
	}
	if paymentID == nil {
		t.Fatal("task carries no payment_id — the linkage the hold is found by")
	}

	var payStatus, intentID string
	var authorized int
	if err := db.QueryRow(context.Background(), `
		select status, coalesce(stripe_payment_intent_id,''), coalesce(authorized_cents,0)
		  from public.payments where id = $1::uuid
	`, *paymentID).Scan(&payStatus, &intentID, &authorized); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if payStatus != paymentStatusAuthorized {
		t.Errorf("payment status = %q, want %q", payStatus, paymentStatusAuthorized)
	}
	if authorized != want {
		t.Errorf("authorized %d cents, want %d (%s)", authorized, want, formatCentsUSD(want))
	}

	// Stripe's own view of it, not ours.
	pi, err := paymentintent.Get(intentID, nil)
	if err != nil {
		t.Fatalf("fetch intent: %v", err)
	}
	t.Cleanup(func() { _, _ = paymentintent.Cancel(intentID, nil) })

	if pi.Status != stripe.PaymentIntentStatusRequiresCapture {
		t.Errorf("Stripe status = %q, want requires_capture — the money must be held, not taken", pi.Status)
	}
	if pi.Amount != int64(want) {
		t.Errorf("Stripe amount = %d, want %d", pi.Amount, want)
	}
	if pi.CaptureMethod != stripe.PaymentIntentCaptureMethodManual {
		t.Errorf("capture_method = %q, want manual", pi.CaptureMethod)
	}
	if pi.Metadata["task_id"] != taskID {
		t.Errorf("metadata task_id = %q, want %q", pi.Metadata["task_id"], taskID)
	}
}

// The decline. A refused card must leave NOTHING: no task for a supporter to
// find and no payments row for a reconciliation to trip over — and a message
// the requester can act on rather than a 500.
func TestPhase2aSmokeDeclinedCardCreatesNothing(t *testing.T) {
	requireStripeSmoke(t)
	setupCreatedViaDB(t)

	const email = "post.declined@example.test"
	uid, _ := seedRequesterWithCard(t, email, testPMDeclined)
	seedProfileFor(t, uid, email)

	t.Setenv("PAYMENTS_ENFORCED", "1")

	code, body := postTaskJSONAs(t, email,
		`{"title":"Grocery run","category":"delivery","estimated_minutes":60,"is_immediate":true}`)
	if code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d (%v)", code, body)
	}
	if body["error"] != "payment_required" {
		t.Errorf("error = %v, want payment_required", body["error"])
	}
	msg, _ := body["message"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Error("402 carried no message — the client has nothing to show")
	}
	t.Logf("decline message: %q (decline_code=%v)", msg, body["decline_code"])

	var tasks, payments int
	_ = db.QueryRow(context.Background(), `select count(*) from public.tasks`).Scan(&tasks)
	_ = db.QueryRow(context.Background(), `select count(*) from public.payments`).Scan(&payments)
	if tasks != 0 {
		t.Errorf("%d task row(s) survived a declined post", tasks)
	}
	if payments != 0 {
		t.Errorf("%d orphan payments row(s) survived a declined post", payments)
	}
}

// No card on file: a 402 that costs one Stripe round trip and writes nothing.
// This is the first thing every requester will hit on the day the flag flips.
func TestPhase2aSmokePostWithoutACardIsRefusedCleanly(t *testing.T) {
	requireStripeSmoke(t)
	setupCreatedViaDB(t)

	const email = "post.nocard@example.test"
	ctx := context.Background()
	var uid string
	if err := db.QueryRow(ctx,
		`insert into public.users (email, name) values ($1, $2) returning id::text`,
		email, "No Card",
	).Scan(&uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedProfileFor(t, uid, email)

	t.Setenv("PAYMENTS_ENFORCED", "1")

	code, body := postTaskJSONAs(t, email,
		`{"title":"Grocery run","category":"delivery","estimated_minutes":30,"is_immediate":true}`)
	if code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d (%v)", code, body)
	}
	if body["error"] != "payment_method_required" {
		t.Errorf("error = %v, want payment_method_required", body["error"])
	}

	var tasks int
	_ = db.QueryRow(ctx, `select count(*) from public.tasks`).Scan(&tasks)
	if tasks != 0 {
		t.Errorf("%d task row(s) written for a requester with no card", tasks)
	}
}

// ── C. Cancelling releases the hold ────────────────────────────────────────

// "Cancel bills $0" meeting its money side. The hold is cancelled at Stripe,
// the row is canceled, and asking again is a no-op — which matters because
// cancel paths do run twice.
func TestPhase2aSmokeCancelReleasesTheHold(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	w := seedOpsWorld(t, "open")
	ctx := context.Background()

	p, err := CreatePreAuth(ctx, PreAuthInput{
		TaskID:              w.taskID,
		RequesterID:         w.requesterID,
		Category:            "delivery",
		EstimatedMinutes:    60,
		ShoppingBudgetCents: 0,
	})
	if err != nil {
		t.Fatalf("CreatePreAuth: %v", err)
	}
	// seedOpsWorld hands the task a supporter; cancelTask refuses an accepted
	// task by design, and "cancelled before any clock-in" is the only cancel
	// Phase 2a reaches. Detach first so the case under test is the real one.
	if _, err := db.Exec(ctx, `
		update public.tasks
		   set payment_id = $2::uuid, assigned_to_id = null, assigned_to = ''
		 where id = $1::uuid
	`, w.taskID, p.ID); err != nil {
		t.Fatalf("link payment: %v", err)
	}

	code, body := callWithSession(t, cancelTask, http.MethodPost,
		"/tasks/"+w.taskID+"/cancel", w.requesterID, requesterEmail,
		`{"reason":"changed my mind"}`, gin.Params{{Key: "id", Value: w.taskID}})
	if code != http.StatusOK {
		t.Fatalf("cancel: expected 200, got %d (%v)", code, body)
	}
	if bill, _ := body["bill_cents"].(float64); bill != 0 {
		t.Errorf("bill_cents = %v, want 0 — a task cancelled before any clock-in owes nothing", bill)
	}

	if got := paymentStatusOf(t, p.StripePaymentIntentID); got != paymentStatusCanceled {
		t.Errorf("payments row = %q, want %q", got, paymentStatusCanceled)
	}

	pi, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		t.Fatalf("re-fetch intent: %v", err)
	}
	if pi.Status != stripe.PaymentIntentStatusCanceled {
		t.Errorf("Stripe status = %q, want canceled — the requester's funds are still held", pi.Status)
	}

	// Second release: a no-op, not an error.
	if got := releaseTaskHold(ctx, w.taskID, w.requesterID, "repeat"); got != nil {
		t.Errorf("second release returned a payment: %+v", got)
	}

	// An audit row, so an operator can answer "when was this released".
	var audits int
	_ = db.QueryRow(ctx,
		`select count(*) from public.audit_logs where job_id = $1::uuid and action = 'PAYMENT_RELEASED'`,
		w.taskID).Scan(&audits)
	if audits != 1 {
		t.Errorf("%d PAYMENT_RELEASED audit rows, want 1", audits)
	}
}

// The same for an admin takedown: a removed task owes nothing either, so the
// hold goes back the same way.
func TestPhase2aSmokeAdminRemoveReleasesTheHold(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	w := seedOpsWorld(t, "open")
	ctx := context.Background()

	p, err := CreatePreAuth(ctx, PreAuthInput{
		TaskID:           w.taskID,
		RequesterID:      w.requesterID,
		Category:         "delivery",
		EstimatedMinutes: 30,
	})
	if err != nil {
		t.Fatalf("CreatePreAuth: %v", err)
	}

	code, body := callAdminOps(t, adminRemoveTask, "remove", w.taskID, w.adminID, adminEmail,
		`{"reason":"out_of_scope_other"}`)
	if code != http.StatusOK {
		t.Fatalf("remove: expected 200, got %d (%v)", code, body)
	}

	if got := paymentStatusOf(t, p.StripePaymentIntentID); got != paymentStatusCanceled {
		t.Errorf("payments row = %q, want %q", got, paymentStatusCanceled)
	}
	pi, err := paymentintent.Get(p.StripePaymentIntentID, nil)
	if err != nil {
		t.Fatalf("re-fetch intent: %v", err)
	}
	if pi.Status != stripe.PaymentIntentStatusCanceled {
		t.Errorf("Stripe status = %q, want canceled", pi.Status)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// postTaskJSONAs is postTaskJSON with a caller-chosen identity — the smoke
// tests need several requesters in one database, where the created_via suite
// only ever needed one.
func postTaskJSONAs(t *testing.T, email, body string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("email", email)

	createTask(c)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// createTask aligns profiles to users by email and will create the row itself;
// seeding it up front just keeps the handler's logs quiet about it.
func seedProfileFor(t *testing.T, uid, email string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`insert into public.profiles (id, email, name) values ($1::uuid, $2, $3)
		 on conflict (email) do nothing`, uid, email, "Smoke Requester"); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
}
