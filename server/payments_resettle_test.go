package main

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func seedLiveHold(t *testing.T, taskID, requesterID, status string, cents int) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		insert into public.payments (task_id, requester_id, kind, status,
		                             stripe_payment_intent_id, authorized_cents)
		values ($1::uuid, $2::uuid, $3, $4, 'pi_'||replace($1::text,'-',''), $5)
		returning id::text
	`, taskID, requesterID, paymentKindTaskPayment, status, cents).Scan(&id); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	return id
}

func markCompletedAgo(t *testing.T, taskID string, minutesAgo int, w opsWorld) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		update public.tasks set status='completed', completed_at = now() - make_interval(mins => $2),
		       assigned_to_id=$3::uuid, assigned_to=$4,
		       completion_photo_url='https://example.test/done.jpg'
		 where id=$1::uuid`, taskID, minutesAgo, w.supporterID, oldSupporterEmail); err != nil {
		t.Fatal(err)
	}
}

// Shape one: the chain died before the capture. The sweep captures the
// hold, pays, and records the settlement — the real orphan, on an
// `authorized` row, against a fake Stripe that succeeds. A completion
// still inside its grace window is left alone; a second pass finds nothing.
func TestResettleSweepSettlesAnOrphanedCompletion(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")

	orphan := w.taskID
	seedWorklog(t, orphan, 30, false)
	orphanPayment := seedLiveHold(t, orphan, w.requesterID, "authorized", 7675)
	markCompletedAgo(t, orphan, 15, w)

	fresh := seedOpenTaskFor(t, w, "Fresh completion", 60)
	seedWorklog(t, fresh, 30, false)
	freshPayment := seedLiveHold(t, fresh, w.requesterID, "authorized", 7675)
	markCompletedAgo(t, fresh, 0, w)

	sweepUnsettledCompletions(ctx)

	if got := paymentStatusByID(t, orphanPayment); got != paymentStatusCaptured {
		t.Fatalf("orphan: payment status = %q, want captured", got)
	}
	if len(f.captures) != 1 {
		t.Fatalf("orphan: %d captures at Stripe, want 1", len(f.captures))
	}
	var settled *int
	if err := db.QueryRow(ctx, `select settled_total_cents from public.tasks where id=$1::uuid`, orphan).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if settled == nil {
		t.Fatal("orphan: settled_total_cents still NULL — the settlement did not finish")
	}
	rows := auditRows(t, orphan, "PAYMENT_CAPTURED")
	if len(rows) != 1 {
		t.Fatalf("orphan: %d PAYMENT_CAPTURED rows, want 1", len(rows))
	}
	if rows[0].ActorID != systemActorUID {
		t.Errorf("orphan: audit actor = %s, want the system actor (a job acted, not a person)", rows[0].ActorID)
	}
	if got := paymentStatusByID(t, freshPayment); got != "authorized" {
		t.Errorf("fresh: payment status = %q — the sweep touched a completion still in its grace window", got)
	}

	sweepUnsettledCompletions(ctx)
	if len(f.captures) != 1 {
		t.Errorf("second pass: %d captures, want still 1", len(f.captures))
	}
	if got := paymentStatusByID(t, freshPayment); got != "authorized" {
		t.Errorf("second pass touched the fresh task: %q", got)
	}
}

// Older than the window: counted, never captured.
func TestResettleSweepLeavesStaleOrphansAlone(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	payment := seedLiveHold(t, w.taskID, w.requesterID, "authorized", 7675)
	markCompletedAgo(t, w.taskID, 8*24*60, w)

	sweepUnsettledCompletions(context.Background())

	if got := paymentStatusByID(t, payment); got != "authorized" {
		t.Fatalf("stale orphan: payment status = %q — a cron charged a backlog it did not create", got)
	}
	if len(f.captures) != 0 {
		t.Fatalf("stale orphan: %d captures, want 0", len(f.captures))
	}
}

// Shape two: captured, but the settlement never finished. Not redone —
// recorded once and escalated; a second pass does not record it again.
func TestResettleSweepFlagsIncompleteSettlementOnce(t *testing.T) {
	setupStripeWebhookDB(t)
	stubStripe(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	seedLiveHold(t, w.taskID, w.requesterID, paymentStatusCaptured, 7675)
	markCompletedAgo(t, w.taskID, 15, w) // settled_total_cents stays NULL

	sweepUnsettledCompletions(ctx)
	sweepUnsettledCompletions(ctx)

	rows := auditRows(t, w.taskID, "SETTLEMENT_INCOMPLETE")
	if len(rows) != 1 {
		t.Fatalf("%d SETTLEMENT_INCOMPLETE rows after two passes, want exactly 1", len(rows))
	}
	if rows[0].ActorID != systemActorUID {
		t.Errorf("actor = %s, want the system actor", rows[0].ActorID)
	}
	if n := len(auditRows(t, w.taskID, "PAYMENT_CAPTURED")); n != 0 {
		t.Errorf("%d PAYMENT_CAPTURED rows — the sweep re-ran a settlement it cannot resume", n)
	}
}

// The per-task lock: a settlement already running on a task makes a second
// caller do nothing — no capture, no audit row, Busy reported.
func TestSettleCompletedTaskRefusesToOverlap(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	seedLiveHold(t, w.taskID, w.requesterID, "authorized", 7675)
	markCompletedAgo(t, w.taskID, 15, w)

	// Hold the lock the way a running settlement does.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockTaskSettle, w.taskID); err != nil {
		t.Fatal(err)
	}

	out := settleCompletedTask(ctx, w.taskID, orSystemActor(""), 2700, 0)
	if !out.Busy {
		t.Fatalf("second settlement ran while the first held the lock: %+v", out)
	}
	if len(f.captures) != 0 {
		t.Fatalf("%d captures while locked, want 0", len(f.captures))
	}
	if n := len(auditRows(t, w.taskID, "PAYMENT_CAPTURED")); n != 0 {
		t.Fatalf("%d PAYMENT_CAPTURED rows while locked, want 0", n)
	}
	tx.Rollback(ctx)

	out = settleCompletedTask(ctx, w.taskID, orSystemActor(""), 2700, 0)
	if out.Busy || out.Err != nil || len(f.captures) != 1 {
		t.Fatalf("after the lock is released the settlement should run once: %+v captures=%d", out, len(f.captures))
	}
}

// Two completes in the same window produce one completion: the second
// request passes the status check (the row is still open when it looks),
// then finds the row no longer open at the write. One chain, one 400.
func TestCompleteTwiceIsOneCompletion(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)

	runs := 0
	prevDetach := completionDetach
	completionDetach = func(fn func()) { runs++; fn() }
	t.Cleanup(func() { completionDetach = prevDetach })

	body := map[string]any{"completion_photo_url": "https://example.test/done.jpg"}
	// The racing complete lands between this request's status check and its
	// status write: the seam completes the task from "underneath" it.
	prevSeam := completeTaskBeforeWrite
	raced := false
	completeTaskBeforeWrite = func(taskID string) {
		if raced {
			return
		}
		raced = true
		completeTaskBeforeWrite = prevSeam
		completeAs(t, taskID, w.supporterID, body, http.StatusOK)
	}
	t.Cleanup(func() { completeTaskBeforeWrite = prevSeam })

	second := completeAs(t, w.taskID, w.supporterID, body, http.StatusBadRequest)
	if second["error"] != "already closed" {
		t.Fatalf("loser: %v, want already closed", second["error"])
	}
	if runs != 1 {
		t.Fatalf("settlement chains started = %d, want 1", runs)
	}
	if got := taskStatus(t, w.taskID); got != "completed" {
		t.Fatalf("task status = %q", got)
	}
}

// A cancel that lands after a complete does not turn the task into a
// cancelled one (and settle it a second time).
func TestCancelAfterCompleteIsRefusedAtTheWrite(t *testing.T) {
	setupStripeWebhookDB(t)
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	body := map[string]any{"completion_photo_url": "https://example.test/done.jpg"}

	prevSeam := cancelTaskBeforeWrite
	raced := false
	cancelTaskBeforeWrite = func(taskID string) {
		if raced {
			return
		}
		raced = true
		completeAs(t, taskID, w.supporterID, body, http.StatusOK)
	}
	t.Cleanup(func() { cancelTaskBeforeWrite = prevSeam })

	code, out := cancelAs(t, w.taskID, w.requesterID, "changed my mind")
	if code != http.StatusBadRequest || out["error"] != "task already closed" {
		t.Fatalf("cancel after complete: %d %v, want 400 task already closed", code, out)
	}
	if got := taskStatus(t, w.taskID); got != "completed" {
		t.Fatalf("task status = %q — a late cancel overwrote a completion", got)
	}
}

// A completed task with a live hold reports `settling`, not `estimated`.
func TestSettlementStateSettlingWhileHoldIsLive(t *testing.T) {
	setupStripeWebhookDB(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	seedLiveHold(t, w.taskID, w.requesterID, "authorized", 1950)
	if got := settlementState(ctx, w.taskID, "open"); got != "estimated" {
		t.Errorf("open task with a live hold: %q, want estimated", got)
	}
	if got := settlementState(ctx, w.taskID, "completed"); got != "settling" {
		t.Errorf("completed task with a live hold: %q, want settling", got)
	}
}

// Slow is not dead: a captured-but-unsettled task whose settlement still
// holds the lock is not flagged — it is still running.
func TestResettleSweepDoesNotFlagARunningSettlement(t *testing.T) {
	setupStripeWebhookDB(t)
	stubStripe(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	seedLiveHold(t, w.taskID, w.requesterID, paymentStatusCaptured, 7675)
	markCompletedAgo(t, w.taskID, 15, w)

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockTaskSettle, w.taskID); err != nil {
		t.Fatal(err)
	}
	sweepUnsettledCompletions(ctx)
	if n := len(auditRows(t, w.taskID, "SETTLEMENT_INCOMPLETE")); n != 0 {
		t.Fatalf("%d SETTLEMENT_INCOMPLETE rows while the settlement holds the lock, want 0", n)
	}
	tx.Rollback(ctx)

	sweepUnsettledCompletions(ctx)
	if n := len(auditRows(t, w.taskID, "SETTLEMENT_INCOMPLETE")); n != 1 {
		t.Fatalf("%d SETTLEMENT_INCOMPLETE rows once the lock is gone, want 1", n)
	}
}

// In-process exclusion: a second settlement of a task this process already
// holds a lease for is refused by the bookkeeping alone, before any
// connection is opened — and the database agrees, since each lease is its
// own session holding its own transaction-level lock.
func TestSettleCompletedTaskRefusesToOverlapInProcess(t *testing.T) {
	setupStripeWebhookDB(t)
	f := stubStripe(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	seedLiveHold(t, w.taskID, w.requesterID, "authorized", 7675)
	markCompletedAgo(t, w.taskID, 15, w)

	lease, got, err := tryLockTaskSettlement(ctx, w.taskID)
	if err != nil || !got {
		t.Fatalf("first lock: got=%v err=%v", got, err)
	}

	_, again, err := tryLockTaskSettlement(ctx, w.taskID)
	if err != nil || again {
		t.Fatalf("second lock in the same process: got=%v err=%v, want false", again, err)
	}
	if running, err := isTaskSettlementRunning(ctx, w.taskID); err != nil || !running {
		t.Fatalf("isTaskSettlementRunning: %v %v, want true", running, err)
	}
	out := settleCompletedTask(ctx, w.taskID, orSystemActor(""), 2700, 0)
	if !out.Busy || len(f.captures) != 0 {
		t.Fatalf("a settlement ran while this process already held the task: %+v captures=%d", out, len(f.captures))
	}

	unlockTaskSettlement(lease)
	if running, _ := isTaskSettlementRunning(ctx, w.taskID); running {
		t.Fatal("still reported running after unlock")
	}
	out = settleCompletedTask(ctx, w.taskID, orSystemActor(""), 2700, 0)
	if out.Busy || out.Err != nil || len(f.captures) != 1 {
		t.Fatalf("after unlock the settlement should run once: %+v captures=%d", out, len(f.captures))
	}
}

// A lease whose connection broke releases nothing that a newer lease of the
// same task holds: the database dropped the old lock when the connection
// died, and a late release of the old lease must clear neither the newer
// settlement's in-process mark nor its database lock, which lives on the
// newer lease's own connection.
func TestSettleLockStaleLeaseDoesNotReleaseANewerLock(t *testing.T) {
	setupStripeWebhookDB(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")

	stale, got, err := tryLockTaskSettlement(ctx, w.taskID)
	if err != nil || !got {
		t.Fatalf("lock: %v %v", got, err)
	}
	// The connection breaks under the running settlement.
	if err := stale.conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Its in-process half still excludes a local second attempt…
	if _, again, _ := tryLockTaskSettlement(ctx, w.taskID); again {
		t.Fatal("a second local settlement got the task while the first still runs")
	}
	// …and once it finishes, a newer settlement locks on a connection of its own.
	unlockTaskSettlement(stale)
	fresh, got, err := tryLockTaskSettlement(ctx, w.taskID)
	if err != nil || !got {
		t.Fatalf("fresh lock: %v %v", got, err)
	}
	defer unlockTaskSettlement(fresh)
	if stale.conn == fresh.conn {
		t.Fatal("the fresh lease reused the broken connection")
	}
	// A late release of the stale lease must free neither half of the fresh
	// lock: not the in-process mark…
	unlockTaskSettlement(stale)
	settleLock.mu.Lock()
	_, heldNow := settleLock.held[w.taskID]
	settleLock.mu.Unlock()
	if !heldNow {
		t.Fatal("the stale lease's unlock cleared the newer settlement's in-process mark")
	}
	// …nor the database lock, as another session sees it:
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var free bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock($1, hashtext($2))`, advisoryLockTaskSettle, w.taskID).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free {
		t.Fatal("the stale lease's unlock released the newer settlement's database lock")
	}
}

// A task with nothing to settle takes no lock and reports nothing: with the
// lock held elsewhere (or unavailable), a hold-less completion is still the
// quiet no-op it was before the lock existed — never a "capture failed".
func TestSettleCompletedTaskWithoutAHoldTakesNoLock(t *testing.T) {
	setupStripeWebhookDB(t)
	stubStripe(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")
	seedWorklog(t, w.taskID, 30, false)
	markCompletedAgo(t, w.taskID, 15, w) // no payments row

	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockTaskSettle, w.taskID); err != nil {
		t.Fatal(err)
	}

	out := settleCompletedTask(ctx, w.taskID, orSystemActor(""), 2700, 0)
	if out.Busy || out.Attempted || out.Err != nil {
		t.Fatalf("hold-less task: %+v, want a quiet no-op", out)
	}
	if n := len(auditRows(t, w.taskID, "PAYMENT_CAPTURE_FAILED")); n != 0 {
		t.Fatalf("%d PAYMENT_CAPTURE_FAILED rows on a task that owed nothing", n)
	}
	settleLock.mu.Lock()
	_, marked := settleLock.held[w.taskID]
	settleLock.mu.Unlock()
	if marked {
		t.Fatal("a hold-less settlement left an in-process lock mark")
	}
}

// leaseConnCount is how many dedicated lease connections are open right now,
// as the server sees them. Closed connections can linger in pg_stat_activity
// for a moment, so a caller waiting for zero polls (leaseConnsGone).
func leaseConnCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(),
		`select count(*) from pg_stat_activity where application_name = $1`, settleLockAppName).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func leaseConnsGone(t *testing.T) bool {
	t.Helper()
	for i := 0; i < 40; i++ {
		if leaseConnCount(t) == 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// The core property through any pooler: a lock held by ANOTHER session — a
// transaction on a different connection — is seen, with no in-process mark
// involved, and the attempt leaves nothing behind: no mark, no connection.
func TestSettleLockSeesAnotherSessionsLock(t *testing.T) {
	setupStripeWebhookDB(t)
	ctx := context.Background()
	w := seedOpsWorld(t, "open")

	other, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, `select pg_advisory_xact_lock($1, hashtext($2))`, advisoryLockTaskSettle, w.taskID); err != nil {
		t.Fatal(err)
	}
	lease, got, err := tryLockTaskSettlement(ctx, w.taskID)
	if err != nil || got {
		t.Fatalf("lock held elsewhere: got=%v err=%v, want false, nil", got, err)
	}
	if lease.conn != nil {
		t.Fatal("a refused lock returned a connection")
	}
	settleLock.mu.Lock()
	_, marked := settleLock.held[w.taskID]
	settleLock.mu.Unlock()
	if marked {
		t.Fatal("a refused lock left a reservation behind")
	}
	if !leaseConnsGone(t) {
		t.Fatalf("a refused lock left %d lease connection(s) open", leaseConnCount(t))
	}
	if running, err := isTaskSettlementRunning(ctx, w.taskID); err != nil || !running {
		t.Fatalf("probe while another session holds it: %v %v, want true", running, err)
	}

	// Released elsewhere → taken here, on a connection of its own, which the
	// release closes.
	if err := other.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	lease, got, err = tryLockTaskSettlement(ctx, w.taskID)
	if err != nil || !got {
		t.Fatalf("lock after release elsewhere: got=%v err=%v", got, err)
	}
	if n := leaseConnCount(t); n != 1 {
		t.Fatalf("%d lease connections while one lease is held, want 1", n)
	}
	unlockTaskSettlement(lease)
	if !leaseConnsGone(t) {
		t.Fatalf("release left %d lease connection(s) open", leaseConnCount(t))
	}
	if running, err := isTaskSettlementRunning(ctx, w.taskID); err != nil || running {
		t.Fatalf("probe after release: %v %v, want false", running, err)
	}
}

// Past the cap a settlement is not started — no connection, no error, no
// capture-failed report — and the task is free again once a slot is.
func TestSettleLockCapLeavesTheTaskToTheSweep(t *testing.T) {
	setupStripeWebhookDB(t)
	t.Setenv("SETTLE_LOCK_MAX_LEASES", "1")
	ctx := context.Background()

	a, got, err := tryLockTaskSettlement(ctx, "cap-task-a")
	if err != nil || !got {
		t.Fatalf("first lease: got=%v err=%v", got, err)
	}
	before := leaseConnCount(t)
	_, got, err = tryLockTaskSettlement(ctx, "cap-task-b")
	if err != nil || got {
		t.Fatalf("second lease at cap 1: got=%v err=%v, want false, nil", got, err)
	}
	settleLock.mu.Lock()
	_, marked := settleLock.held["cap-task-b"]
	settleLock.mu.Unlock()
	if marked {
		t.Fatal("a cap-refused task was left reserved")
	}
	if n := leaseConnCount(t); n != before {
		t.Fatalf("a cap-refused attempt opened a connection: %d → %d", before, n)
	}
	// The probe still answers truthfully for the refused task: nobody holds it.
	if running, err := isTaskSettlementRunning(ctx, "cap-task-b"); err != nil || running {
		t.Fatalf("probe of a cap-refused task: %v %v, want false", running, err)
	}

	unlockTaskSettlement(a)
	b, got, err := tryLockTaskSettlement(ctx, "cap-task-b")
	if err != nil || !got {
		t.Fatalf("lease once a slot is free: got=%v err=%v", got, err)
	}
	unlockTaskSettlement(b)
	if !leaseConnsGone(t) {
		t.Fatalf("%d lease connection(s) left open", leaseConnCount(t))
	}
}
