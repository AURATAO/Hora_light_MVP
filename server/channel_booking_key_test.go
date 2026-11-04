package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

// Audit 2026-10-30, gap B.3, against a real Postgres and the real createTask:
// a text booking retried after Stripe timed out — with the hold already
// placed — must neither make a second task nor place a second hold.

const bookingKeyMigrationPath = "../supabase/migrations/20261104120000_tasks_booking_key.sql"

// stripeWithReplay answers PaymentIntent creates the way Stripe does: one
// intent per idempotency key, replayed on a repeat. timeouts: how many of
// the first calls place their hold and then lose the response.
type stripeWithReplay struct {
	mu       sync.Mutex
	byKey    map[string]*stripe.PaymentIntent
	keys     []string
	timeouts int
}

func stubStripeWithReplay(t *testing.T, timeouts int) *stripeWithReplay {
	t.Helper()
	forceStripeKey(t, "sk_test_fixture")
	s := &stripeWithReplay{byKey: map[string]*stripe.PaymentIntent{}, timeouts: timeouts}
	prevNew, prevPM := stripeCreatePaymentIntent, stripeDefaultPaymentMethodFor
	stripeCreatePaymentIntent = func(_ string, params *stripe.PaymentIntentParams) (*stripe.PaymentIntent, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		key := ""
		if params.IdempotencyKey != nil {
			key = *params.IdempotencyKey
		}
		s.keys = append(s.keys, key)
		pi, seen := s.byKey[key]
		if !seen || key == "" {
			pi = &stripe.PaymentIntent{ID: fmt.Sprintf("pi_%d", len(s.byKey)+1), Amount: *params.Amount,
				Status: stripe.PaymentIntentStatusRequiresCapture}
			s.byKey[key] = pi
		}
		if s.timeouts > 0 {
			s.timeouts--
			// The hold is placed; the response never arrives.
			return nil, &url.Error{Op: "Post", URL: "https://api.stripe.com/v1/payment_intents", Err: context.DeadlineExceeded}
		}
		return pi, nil
	}
	stripeDefaultPaymentMethodFor = func(string, string) (string, error) { return "pm_fixture", nil }
	t.Cleanup(func() { stripeCreatePaymentIntent, stripeDefaultPaymentMethodFor = prevNew, prevPM })
	return s
}

func (s *stripeWithReplay) holds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byKey)
}

func setupBookingKeyDB(t *testing.T) string {
	t.Helper()
	// The fixture the createTask DB tests use (task_created_via_test.go),
	// plus the promo tables discardUnpaidTask reads and this migration.
	setupCreatedViaDB(t)
	mustExec(t, `DROP TABLE IF EXISTS public.promo_redemptions, public.promo_codes CASCADE`)
	for _, path := range []string{promoCodesMigrationPath, bookingKeyMigrationPath} {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, string(migration))
	}
	t.Setenv("PAYMENTS_ENFORCED", "1")
	var uid string
	if err := db.QueryRow(context.Background(), `INSERT INTO public.users (email, name) VALUES ($1, 'Rita') RETURNING id::text`, createdViaEmail).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	giveCard(t, uid)
	return uid
}

func countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

var bookingKeyDraft = llm.TaskDraft{Title: "Laundry pickup", Category: "laundry", Location1: "12 Bedford Ave", DurationMinutes: 30}

func TestChatRetryAfterStripeTimeoutHoldsOnce(t *testing.T) {
	uid := setupBookingKeyDB(t)
	s := stubStripeWithReplay(t, 1)
	b := &chatBooker{sqlDB: sqldb}
	ctx := context.Background()
	opts := channel.PostOptions{AutoExtend: true, BookingKey: "chat_key_1"}

	// The YES: the hold lands at Stripe, the response is lost.
	if r := b.Post(ctx, uid, bookingKeyDraft, opts); r.Outcome != channel.PostFailed {
		t.Fatalf("first post: %+v, want a failure the person is asked to retry", r)
	}
	if s.holds() != 1 {
		t.Fatalf("stripe holds %d", s.holds())
	}
	// The task and its payments row are kept — a hold may exist under them.
	if n := countRows(t, `select count(*) from public.tasks where booking_key = 'chat_key_1' and status = 'pending_payment'`); n != 1 {
		t.Fatalf("parked tasks %d, want the first attempt's task kept", n)
	}
	if n := countRows(t, `select count(*) from public.payments where status = 'requires_auth' and stripe_payment_intent_id is null`); n != 1 {
		t.Fatalf("uncertain payments rows %d", n)
	}
	var firstTask string
	if err := db.QueryRow(ctx, `select id::text from public.tasks where booking_key = 'chat_key_1'`).Scan(&firstTask); err != nil {
		t.Fatal(err)
	}
	if got, err := b.BookedTask(ctx, uid, "chat_key_1"); err != nil || got != "" {
		t.Fatalf("a parked task reads as booked: %q %v", got, err)
	}

	// The retry YES, same key.
	r := b.Post(ctx, uid, bookingKeyDraft, opts)
	if r.Outcome != channel.PostBooked || r.TaskID != firstTask {
		t.Fatalf("retry: %+v, want the first task %s booked", r, firstTask)
	}
	if s.holds() != 1 {
		t.Fatalf("stripe holds %d after the retry — a second hold", s.holds())
	}
	if len(s.keys) != 2 || s.keys[0] == "" || s.keys[0] != s.keys[1] {
		t.Fatalf("idempotency keys %v: the retry must repeat the first call's key", s.keys)
	}
	if n := countRows(t, `select count(*) from public.tasks where requester_id = $1::uuid`, uid); n != 1 {
		t.Fatalf("tasks %d", n)
	}
	if n := countRows(t, `select count(*) from public.tasks where id = $1::uuid and status = 'open' and payment_id is not null`, firstTask); n != 1 {
		t.Fatal("the task is not posted on its hold")
	}
	if n := countRows(t, `select count(*) from public.payments where task_id = $1::uuid and status = 'authorized' and stripe_payment_intent_id = 'pi_1'`, firstTask); n != 1 {
		t.Fatal("the payments row does not name the one hold")
	}
	if got, err := b.BookedTask(ctx, uid, "chat_key_1"); err != nil || got != firstTask {
		t.Fatalf("booked task %q %v", got, err)
	}

	// A third YES (a crash after the post, recovered by retrying): the same
	// task, and Stripe is not asked at all.
	if r := b.Post(ctx, uid, bookingKeyDraft, opts); r.Outcome != channel.PostBooked || r.TaskID != firstTask {
		t.Fatalf("replay: %+v", r)
	}
	if len(s.keys) != 2 || countRows(t, `select count(*) from public.tasks`) != 1 {
		t.Fatalf("replay reached Stripe (%d calls) or wrote a task", len(s.keys))
	}

	// A different errand (another key) is a new booking.
	if r := b.Post(ctx, uid, bookingKeyDraft, channel.PostOptions{AutoExtend: true, BookingKey: "chat_key_2"}); r.Outcome != channel.PostBooked || r.TaskID == firstTask {
		t.Fatalf("second errand: %+v", r)
	}
	if s.holds() != 2 {
		t.Fatalf("holds %d", s.holds())
	}
}

// Without a key (mobile, old clients) an uncertain hold no longer discards
// the attempt (fix 2026-11-04): the task and its payments row are kept —
// a hold may exist under them — and the person is told to try again.
func TestKeylessTimeoutKeepsTheTask(t *testing.T) {
	uid := setupBookingKeyDB(t)
	stubStripeWithReplay(t, 1)
	if r := (&chatBooker{sqlDB: sqldb}).Post(context.Background(), uid, bookingKeyDraft, channel.PostOptions{AutoExtend: true}); r.Outcome != channel.PostFailed {
		t.Fatalf("post: %+v", r)
	}
	if n := countRows(t, `select count(*) from public.tasks where status = 'pending_payment'`); n != 1 {
		t.Fatalf("parked tasks %d, want the attempt kept", n)
	}
	if n := countRows(t, `select count(*) from public.payments where status = 'requires_auth' and stripe_payment_intent_id is null`); n != 1 {
		t.Fatalf("uncertain payments rows %d, want it kept", n)
	}
}

// webTaskBody is the new-task form's post, with its booking key.
func webTaskBody(key string) string {
	return `{"title":"Laundry pickup","category":"laundry","location_text":"12 Bedford Ave, Brooklyn, NY 11211","estimated_minutes":30,"is_immediate":true,"booking_key":"` + key + `"}`
}

// The web form (fix 2026-11-04): a resubmit after a Stripe timeout carries
// the form's key, finds the first task and repeats its Stripe call — one
// task, one hold.
func TestWebRetryAfterStripeTimeoutHoldsOnce(t *testing.T) {
	uid := setupBookingKeyDB(t)
	s := stubStripeWithReplay(t, 1)
	const formKey = "3f1c2a9e-7b4d-4e0a-9c1f-2b6d8e4a5c70"

	code, body := postTaskJSON(t, webTaskBody(formKey))
	if code != 502 || body["error"] != "payments_uncertain" || body["message"] != "Something went wrong, please try again." {
		t.Fatalf("first post: %d %v", code, body)
	}
	if n := countRows(t, `select count(*) from public.tasks where booking_key = $1 and status = 'pending_payment'`, "web:"+formKey); n != 1 {
		t.Fatalf("parked tasks %d, want the first attempt kept under web:<key>", n)
	}
	if n := countRows(t, `select count(*) from public.payments where status = 'requires_auth' and stripe_payment_intent_id is null`); n != 1 {
		t.Fatalf("uncertain payments rows %d, want it kept", n)
	}
	var firstTask string
	if err := db.QueryRow(context.Background(), `select id::text from public.tasks`).Scan(&firstTask); err != nil {
		t.Fatal(err)
	}

	code, body = postTaskJSON(t, webTaskBody(formKey))
	if code != 201 || body["id"] != firstTask {
		t.Fatalf("retry: %d %v, want the first task %s", code, body, firstTask)
	}
	if s.holds() != 1 || len(s.keys) != 2 || s.keys[0] != s.keys[1] {
		t.Fatalf("holds %d, idempotency keys %v: the retry must repeat the first call", s.holds(), s.keys)
	}
	if n := countRows(t, `select count(*) from public.tasks where requester_id = $1::uuid`, uid); n != 1 {
		t.Fatalf("tasks %d", n)
	}
	if n := countRows(t, `select count(*) from public.tasks where id = $1::uuid and status = 'open' and payment_id is not null`, firstTask); n != 1 {
		t.Fatal("the task is not posted on its hold")
	}
	if n := countRows(t, `select count(*) from public.payments`); n != 1 {
		t.Fatalf("payments rows %d", n)
	}

	// A double-tap after it posted: the same task, Stripe not asked.
	if code, body = postTaskJSON(t, webTaskBody(formKey)); code != 201 || body["id"] != firstTask {
		t.Fatalf("replay: %d %v", code, body)
	}
	if len(s.keys) != 2 {
		t.Fatalf("replay reached Stripe (%d calls)", len(s.keys))
	}

	// A new form is a new key: a new task, a new hold.
	code, body = postTaskJSON(t, webTaskBody("9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"))
	if code != 201 || body["id"] == firstTask {
		t.Fatalf("new form: %d %v", code, body)
	}
	if s.holds() != 2 || countRows(t, `select count(*) from public.tasks`) != 2 {
		t.Fatalf("holds %d", s.holds())
	}
}

// A body key is namespaced: it can never name the chat's key, and only a
// token shape is accepted.
func TestWebBookingKeyShape(t *testing.T) {
	uid := setupBookingKeyDB(t)
	stubStripeWithReplay(t, 0)
	const shared = "chat_key_1234567890"
	if r := (&chatBooker{sqlDB: sqldb}).Post(context.Background(), uid, bookingKeyDraft, channel.PostOptions{AutoExtend: true, BookingKey: shared}); r.Outcome != channel.PostBooked {
		t.Fatalf("chat post: %+v", r)
	}
	code, body := postTaskJSON(t, webTaskBody(shared))
	if code != 201 || body["replayed"] == true {
		t.Fatalf("web post with the chat's key string: %d %v, want a new task", code, body)
	}
	if n := countRows(t, `select count(*) from public.tasks`); n != 2 {
		t.Fatalf("tasks %d", n)
	}
	for _, bad := range []string{"short", "has spaces in it 1234", `quote"1234567890123`} {
		if code, _ := postTaskJSON(t, strings.Replace(webTaskBody("x"), `"x"`, strconvQuote(bad), 1)); code != 400 {
			t.Errorf("key %q: %d, want 400", bad, code)
		}
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// Gap A.3 at createTask: the estimate the person said YES to is a ceiling.
func TestChatPostAboveTheAgreedQuoteIsRefused(t *testing.T) {
	uid := setupBookingKeyDB(t)
	s := stubStripeWithReplay(t, 0)
	b := &chatBooker{sqlDB: sqldb}
	ctx := context.Background()
	now := quoteTask("laundry", 30, 0, resolveRateCentsPerMin(time.Now())).TotalCents

	r := b.Post(ctx, uid, bookingKeyDraft, channel.PostOptions{AutoExtend: true, BookingKey: "chat_key_1", QuotedCents: now - 100})
	if r.Outcome != channel.PostPriceChanged || r.QuoteCents != now {
		t.Fatalf("post above the quote: %+v, want PostPriceChanged at %d", r, now)
	}
	if countRows(t, `select count(*) from public.tasks`) != 0 || s.holds() != 0 {
		t.Fatal("a refused post wrote a task or held money")
	}
	// Equal books.
	if r := b.Post(ctx, uid, bookingKeyDraft, channel.PostOptions{AutoExtend: true, BookingKey: "chat_key_1", QuotedCents: now}); r.Outcome != channel.PostBooked {
		t.Fatalf("post at the quote: %+v", r)
	}
	// The quote the chat shows is the one createTask holds itself to.
	if cents, _, err := b.Quote(ctx, bookingKeyDraft, 0); err != nil || cents != now {
		t.Fatalf("chat quote %d, createTask's %d (%v)", cents, now, err)
	}
}

func TestPostResultFromQuoteChanged(t *testing.T) {
	r := postResultFrom(409, []byte(`{"error":"quote_changed","quote_cents":2400}`))
	if r.Outcome != channel.PostPriceChanged || r.QuoteCents != 2400 {
		t.Fatalf("%+v", r)
	}
	if r := postResultFrom(409, []byte(`{"error":"booking_in_progress"}`)); r.Outcome != channel.PostFailed {
		t.Fatalf("%+v", r)
	}
	if r := postResultFrom(502, []byte(`{"error":"payments_uncertain"}`)); r.Outcome != channel.PostFailed {
		t.Fatalf("%+v", r)
	}
}

func TestBookingFailedOpsNotice(t *testing.T) {
	n := channel.HandoffNotice{IdentityID: "id-1", Channel: channel.IMessage, PhoneHint: channel.PhoneHint("+15551234567"),
		Reason: channel.HandoffBookingFailed, BookingProblem: "stuck booking recovered: 1 of 1 errands not booked"}
	body := opsNoticeHTML(n, "rita@example.test")
	for _, want := range []string{"A text booking failed: stuck booking recovered", "still with the bot", "•••• 4567"} {
		if !strings.Contains(body, want) {
			t.Fatalf("notice missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "5551234567") {
		t.Fatal("the notice carries the full number")
	}
}
