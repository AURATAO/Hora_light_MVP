package main

// The per-task settlement lock, and the one connection it lives on.
//
// Why not a pool connection: settleCompletedTask runs ~15 s of Stripe calls
// and pool writes. A lock held on a pool connection for that long is a
// connection the chain's own writes cannot use; with DB_MAX_CONNS=3
// (staging) three overlapping settlements would each hold one and each wait
// for one, forever, with every HTTP handler behind them. (Review r3.)
//
// So the lock lives on ONE dedicated connection, outside the pool, opened
// lazily from the pool's own config and shared by every settlement in the
// process. Session-level advisory locks (pg_try_advisory_lock /
// pg_advisory_unlock) are per connection, so all of them ride this one;
// the Go mutex serialises the lock/unlock round-trips only, never the
// settlement itself. If the process dies the connection dies and Postgres
// drops every lock it held — which is exactly when the sweep is meant to
// take over. If the connection breaks, the next call reconnects (the locks
// it held are gone with it; whoever held them is told through the unlock
// error and logs it).
//
// Budget against DB_MAX_CONNS: one connection, in addition to the pool.
//
// In-process exclusion is NOT the database's job here: session-level
// advisory locks are re-entrant within a session, so two goroutines on the
// shared connection would both be told "taken". settleLock.held is the
// in-process half; the database lock is the cross-process half.
//
// Lock space is shared with transaction-level advisory locks, so a test (or
// any caller) holding pg_advisory_xact_lock(advisoryLockTaskSettle, hashtext(task))
// blocks this just the same.

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
	conn *pgx.Conn
	// held: the tasks THIS process has locked. Session-level advisory locks
	// are re-entrant within one session — a second pg_try_advisory_lock for
	// the same key on the same connection returns true and stacks — so the
	// database alone cannot tell two goroutines of this process apart. This
	// set does; the database lock is what keeps other processes out.
	held map[string]bool
}

// errSettleLockUnavailable: the lock could not be ASKED for — the
// connection could not be made or the query failed. Distinct from "held by
// someone else", which is not an error (settlementOutcome.Busy).
var errSettleLockUnavailable = errors.New("payments: settlement lock unavailable")

// settleLockConn returns the shared lock connection, opening it on first
// use or after a break. Caller holds settleLock.mu.
func settleLockConn(ctx context.Context) (*pgx.Conn, error) {
	if settleLock.conn != nil && !settleLock.conn.IsClosed() {
		return settleLock.conn, nil
	}
	settleLock.conn = nil
	if db == nil {
		return nil, fmt.Errorf("%w: no database", errSettleLockUnavailable)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(cctx, db.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("%w: connect: %v", errSettleLockUnavailable, err)
	}
	settleLock.conn = conn
	return conn, nil
}

// tryLockTaskSettlement takes the per-task settlement lock without waiting.
// (true, nil): taken — the caller must call unlockTaskSettlement.
// (false, nil): held by another settlement of this task; do nothing.
// (false, err): the lock could not be asked for — an infrastructure error,
// to be reported like any other settlement failure, never swallowed.
func tryLockTaskSettlement(ctx context.Context, taskID string) (bool, error) {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	if settleLock.held[taskID] {
		return false, nil // this process is already settling it
	}
	conn, err := settleLockConn(ctx)
	if err != nil {
		return false, err
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var got bool
	if err := conn.QueryRow(qctx, `select pg_try_advisory_lock($1, hashtext($2))`,
		advisoryLockTaskSettle, taskID).Scan(&got); err != nil {
		// A broken connection has lost every lock it held; drop it so the
		// next caller reconnects rather than failing the same way.
		_ = conn.Close(context.Background())
		settleLock.conn = nil
		settleLock.held = nil
		return false, fmt.Errorf("%w: %v", errSettleLockUnavailable, err)
	}
	if got {
		if settleLock.held == nil {
			settleLock.held = map[string]bool{}
		}
		settleLock.held[taskID] = true
	}
	return got, nil
}

// unlockTaskSettlement releases the lock taken by tryLockTaskSettlement.
// On context.Background(): the caller's context may be the one that just
// expired, and a lock that stays held because its release was cancelled is
// a task nobody can settle until the process restarts.
func unlockTaskSettlement(taskID string) {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	delete(settleLock.held, taskID)
	if settleLock.conn == nil || settleLock.conn.IsClosed() {
		// The connection is gone and so is the lock. Nothing to release.
		settleLock.conn = nil
		settleLock.held = nil
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var released bool
	if err := settleLock.conn.QueryRow(ctx, `select pg_advisory_unlock($1, hashtext($2))`,
		advisoryLockTaskSettle, taskID).Scan(&released); err != nil {
		log.Printf("[payments][settle] task=%s: unlock: %v (connection dropped; its locks are released with it)", taskID, err)
		_ = settleLock.conn.Close(context.Background())
		settleLock.conn = nil
		settleLock.held = nil
		return
	}
	if !released {
		log.Printf("[payments][settle] task=%s: unlock found no lock held — the connection was remade mid-settlement", taskID)
	}
}

// isTaskSettlementRunning reports whether some settlement of the task holds
// the lock right now, without taking it for longer than one round-trip.
// Used by the sweep to tell "the chain died" from "the chain is slow".
func isTaskSettlementRunning(ctx context.Context, taskID string) (bool, error) {
	got, err := tryLockTaskSettlement(ctx, taskID)
	if err != nil {
		return false, err
	}
	if got {
		unlockTaskSettlement(taskID)
		return false, nil
	}
	return true, nil
}
