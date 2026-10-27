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
//	tryLock   open a dedicated connection (outside the pool), BEGIN, and
//	          `select pg_try_advisory_xact_lock(k, hashtext(task))`. true →
//	          the lease is that connection and its open transaction, held for
//	          the settlement. false → ROLLBACK, close, "busy".
//	unlock    ROLLBACK (releases the lock; nothing was written on this
//	          transaction) and close the connection.
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
// sharing a lifetime. The cost is a connection per concurrent settlement,
// capped at settleLockMaxLeases() so a burst cannot exhaust the server's
// client limit — a settlement past the cap is not started and is left to the
// five-minute sweep (payments_resettle.go), never reported as a capture
// failure about money that was not attempted.
//
// `held` remains, for two reasons that are not the old one. It makes
// isTaskSettlementRunning free for the common case (this process holds it),
// and it is what a late or duplicate unlock is checked against, so releasing
// a lease twice, or after a newer settlement of the same task has its own
// lease, touches nothing that is not that lease's own. (The old reason —
// session locks being re-entrant within one connection — no longer applies:
// every settlement is its own session.)
//
// If a lease's connection breaks mid-settlement the database has already
// released its lock; nothing can be done about the exclusion that was lost,
// but it must not be lost QUIETLY: unlock finds the connection closed, logs
// at ERROR and emails ops naming the task (rate-limited), so the task's
// payment and payout rows get checked against Stripe for duplicates. The
// database rules that actually hold the money — one completion
// (`WHERE status='open'`), one transfer per charge (`payouts.payment_id
// UNIQUE`) — do not depend on this lock; it is the layer above them.
//
// The mutex is held across the connect + lock round-trip (and the release),
// as it was before: it serialises lock bookkeeping only, never the
// settlement. A connect that hangs to its 10 s timeout stalls other
// settlements' lock step for that long — accepted, and unchanged.
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
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var settleLock struct {
	mu   sync.Mutex
	held map[string]settleLease // tasks this process is settling right now → their lease
	// lastLostMail: a flapping database must not mail ops on every lost lock.
	lastLostMail time.Time
}

// settleLease is one taken lock: the dedicated connection and the open
// transaction that holds it. The zero lease (conn == nil) holds nothing and
// releases nothing.
type settleLease struct {
	taskID string
	conn   *pgx.Conn
	tx     pgx.Tx
}

// errSettleLockUnavailable: the lock could not be ASKED for — the
// connection could not be made or the query failed. Distinct from "held by
// someone else", which is not an error (settlementOutcome.Busy).
var errSettleLockUnavailable = errors.New("payments: settlement lock unavailable")

// settleLockMaxLeases caps the dedicated lock connections a process holds at
// once. Beyond it a settlement is deferred to the sweep rather than opening
// another connection against the server's client limit. DB_MAX_CONNS is the
// number each pool is sized to, so this is "as many again".
func settleLockMaxLeases() int {
	if n := dbMaxConns(); n > 0 {
		return n
	}
	return 1
}

// tryLockTaskSettlement takes the per-task settlement lock without waiting.
// (lease, true, nil): taken — the caller must unlockTaskSettlement(lease).
// (_, false, nil): held by another settlement of this task, here or in
// another process — or this process is at its lease cap; do nothing, the
// sweep will come back to the task.
// (_, false, err): the lock could not be asked for — an infrastructure
// error, reported like any other settlement failure, never swallowed.
func tryLockTaskSettlement(ctx context.Context, taskID string) (settleLease, bool, error) {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	if _, busy := settleLock.held[taskID]; busy {
		return settleLease{}, false, nil // this process is already settling it
	}
	if n := len(settleLock.held); n >= settleLockMaxLeases() {
		log.Printf("[payments][settle] task=%s: %d settlements already hold lock connections (cap %d) — not started, left to the sweep",
			taskID, n, settleLockMaxLeases())
		return settleLease{}, false, nil
	}
	if db == nil {
		return settleLease{}, false, fmt.Errorf("%w: no database", errSettleLockUnavailable)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, db.Config().ConnConfig.Copy())
	if err != nil {
		// The pgx error names host, user and database; that belongs in the
		// log, not in an audit row or an ops email (reportCaptureFailure
		// writes out.Err into both).
		log.Printf("[payments][settle] task=%s: lock connection: connect: %v", taskID, err)
		return settleLease{}, false, fmt.Errorf("%w: connect failed", errSettleLockUnavailable)
	}
	tx, err := conn.Begin(cctx)
	if err != nil {
		_ = conn.Close(context.Background())
		log.Printf("[payments][settle] task=%s: lock connection: begin: %v", taskID, err)
		return settleLease{}, false, fmt.Errorf("%w: begin failed", errSettleLockUnavailable)
	}
	var got bool
	if err := tx.QueryRow(cctx, `select pg_try_advisory_xact_lock($1, hashtext($2))`,
		advisoryLockTaskSettle, taskID).Scan(&got); err != nil {
		_ = tx.Rollback(context.Background())
		_ = conn.Close(context.Background())
		log.Printf("[payments][settle] task=%s: lock query: %v", taskID, err)
		return settleLease{}, false, fmt.Errorf("%w: lock query failed", errSettleLockUnavailable)
	}
	if !got {
		_ = tx.Rollback(context.Background())
		_ = conn.Close(context.Background())
		return settleLease{}, false, nil
	}
	l := settleLease{taskID: taskID, conn: conn, tx: tx}
	if settleLock.held == nil {
		settleLock.held = map[string]settleLease{}
	}
	settleLock.held[taskID] = l
	return l, true, nil
}

// unlockTaskSettlement releases a lease taken by tryLockTaskSettlement:
// ROLLBACK ends the transaction, which releases the lock, and the connection
// is closed. On context.Background(): the caller's context may be the one
// that just expired, and a lock that stays held because its release was
// cancelled is a task nobody can settle until the process restarts.
//
// Only this lease's own mark is cleared: a lease released twice, or late,
// after a newer settlement of the same task took its own lease, must not
// clear that newer mark — and holds no database lock of its own any more, so
// there is nothing of the newer settlement's it could release either.
func unlockTaskSettlement(l settleLease) {
	if l.conn == nil {
		return
	}
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	current := false
	if cur, ok := settleLock.held[l.taskID]; ok && cur.conn == l.conn {
		delete(settleLock.held, l.taskID)
		current = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if l.conn.IsClosed() {
		// The connection broke under the running settlement; the database
		// released the lock when it did. Reported once, by the release that
		// still owned the mark — a late duplicate says nothing new.
		if current {
			settleLockLost(l.taskID, "connection closed during the settlement")
		}
		return
	}
	if err := l.tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		if current {
			settleLockLost(l.taskID, fmt.Sprintf("release failed: %v", err))
		}
	}
	_ = l.conn.Close(ctx)
}

// settleLockLost says, loudly, that a settlement ran part of its course
// without the cross-process lock it was supposed to hold. Caller holds
// settleLock.mu.
func settleLockLost(taskID, reason string) {
	log.Printf("[payments][settle][ERROR] task=%s: lock lost during the settlement (%s) — "+
		"another process could have settled it at the same time; check its payment and payout rows against Stripe", taskID, reason)
	if time.Since(settleLock.lastLostMail) < 10*time.Minute {
		return
	}
	settleLock.lastLostMail = time.Now()
	emailOpsAdmins("[HO:RA] Settlement lock lost mid-settlement",
		fmt.Sprintf("<p>The database connection holding the per-task settlement lock for task <code>%s</code> broke while its settlement was running (%s). "+
			"The settlement continued, but another process could have settled the same task at the same time. "+
			"Check the task's payment and payout rows against Stripe for duplicates.</p>", taskID, reason))
}

// isTaskSettlementRunning reports whether some settlement of the task holds
// the lock right now — here (held) or in another process (database) —
// without taking it for longer than one round-trip. Used by the sweep to
// tell "the chain died" from "the chain is slow". The probe runs on a POOL
// transaction: sub-second, rolled back at once, so it never ties a pool
// connection up the way a settlement would.
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
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: probe begin: %v", errSettleLockUnavailable, err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck — the rollback IS the release
	var free bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock($1, hashtext($2))`,
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
	defer settleLock.mu.Unlock()
	for _, l := range settleLock.held {
		if l.conn != nil && !l.conn.IsClosed() {
			_ = l.tx.Rollback(context.Background())
			_ = l.conn.Close(context.Background())
		}
	}
	settleLock.held = nil
}
