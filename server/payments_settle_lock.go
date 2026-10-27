package main

// The per-task settlement lock: one connection, one transaction, one
// transaction-level advisory lock, per settlement.
//
// WHY A TRANSACTION-LEVEL LOCK (2026-10-27). The first version of this file
// held SESSION-level advisory locks (pg_try_advisory_lock / pg_advisory_unlock)
// on one dedicated connection shared by every settlement in the process. Its
// first live settlement on production printed
//
//	[payments][settle][ERROR] unlock found no lock held on the live connection
//
// because production's SUPABASE_DB_URL is Supabase's TRANSACTION pooler
// (port 6543): each statement outside a transaction may be served by a
// different backend session, so the session that took the lock was not the
// session asked to release it — and, worse, was not the session any later
// settlement would be checked against. Session-level locks are simply not a
// thing through a transaction pooler; Supabase's docs say so. The in-process
// half (the `held` map) kept the money right, but the cross-process half the
// design promised did not exist. See D-06, addendum 2026-10-27.
//
// A transaction pins ONE backend for its whole life in every pooler mode. So
// a lock that lives inside an open transaction means the same thing on 6543,
// on 5432 and on a direct connection, and it needs no assumption about the
// environment to be true. That is the whole design:
//
//	tryLock   reserve the task in `held`, then (outside the mutex) open a
//	          dedicated connection, BEGIN, and
//	          `select pg_try_advisory_xact_lock(k, hashtext(task))`. true →
//	          the lease is that connection and its open transaction, held for
//	          the settlement. false → ROLLBACK, close, "busy".
//	unlock    ROLLBACK (releases the lock; nothing was written on this
//	          transaction) and close the connection; then clear the mark.
//	probe     isTaskSettlementRunning: a pool transaction tries the same lock
//	          and rolls back at once. Sub-second, so the pool is fine for it —
//	          the reason settlements do NOT use the pool (review r3) is the
//	          15 s they would hold a pool connection, not the lock itself.
//
// Why not a pool connection for the settlement: settleCompletedTask runs
// ~15 s of Stripe calls and pool writes. A lock held on a pool connection
// for that long is a connection the chain's own writes cannot use; with
// DB_MAX_CONNS=3 (staging) three overlapping settlements would each hold one
// and each wait for one, forever, with every HTTP handler behind them.
//
// One connection PER settlement rather than one shared: a transaction is per
// connection, and transaction-level locks release only when their
// transaction ends, so two settlements cannot share a transaction without
// sharing a lifetime.
//
// THE BUDGET (review r1 of this rewrite, MAJOR). Through the transaction
// pooler an open transaction pins a BACKEND, not just a client socket, so
// every lease is one of the pooler's ~15 backends for ~15 s — the same
// resource the settlement's own writes and every HTTP handler need. A cap
// sized to DB_MAX_CONNS (8 on production) would let two overlapping
// processes pin 16 and recreate, in the pooler, exactly the circular wait
// review r3 kept out of the Go pool. So the cap is SMALL and INDEPENDENT of
// the pool size: settleLockMaxLeases, default 2, SETTLE_LOCK_MAX_LEASES to
// change it. A process therefore holds at most pgx pool + sqldb + 2
// connections, and at most 2 pinned backends. A settlement past the cap is
// not started and is left to the five-minute sweep (payments_resettle.go),
// never reported as a capture failure about money that was not attempted;
// the log line names the leases in the way and how long each has run, so a
// chain that hung (the chain has no deadline, by D-06) is visible as the
// slot it is holding.
//
// `held` remains, for two reasons that are not the old one. It is the
// reservation: the in-process check, the cap, and the "already settling
// here" answer all come from it without a round-trip. And it is what a late
// or duplicate unlock is checked against, so releasing a lease twice, or
// after a newer settlement of the same task has its own lease, touches
// nothing that is not that lease's own. (The old reason — session locks
// being re-entrant within one connection — no longer applies: every
// settlement is its own session.)
//
// The mutex covers bookkeeping only. Connect, BEGIN, the lock query, the
// release and the ops email all run OUTSIDE it (review r1, m2): a database
// brown-out that hangs one connect to its 10 s timeout must not queue every
// other settlement's lock step, nor a finished settlement's release, behind
// it. The reservation in `held` is what keeps two local attempts at one task
// apart while the first is still connecting.
//
// A lost lock is detected LATE. If a lease's connection breaks mid-settlement
// (or the server ends its transaction — an idle_in_transaction timeout, a
// failover), the database released the lock at that moment and the
// settlement runs on without the cross-process exclusion; nothing here
// notices until the release, where ROLLBACK fails or the connection is found
// closed. Then it is logged at ERROR and ops are emailed naming the task
// (rate-limited), so its payment and payout rows get checked against Stripe
// for duplicates. The database rules that actually hold the money — one
// completion (`WHERE status='open'`), one transfer per charge
// (`payouts.payment_id UNIQUE`) — do not depend on this lock; it is the
// layer above them.
//
// Known, accepted: the probe takes the lock for one round-trip, so a
// settlement that tries the same task in that instant is told "busy" and
// deferred to the sweep (review r1, m3). The probe runs only from the sweep,
// on captured-but-unsettled tasks older than ten minutes.
//
// Lock space is shared with any other transaction-level advisory lock on the
// same key, so a test (or any caller) holding pg_advisory_xact_lock(
// advisoryLockTaskSettle, hashtext(task)) on another connection blocks this
// just the same.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// settleLockAppName is the application_name of every lease connection, so
// `pg_stat_activity` tells a lease from a pool connection — and a leak from
// a lease (a test counts them).
const settleLockAppName = "hora-settle-lock"

var settleLock struct {
	mu   sync.Mutex
	held map[string]settleLease // tasks this process is settling right now → their lease (conn nil while connecting)
	// lastLostMail: a flapping database must not mail ops on every lost lock.
	lastLostMail time.Time
}

// settleLease is one taken lock: the dedicated connection and the open
// transaction that holds it. The zero lease (conn == nil) holds nothing and
// releases nothing. `since` is when the reservation was made.
type settleLease struct {
	taskID string
	conn   *pgx.Conn
	tx     pgx.Tx
	since  time.Time
}

// errSettleLockUnavailable: the lock could not be ASKED for — the
// connection could not be made or the query failed. Distinct from "held by
// someone else", which is not an error (settlementOutcome.Busy).
var errSettleLockUnavailable = errors.New("payments: settlement lock unavailable")

// settleLockMaxLeases caps the dedicated lock connections — pinned pooler
// backends — a process holds at once. See THE BUDGET above. Default 2;
// SETTLE_LOCK_MAX_LEASES overrides.
func settleLockMaxLeases() int {
	raw := strings.TrimSpace(os.Getenv("SETTLE_LOCK_MAX_LEASES"))
	if raw == "" {
		return 2
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("[payments][settle] SETTLE_LOCK_MAX_LEASES=%q is not a positive integer; using 2", raw)
		return 2
	}
	return n
}

// reserveTaskSettlement claims the task in `held` for this process without
// opening anything. true: reserved — the caller must either promote the
// reservation to a lease or release it. false: already held here, or at
// the cap.
func reserveTaskSettlement(taskID string) bool {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	if _, busy := settleLock.held[taskID]; busy {
		return false // this process is already settling it
	}
	if n := len(settleLock.held); n >= settleLockMaxLeases() {
		ages := make([]string, 0, n)
		for id, l := range settleLock.held {
			ages = append(ages, fmt.Sprintf("%s (%s)", id, time.Since(l.since).Round(time.Second)))
		}
		sort.Strings(ages)
		log.Printf("[payments][settle] task=%s: %d settlements already hold lock connections (cap %d) — not started, left to the sweep: %s",
			taskID, n, settleLockMaxLeases(), strings.Join(ages, ", "))
		return false
	}
	if settleLock.held == nil {
		settleLock.held = map[string]settleLease{}
	}
	settleLock.held[taskID] = settleLease{taskID: taskID, since: time.Now()}
	return true
}

// releaseReservation drops a reservation that never became a lease.
func releaseReservation(taskID string) {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	if cur, ok := settleLock.held[taskID]; ok && cur.conn == nil {
		delete(settleLock.held, taskID)
	}
}

// tryLockTaskSettlement takes the per-task settlement lock without waiting.
// (lease, true, nil): taken — the caller must unlockTaskSettlement(lease).
// (_, false, nil): held by another settlement of this task, here or in
// another process — or this process is at its lease cap, or the caller's
// context ended before the lock could be asked for; do nothing, the sweep
// will come back to the task.
// (_, false, err): the lock could not be asked for — an infrastructure
// error, reported like any other settlement failure, never swallowed.
func tryLockTaskSettlement(ctx context.Context, taskID string) (settleLease, bool, error) {
	if !reserveTaskSettlement(taskID) {
		return settleLease{}, false, nil
	}
	l, got, err := openSettlementLease(ctx, taskID)
	if !got {
		releaseReservation(taskID)
		return settleLease{}, false, err
	}
	settleLock.mu.Lock()
	if settleLock.held == nil { // resetSettleLockForTests ran meanwhile
		settleLock.held = map[string]settleLease{}
	}
	settleLock.held[taskID] = l
	settleLock.mu.Unlock()
	return l, true, nil
}

// openSettlementLease does the I/O half of tryLockTaskSettlement, outside
// the mutex: connect, BEGIN, try the lock. Every failure path closes what it
// opened. A caller whose context ended is not an infrastructure failure
// (review r1, m4): it is reported as "not now", never as a capture failure.
func openSettlementLease(ctx context.Context, taskID string) (settleLease, bool, error) {
	if db == nil {
		return settleLease{}, false, fmt.Errorf("%w: no database", errSettleLockUnavailable)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cfg := db.Config().ConnConfig.Copy()
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = settleLockAppName
	conn, err := pgx.ConnectConfig(cctx, cfg)
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("[payments][settle] task=%s: caller's context ended before the lock was taken — not started, left to the sweep", taskID)
			return settleLease{}, false, nil
		}
		// The pgx error names host, user and database; that belongs in the
		// log, not in an audit row or an ops email (reportCaptureFailure
		// writes out.Err into both).
		log.Printf("[payments][settle] task=%s: lock connection: connect: %v", taskID, err)
		return settleLease{}, false, fmt.Errorf("%w: connect failed", errSettleLockUnavailable)
	}
	tx, err := conn.Begin(cctx)
	if err != nil {
		_ = conn.Close(context.Background())
		if ctx.Err() != nil {
			log.Printf("[payments][settle] task=%s: caller's context ended before the lock was taken — not started, left to the sweep", taskID)
			return settleLease{}, false, nil
		}
		log.Printf("[payments][settle] task=%s: lock connection: begin: %v", taskID, err)
		return settleLease{}, false, fmt.Errorf("%w: begin failed", errSettleLockUnavailable)
	}
	var got bool
	if err := tx.QueryRow(cctx, `select pg_try_advisory_xact_lock($1, hashtext($2))`,
		advisoryLockTaskSettle, taskID).Scan(&got); err != nil {
		_ = tx.Rollback(context.Background())
		_ = conn.Close(context.Background())
		if ctx.Err() != nil {
			log.Printf("[payments][settle] task=%s: caller's context ended before the lock was taken — not started, left to the sweep", taskID)
			return settleLease{}, false, nil
		}
		log.Printf("[payments][settle] task=%s: lock query: %v", taskID, err)
		return settleLease{}, false, fmt.Errorf("%w: lock query failed", errSettleLockUnavailable)
	}
	if !got {
		_ = tx.Rollback(context.Background())
		_ = conn.Close(context.Background())
		return settleLease{}, false, nil
	}
	return settleLease{taskID: taskID, conn: conn, tx: tx, since: time.Now()}, true, nil
}

// unlockTaskSettlement releases a lease taken by tryLockTaskSettlement:
// ROLLBACK ends the transaction, which releases the lock, and the connection
// is closed; then the mark is cleared. On context.Background(): the caller's
// context may be the one that just expired, and a lock that stays held
// because its release was cancelled is a task nobody can settle until the
// process restarts.
//
// Only this lease's own mark is cleared: a lease released twice, or late,
// after a newer settlement of the same task took its own lease, must not
// clear that newer mark — and holds no database lock of its own any more, so
// there is nothing of the newer settlement's it could release either.
//
// The I/O runs outside the mutex. Between the ROLLBACK and the mark's
// removal a concurrent attempt at the same task is told "busy" by the mark
// and deferred to the sweep — the safe direction.
func unlockTaskSettlement(l settleLease) {
	if l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lost := ""
	switch {
	case l.conn.IsClosed():
		// The connection broke under the running settlement; the database
		// released the lock when it did.
		lost = "connection closed during the settlement"
	default:
		if err := l.tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			lost = fmt.Sprintf("release failed: %v", err)
		}
		_ = l.conn.Close(ctx)
	}

	settleLock.mu.Lock()
	current := false
	if cur, ok := settleLock.held[l.taskID]; ok && cur.conn == l.conn {
		delete(settleLock.held, l.taskID)
		current = true
	}
	mail := false
	if lost != "" && current {
		// Reported once, by the release that still owned the mark — a late
		// duplicate says nothing new. The email is rate-limited; the log is not.
		if time.Since(settleLock.lastLostMail) >= 10*time.Minute {
			settleLock.lastLostMail = time.Now()
			mail = true
		}
	}
	settleLock.mu.Unlock()

	if lost != "" && current {
		settleLockLost(l.taskID, lost, mail)
	}
}

// settleLockLost says, loudly, that a settlement ran part of its course
// without the cross-process lock it was supposed to hold. Called outside
// the mutex.
func settleLockLost(taskID, reason string, mail bool) {
	log.Printf("[payments][settle][ERROR] task=%s: lock lost during the settlement (%s) — "+
		"another process could have settled it at the same time; check its payment and payout rows against Stripe", taskID, reason)
	if !mail {
		return
	}
	emailOpsAdmins("[HO:RA] Settlement lock lost mid-settlement",
		fmt.Sprintf("<p>The database connection holding the per-task settlement lock for task <code>%s</code> broke while its settlement was running (%s). "+
			"The settlement continued, but another process could have settled the same task at the same time. "+
			"Check the task's payment and payout rows against Stripe for duplicates.</p>", taskID, reason))
}

// isTaskSettlementRunning reports whether some settlement of the task holds
// the lock right now — here (held, or reserved and connecting) or in another
// process (database) — without taking it for longer than one round-trip.
// Used by the sweep to tell "the chain died" from "the chain is slow". The
// probe runs on a POOL transaction: sub-second, rolled back at once, so it
// never ties a pool connection up the way a settlement would.
func isTaskSettlementRunning(ctx context.Context, taskID string) (bool, error) {
	settleLock.mu.Lock()
	_, local := settleLock.held[taskID]
	settleLock.mu.Unlock()
	if local {
		return true, nil
	}
	if db == nil {
		return false, fmt.Errorf("%w: no database", errSettleLockUnavailable)
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.Begin(pctx)
	if err != nil {
		return false, fmt.Errorf("%w: probe begin: %v", errSettleLockUnavailable, err)
	}
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx) // the rollback IS the release
	}()
	var free bool
	if err := tx.QueryRow(pctx, `select pg_try_advisory_xact_lock($1, hashtext($2))`,
		advisoryLockTaskSettle, taskID).Scan(&free); err != nil {
		return false, fmt.Errorf("%w: probe: %v", errSettleLockUnavailable, err)
	}
	return !free, nil
}

// resetSettleLockForTests releases every lease this process holds and drops
// the held set, so a test fixture that swaps databases does not leave
// connections to the old one (admin_task_ops_test.go).
func resetSettleLockForTests() {
	settleLock.mu.Lock()
	leases := make([]settleLease, 0, len(settleLock.held))
	for _, l := range settleLock.held {
		leases = append(leases, l)
	}
	settleLock.held = nil
	settleLock.mu.Unlock()
	for _, l := range leases {
		if l.conn != nil && !l.conn.IsClosed() {
			_ = l.tx.Rollback(context.Background())
			_ = l.conn.Close(context.Background())
		}
	}
}
