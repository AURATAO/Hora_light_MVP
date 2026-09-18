package main

// Collecting an outstanding balance, under concurrency.
//
// THE BUG THIS SUITE EXISTS FOR. Double-tapping Settle ran two passes over the
// same balance_due rows. Both called paymentintent.New with the same
// idempotency key `balance_<payment_id>`, and Stripe answered the second with
// HTTP 409 `idempotency_key_in_use` — an error, not a replay of the first
// object, because the cache replays a COMPLETED request and does not
// serialize an in-flight one.
//
// The key still did its real job — nobody was charged twice — so this was a
// UX bug, not a money bug: the loser's 409 was classified as a decline, and a
// settle that had in fact just succeeded told the cardholder "we couldn't
// reach your bank just now". Same root cause as the Stripe Customer 500 fixed
// in 792f3bf; see payments_customer_race_test.go.
//
// The fix is a per-requester advisory lock plus a double-checked re-read, and
// deliberately NOT `select … for update` on the payments row — that deadlocks
// against the pass's own pool-side writes. TestSettleRacePoolWritesAreNotBlocked
// is the executable statement of why.
//
//	docker run -d --rm --name hora-race-test -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=horatest -p 55437:5432 supabase/postgres:15.8.1.060
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55437/horatest' \
//	  go test ./ -run SettleRace -v -count=1

import (
	"context"
	"testing"
	"time"
)

// seedBalanceDueRow makes a requester who owes exactly one balance_due row,
// the only state in which any of this is reachable. Returns the payment id.
func seedBalanceDueRow(t *testing.T, uid string, cents int) string {
	t.Helper()
	taskID := seedSmokeTask(t, uid, "settle.race@example.test")
	var paymentID string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, $5)
		returning id::text
	`, taskID, uid, paymentKindCompletionBalance, paymentStatusBalanceDue, cents).Scan(&paymentID); err != nil {
		t.Fatalf("seed balance_due: %v", err)
	}
	return paymentID
}

func owedCount(t *testing.T, uid string) int {
	t.Helper()
	owed, err := readBalanceDue(context.Background(), db, uid)
	if err != nil {
		t.Fatalf("read balance due: %v", err)
	}
	return len(owed)
}

// ── 1. The serialization primitive ─────────────────────────────────────────

// While one pass holds the settle lock for a requester, no other pass can be
// inside the same section for that requester.
//
// Asserted by observation rather than by timing: the contender must not get
// through while the holder is holding. A test that merely slept and hoped
// would pass just as happily against no lock at all.
func TestSettleRaceAdvisoryLockSerializesPerRequester(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "settle.lock@example.test")
	ctx := context.Background()

	held := make(chan struct{})
	done := make(chan error, 1)

	tx1, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if err := lockPerUser(ctx, tx1, advisoryLockBalanceSettle, uid); err != nil {
		t.Fatalf("holder lock: %v", err)
	}
	close(held)

	go func() {
		<-held
		tx2, err := db.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		done <- lockPerUser(ctx, tx2, advisoryLockBalanceSettle, uid)
	}()

	// Long enough that a missing lock would let the contender through first.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the contender acquired the settle lock while it was held (err=%v) — no mutual exclusion", err)
	default:
	}

	if err := tx1.Rollback(ctx); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("contender lock after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the contender never acquired the lock after it was released")
	}
}

// The settle namespace must be its own. Advisory locks share one global int
// space database-wide, so a namespace collision would make unrelated features
// block each other — settling a balance would stall that user's card sheet.
func TestSettleRaceNamespaceIsDistinct(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "settle.ns@example.test")
	ctx := context.Background()

	if advisoryLockBalanceSettle == advisoryLockCustomer ||
		advisoryLockBalanceSettle == advisoryLockConnectAccount {
		t.Fatal("advisoryLockBalanceSettle collides with another namespace")
	}

	txSettle, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin settle holder: %v", err)
	}
	defer func() { _ = txSettle.Rollback(ctx) }()
	if err := lockPerUser(ctx, txSettle, advisoryLockBalanceSettle, uid); err != nil {
		t.Fatalf("settle lock: %v", err)
	}

	// The customer lock for the SAME user must still be free.
	txCus, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin customer contender: %v", err)
	}
	defer func() { _ = txCus.Rollback(ctx) }()
	var got bool
	if err := txCus.QueryRow(ctx,
		`select pg_try_advisory_xact_lock($1, hashtext($2))`, advisoryLockCustomer, uid).Scan(&got); err != nil {
		t.Fatalf("try customer lock: %v", err)
	}
	if !got {
		t.Fatal("holding the settle lock blocked the customer lock — the namespaces are not distinct")
	}
}

// Settling for one requester must not stall another's settle. The lock is
// per-user; a global one would serialize every requester on the platform
// behind whoever is currently talking to Stripe.
func TestSettleRaceDoesNotBlockOtherRequesters(t *testing.T) {
	setupStripeWebhookDB(t)
	uidA := seedRaceUser(t, "settle.a@example.test")
	uidB := seedRaceUser(t, "settle.b@example.test")
	ctx := context.Background()

	txA, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if err := lockPerUser(ctx, txA, advisoryLockBalanceSettle, uidA); err != nil {
		t.Fatalf("lock A: %v", err)
	}

	txB, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin B: %v", err)
	}
	defer func() { _ = txB.Rollback(ctx) }()
	var got bool
	if err := txB.QueryRow(ctx,
		`select pg_try_advisory_xact_lock($1, hashtext($2))`, advisoryLockBalanceSettle, uidB).Scan(&got); err != nil {
		t.Fatalf("try lock B: %v", err)
	}
	if !got {
		t.Fatal("settling for one requester blocked another — the lock is not per-user")
	}
}

// ── 2. The property the fix promises ───────────────────────────────────────

// The loser of a double-tap charges NOTHING and reports success.
//
// This reproduces production's exact ordering. The winner's row write commits
// through the pool while the lock is still held (that is what recordCapture
// does), and only then is the lock released — so the loser's re-read, taken
// after the lock is granted, sees a requester who owes nothing and returns
// before it ever reaches Stripe.
//
// NOT VACUOUS: no Stripe key is configured in the hermetic suite, so if the
// double-checked re-read regressed, the pass would reach paymentintent.New
// and come back with a decline or an error. "settled 0, nothing owed, no
// decline" is reachable only by returning early.
func TestSettleRaceLoserChargesNothing(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "settle.loser@example.test")
	paymentID := seedBalanceDueRow(t, uid, 1950)
	ctx := context.Background()

	// The winner: holds the settle lock, has not finished yet.
	winner, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin winner: %v", err)
	}
	defer func() { _ = winner.Rollback(context.Background()) }()
	if err := lockPerUser(ctx, winner, advisoryLockBalanceSettle, uid); err != nil {
		t.Fatalf("winner lock: %v", err)
	}

	// Control against a vacuous test: the row really is collectable right now,
	// so a loser that skipped the lock would have charged it.
	if n := owedCount(t, uid); n != 1 {
		t.Fatalf("setup: want 1 balance_due row, got %d", n)
	}

	type passResult struct {
		out settleOutcome
		err error
	}
	results := make(chan passResult, 1)
	go func() {
		out, err := runSettlePass(ctx, uid)
		results <- passResult{out, err}
	}()

	// The loser must be parked on the lock, not racing ahead to Stripe.
	time.Sleep(300 * time.Millisecond)
	select {
	case r := <-results:
		t.Fatalf("the loser ran the pass while the winner held the lock (out=%+v err=%v)", r.out, r.err)
	default:
	}

	// The winner finishes: recordCapture's write, through the POOL, while the
	// advisory lock is still held. That this does not block is the second half
	// of the fix — a `select … for update` here would have the winner waiting
	// on its own row lock.
	poolWrite := make(chan error, 1)
	go func() {
		_, err := db.Exec(context.Background(), `
			update public.payments set status = $2, captured_cents = authorized_cents, updated_at = now()
			 where id = $1::uuid
		`, paymentID, paymentStatusCaptured)
		poolWrite <- err
	}()
	select {
	case err := <-poolWrite:
		if err != nil {
			t.Fatalf("winner's pool write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the winner's pool write blocked while the settle lock was held — the lock is taking row locks it must not take")
	}

	// ...and releases the lock.
	if err := winner.Rollback(ctx); err != nil {
		t.Fatalf("release winner: %v", err)
	}

	select {
	case r := <-results:
		if r.err != nil {
			t.Fatalf("loser pass errored: %v", r.err)
		}
		if r.out.settledCents != 0 || r.out.settledCount != 0 {
			t.Fatalf("the loser charged something: %+v", r.out)
		}
		if r.out.authErr != nil || r.out.declineMessage != "" {
			t.Fatalf("the loser reported a failure for an already-settled balance: authErr=%v decline=%q — this is the bug",
				r.out.authErr, r.out.declineMessage)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the loser never finished after the lock was released")
	}

	if n := owedCount(t, uid); n != 0 {
		t.Fatalf("after settlement: want 0 balance_due rows, got %d", n)
	}
}

// A pass over a requester who owes nothing charges nothing and reports quiet
// success. (The handler does not even get this far in that case — it has an
// unlocked pre-read that returns first, which is what keeps the ordinary
// banner-less page load off the lock entirely.)
func TestSettleRaceNothingOwedIsSuccess(t *testing.T) {
	setupStripeWebhookDB(t)
	uid := seedRaceUser(t, "settle.clear@example.test")

	out, err := runSettlePass(context.Background(), uid)
	if err != nil {
		t.Fatalf("pass with nothing owed: %v", err)
	}
	if out.settledCents != 0 || out.authErr != nil || out.declineMessage != "" {
		t.Fatalf("nothing owed should be quiet success, got %+v", out)
	}
}
