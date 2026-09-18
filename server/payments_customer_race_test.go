package main

// Get-or-create for the Stripe Customer, under concurrency.
//
// THE BUG THIS SUITE EXISTS FOR. Two simultaneous first-calls for one user
// each found no Customer, each called customer.New with the same idempotency
// key, and Stripe answered the second with HTTP 409 `idempotency_key_in_use` —
// an error, not a replay of the first object, because the cache replays a
// COMPLETED request and does not serialize an in-flight one. The handler
// turned that into a 500. Found on 2026-09-18 by a local UI sweep, on an
// ordinary page load that happened to read the payment gate twice.
//
// Two layers, because they fail for different reasons:
//
//	1. HERMETIC (DB only). The serialization primitive: does the advisory lock
//	   actually make two overlapping creates take turns, and does the loser
//	   adopt rather than overwrite? Runs in the normal suite.
//
//	2. SMOKE (real Stripe). The property the fix promises, against the API
//	   whose behaviour caused the bug: N parallel calls, ONE Customer, N
//	   identical answers, no 409. A fake here would assert our own assumption
//	   about Stripe's idempotency back at us, which is the assumption that was
//	   wrong in the first place.
//
//	docker run -d --rm --name hora-race-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55437:5432 supabase/postgres:15.8.1.060
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55437/horatest' \
//	  go test ./ -run CustomerRace -v -count=1
//
//	# and, with a test key in the environment:
//	STRIPE_SMOKE=1 TEST_DATABASE_URL=... go test ./ -run CustomerRaceSmoke -v -count=1

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/customer"
)

// seedRaceUser makes a user with no Stripe Customer — the only state in which
// any of this is reachable.
func seedRaceUser(t *testing.T, email string) string {
	t.Helper()
	var uid string
	if err := db.QueryRow(context.Background(),
		`insert into public.users (email, name) values ($1, $2) returning id::text`,
		email, "Race Requester",
	).Scan(&uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return uid
}

func storedCustomer(t *testing.T, uid string) string {
	t.Helper()
	got, err := readStripeCustomer(context.Background(), db, uid)
	if err != nil {
		t.Fatalf("read stored customer: %v", err)
	}
	return got
}

// ── 1. The serialization primitive, without Stripe ─────────────────────────

// The advisory lock does what the fix rests on: while one transaction holds it
// for a user, no other transaction can be inside the same section for that
// user.
//
// Asserted by observation rather than by timing: the second taker records the
// moment it acquired the lock, and that moment must be AFTER the first taker
// released it. A test that merely slept and hoped would pass just as happily
// against no lock at all.
func TestCustomerRaceAdvisoryLockSerializesPerUser(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "race.lock@example.test")
	ctx := context.Background()

	var released, acquired time.Time
	held := make(chan struct{})
	done := make(chan error, 1)

	// Holder: takes the lock and sits on it.
	tx1, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	if _, err := tx1.Exec(ctx,
		`select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockCustomer, uid); err != nil {
		t.Fatalf("holder lock: %v", err)
	}
	close(held)

	// Contender: blocks until the holder commits.
	go func() {
		<-held
		tx2, err := db.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		if _, err := tx2.Exec(ctx,
			`select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockCustomer, uid); err != nil {
			done <- err
			return
		}
		acquired = time.Now()
		done <- nil
	}()

	// Long enough that a missing lock would let the contender through first.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the contender acquired the lock while it was held (err=%v) — no mutual exclusion", err)
	default:
	}

	released = time.Now()
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit holder: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("contender: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the contender never acquired the lock after it was released")
	}
	if !acquired.After(released) {
		t.Errorf("contender acquired at %v, holder released at %v — the wait was not caused by the lock",
			acquired, released)
	}
}

// The two namespaces are independent: a user creating a Customer must not
// block the same user creating a Connect account, and vice versa. They are
// different objects on different screens and there is no reason for one to
// wait on the other — and if a future namespace were added with a copied
// constant, this is the test that notices.
func TestCustomerRaceLockNamespacesDoNotCollide(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "race.namespaces@example.test")
	ctx := context.Background()

	if advisoryLockCustomer == advisoryLockConnectAccount {
		t.Fatal("the two advisory-lock namespaces are the same constant")
	}

	txCus, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin customer tx: %v", err)
	}
	defer func() { _ = txCus.Rollback(ctx) }()
	if err := lockPerUser(ctx, txCus, advisoryLockCustomer, uid); err != nil {
		t.Fatalf("lock customer: %v", err)
	}

	txAcct, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin connect tx: %v", err)
	}
	defer func() { _ = txAcct.Rollback(ctx) }()
	var got bool
	if err := txAcct.QueryRow(ctx,
		`select pg_try_advisory_xact_lock($1, hashtext($2))`, advisoryLockConnectAccount, uid).Scan(&got); err != nil {
		t.Fatalf("try connect lock: %v", err)
	}
	if !got {
		t.Error("the Connect lock blocked on the Customer lock for the same user — the namespaces collide")
	}
}

// Different users must NOT block each other. A lock keyed on something coarser
// (a table, a constant) would serialize every first-time card sheet in the
// product behind one Stripe round trip.
func TestCustomerRaceAdvisoryLockIsPerUserNotGlobal(t *testing.T) {
	setupStripeWebhookDB(t)
	uidA := seedRaceUser(t, "race.a@example.test")
	uidB := seedRaceUser(t, "race.b@example.test")
	ctx := context.Background()

	txA, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if _, err := txA.Exec(ctx,
		`select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockCustomer, uidA); err != nil {
		t.Fatalf("lock A: %v", err)
	}

	// B must go straight through while A is held. try_lock rather than lock so
	// the failure is an assertion instead of a hung test.
	txB, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer func() { _ = txB.Rollback(ctx) }()
	var got bool
	if err := txB.QueryRow(ctx,
		`select pg_try_advisory_xact_lock($1, hashtext($2))`, advisoryLockCustomer, uidB).Scan(&got); err != nil {
		t.Fatalf("try lock B: %v", err)
	}
	if !got {
		t.Error("user B blocked on user A's lock — the lock is not keyed per user")
	}
}

// The write half: the UPDATE coalesces, so a late writer ADOPTS the stored id
// rather than overwriting it. This is what makes a loser's answer correct
// even in the branch where one somehow gets past the lock.
func TestCustomerRaceStoredCustomerIsNeverOverwritten(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "race.adopt@example.test")
	ctx := context.Background()

	var first string
	if err := db.QueryRow(ctx, `
		update public.users set stripe_customer_id = coalesce(stripe_customer_id, $2)
		 where id = $1::uuid returning stripe_customer_id`, uid, "cus_winner").Scan(&first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if first != "cus_winner" {
		t.Fatalf("first write stored %q", first)
	}

	var second string
	if err := db.QueryRow(ctx, `
		update public.users set stripe_customer_id = coalesce(stripe_customer_id, $2)
		 where id = $1::uuid returning stripe_customer_id`, uid, "cus_loser").Scan(&second); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if second != "cus_winner" {
		t.Errorf("a second writer overwrote the stored customer: %q — a user's cards would move wallets", second)
	}
	if got := storedCustomer(t, uid); got != "cus_winner" {
		t.Errorf("stored = %q, want cus_winner", got)
	}
}

// Two users can never share one Customer. The partial unique index from
// migration 20260914120000 is the guarantee; this is the test that says so, so
// that dropping it fails here rather than in a webhook that cannot tell whose
// payment it is holding.
func TestCustomerRaceTwoUsersCannotShareACustomer(t *testing.T) {
	setupStripeWebhookDB(t)
	uidA := seedRaceUser(t, "race.unique.a@example.test")
	uidB := seedRaceUser(t, "race.unique.b@example.test")
	ctx := context.Background()

	if _, err := db.Exec(ctx,
		`update public.users set stripe_customer_id = $2 where id = $1::uuid`, uidA, "cus_shared"); err != nil {
		t.Fatalf("assign to A: %v", err)
	}
	if _, err := db.Exec(ctx,
		`update public.users set stripe_customer_id = $2 where id = $1::uuid`, uidB, "cus_shared"); err == nil {
		t.Fatal("two users were allowed to share one Stripe Customer — the unique index is gone")
	}

	// And NULLs stay exempt, which is why the index is partial: most accounts
	// never add a card, and they would otherwise all collide on NULL.
	if _, err := db.Exec(ctx,
		`insert into public.users (email, name) values ($1, $2), ($3, $2)`,
		"race.null.a@example.test", "Race", "race.null.b@example.test"); err != nil {
		t.Errorf("two users without customers collided: %v", err)
	}
}

// ── 2. The real thing, against Stripe ──────────────────────────────────────

// N parallel first-calls for one fresh user: every one returns 200-equivalent
// (no error), every one returns the SAME customer id, and Stripe holds exactly
// one Customer for that user.
//
// This is the test that fails on the old code. Without the lock the second
// caller meets `idempotency_key_in_use` and comes back an error — which is
// precisely the 500 the sweep saw.
func TestCustomerRaceSmokeParallelFirstCallsAgreeOnOneCustomer(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "race.parallel@example.test"
	uid := seedRaceUser(t, email)
	t.Cleanup(func() {
		if id := storedCustomer(t, uid); id != "" {
			if _, err := customer.Del(id, nil); err != nil {
				t.Logf("could not delete test customer %s: %v", id, err)
			}
		}
	})

	const callers = 6
	ids := make([]string, callers)
	errs := make([]error, callers)

	// One barrier, released at once, so the calls genuinely overlap rather
	// than queueing behind goroutine start-up.
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			ids[i], errs[i] = stripeCustomerFor(context.Background(), uid, email)
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d failed: %v", i, err)
		}
	}
	if t.Failed() {
		t.Fatalf("concurrent get-or-create is still failing callers — this is the 409 the fix exists for")
	}

	want := ids[0]
	if want == "" {
		t.Fatal("no customer id came back")
	}
	for i, got := range ids {
		if got != want {
			t.Errorf("caller %d answered %q, caller 0 answered %q — the callers disagree about the wallet", i, got, want)
		}
	}
	if stored := storedCustomer(t, uid); stored != want {
		t.Errorf("stored customer = %q, callers answered %q", stored, want)
	}

	// And Stripe agrees there is only one. Searched by the metadata every
	// create stamps, so a duplicate minted by a losing branch is visible here
	// even though no row points at it.
	assertOneStripeCustomerForUser(t, uid, want)

	// A later call reuses it rather than making another — the fast path, and
	// the state every subsequent request in the user's life is in.
	again, err := stripeCustomerFor(context.Background(), uid, email)
	if err != nil {
		t.Fatalf("follow-up call: %v", err)
	}
	if again != want {
		t.Errorf("follow-up minted a different customer: %q, want %q", again, want)
	}
	assertOneStripeCustomerForUser(t, uid, want)
}

// The same property through the HTTP handler, which is where it was actually
// observed: two simultaneous GET /payments/payment-methods for a user with no
// Customer. Both answered 500-then-200 before the fix; both must answer 200 now.
func TestCustomerRaceSmokeParallelHandlerCallsBothSucceed(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	const email = "race.handler@example.test"
	uid := seedRaceUser(t, email)
	t.Cleanup(func() {
		if id := storedCustomer(t, uid); id != "" {
			if _, err := customer.Del(id, nil); err != nil {
				t.Logf("could not delete test customer %s: %v", id, err)
			}
		}
	})

	type result struct {
		code int
		body map[string]any
	}
	results := make([]result, 2)
	var done sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < 2; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			code, body := callWithSession(t, listPaymentMethodsHandler,
				"GET", "/payments/payment-methods", uid, email, "", nil)
			results[i] = result{code, body}
		}(i)
	}
	start.Done()
	done.Wait()

	for i, r := range results {
		if r.code != 200 {
			t.Errorf("request %d answered %d (%v) — this is the 500 the sweep found", i, r.code, r.body)
		}
	}
	assertOneStripeCustomerForUser(t, uid, storedCustomer(t, uid))
}

// A create that cannot be stored leaves nothing behind at Stripe.
//
// Exercised by pointing the write at a user id that does not exist, so the
// UPDATE returns no row and the persist branch fails with a Customer already
// minted. The assertion is that the Customer is GONE afterwards — the old code
// logged an orphan and left it in the dashboard forever.
func TestCustomerRaceSmokeUnstorableCustomerIsDeleted(t *testing.T) {
	requireStripeSmoke(t)
	setupStripeWebhookDB(t)

	// A well-formed uuid with no row behind it.
	const ghost = "00000000-0000-4000-8000-0000000000ff"

	_, err := createStripeCustomer(context.Background(), ghost, "race.ghost@example.test")
	if err == nil {
		t.Fatal("creating a customer for a nonexistent user should fail")
	}

	// Nothing was left behind: no Customer carries this user_id.
	if n, ids := stripeCustomersForUser(t, ghost); n != 0 {
		for _, id := range ids {
			if _, delErr := customer.Del(id, nil); delErr != nil {
				t.Logf("cleanup of %s: %v", id, delErr)
			}
		}
		t.Errorf("%d orphan customer(s) left at Stripe after an unstorable create: %v", n, ids)
	}
}

// customerLookbackWindow bounds the duplicate scan. Every Customer these
// tests can possibly have created was created seconds ago, and a window keeps
// the listing to a handful of objects even on a test account with a long
// history.
const customerLookbackWindow = 10 * time.Minute

// stripeCustomersForUser lists the test-mode Customers stamped with this
// user_id — including any that no database row points at, which is the whole
// point: an orphan is invisible to every query that starts from our own data.
//
// LIST, NOT SEARCH, and the difference is load-bearing. `customer.Search`
// queries Stripe's metadata index, which is EVENTUALLY consistent — documented
// as up to about a minute behind. A duplicate-detection assertion built on it
// cannot tell "there is no second Customer" from "the index has not caught up",
// so it reports whichever the timing produces; the first draft of this suite
// polled for twenty seconds and still read 0 for a Customer that demonstrably
// existed. `customer.List` reads the live objects and is immediately
// consistent, so the filtering happens here instead of in a query language.
func stripeCustomersForUser(t *testing.T, uid string) (int, []string) {
	t.Helper()
	since := time.Now().Add(-customerLookbackWindow).Unix()
	params := &stripe.CustomerListParams{
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since},
	}
	params.Limit = stripe.Int64(100)

	var ids []string
	iter := customer.List(params)
	for iter.Next() {
		cus := iter.Customer()
		if cus.Metadata != nil && cus.Metadata["user_id"] == uid {
			ids = append(ids, cus.ID)
		}
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("list customers: %v", err)
	}
	return len(ids), ids
}

// assertOneStripeCustomerForUser is the duplicate check: exactly one Customer
// carries this user_id, and it is the one we stored.
func assertOneStripeCustomerForUser(t *testing.T, uid, want string) {
	t.Helper()
	n, ids := stripeCustomersForUser(t, uid)
	if n != 1 {
		t.Errorf("Stripe holds %d customers for user=%s (%v) — want exactly 1 (%s)", n, uid, ids, want)
		return
	}
	if ids[0] != want {
		t.Errorf("Stripe holds customer %s for user=%s, but %s is stored", ids[0], uid, want)
	}
}
