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
// pg_advisory_unlock) are per connection, so all of them ride this one; the
// Go mutex serialises the lock/unlock round-trips only, never the
// settlement itself. If the process dies the connection dies and Postgres
// drops every lock it held — which is exactly when the sweep is meant to
// take over.
//
// Two halves, because the database alone is not enough (review r4):
//
//	in-process    session-level advisory locks are RE-ENTRANT within one
//	              session — a second pg_try_advisory_lock for the same key on
//	              the same connection returns true and stacks — so two
//	              goroutines of this process asking the database would both
//	              be told "yours". `held` is the in-process half: a task in
//	              it is being settled here, whatever the database says.
//	cross-process the database lock. Another process (a deploy overlap, a
//	              second instance) has its own connection and is told false.
//
// A lease, not a boolean: every (re)connection has a generation, and a lock
// carries the generation it was taken under. If the connection broke and was
// remade while a settlement ran, that settlement's lock went with the old
// connection — the database no longer excludes anyone from its task — and
// its release must not touch a lock some newer settlement took on the new
// connection. unlock checks the generation: a stale lease is reported, loudly
// (log + ops email naming the task), and released from `held` only.
//
// Budget against DB_MAX_CONNS: one connection, in addition to the pgx pool
// and the stdlib `sqldb` handle.
//
// Lock space is shared with transaction-level advisory locks, so a test (or
// any caller) holding pg_advisory_xact_lock(advisoryLockTaskSettle, hashtext(task))
// on another connection blocks this just the same.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var settleLock struct {
	mu   sync.Mutex
	conn *pgx.Conn
	gen  uint64            // bumped on every (re)connect
	held map[string]uint64 // tasks this process is settling right now → lease generation
	// lastDropMail: a flapping connection must not mail ops on every flap.
	lastDropMail time.Time
}

// settleLease is one taken lock: which task, under which connection.
type settleLease struct {
	taskID string
	gen    uint64
}

// errSettleLockUnavailable: the lock could not be ASKED for — the
// connection could not be made or the query failed twice. Distinct from
// "held by someone else", which is not an error (settlementOutcome.Busy).
var errSettleLockUnavailable = errors.New("payments: settlement lock unavailable")

// settleLockDropConn closes the shared connection after an error on it and
// says, loudly, which in-flight settlements just lost their database lock.
// Caller holds settleLock.mu.
func settleLockDropConn(reason string) {
	if settleLock.conn != nil {
		_ = settleLock.conn.Close(context.Background())
	}
	settleLock.conn = nil
	if len(settleLock.held) > 0 {
		ids := make([]string, 0, len(settleLock.held))
		for id := range settleLock.held {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		log.Printf("[payments][settle][ERROR] lock connection dropped (%s) while %d settlement(s) were running: %s — "+
			"their cross-process exclusion is gone until they finish", reason, len(ids), strings.Join(ids, ", "))
		if time.Since(settleLock.lastDropMail) < 10*time.Minute {
			return
		}
		settleLock.lastDropMail = time.Now()
		emailOpsAdmins("[HO:RA] Settlement lock connection dropped mid-settlement",
			fmt.Sprintf("<p>The database connection that holds per-task settlement locks dropped while %d settlement(s) were running. "+
				"They continue, but another process could now settle the same task(s) at the same time. "+
				"Once they finish, check these tasks' payment and payout rows against Stripe for duplicates:</p><p><code>%s</code></p>",
				len(ids), strings.Join(ids, "<br>")))
	}
	// `held` is kept: those settlements are still running here, and the
	// in-process half must go on excluding a second local attempt.
}

// settleLockConn returns the shared lock connection, opening it on first
// use or after a break (bumping the generation). Caller holds settleLock.mu.
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
		// The pgx error names host, user and database; that belongs in the
		// log, not in an audit row or an ops email (reportCaptureFailure
		// writes out.Err into both).
		log.Printf("[payments][settle] lock connection: connect: %v", err)
		return nil, fmt.Errorf("%w: connect failed", errSettleLockUnavailable)
	}
	settleLock.conn = conn
	settleLock.gen++
	return conn, nil
}

// tryLockTaskSettlement takes the per-task settlement lock without waiting.
// (lease, true, nil): taken — the caller must unlockTaskSettlement(lease).
// (_, false, nil): held by another settlement of this task; do nothing.
// (_, false, err): the lock could not be asked for — an infrastructure
// error, reported like any other settlement failure, never swallowed.
func tryLockTaskSettlement(ctx context.Context, taskID string) (settleLease, bool, error) {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	if _, busy := settleLock.held[taskID]; busy {
		return settleLease{}, false, nil // this process is already settling it
	}
	// Two attempts: an idle connection the server dropped shows up only as
	// an error on its next use, and that must not become a reported
	// settlement failure when a fresh connection answers fine.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := settleLockConn(ctx)
		if err != nil {
			return settleLease{}, false, err
		}
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var got bool
		err = conn.QueryRow(qctx, `select pg_try_advisory_lock($1, hashtext($2))`,
			advisoryLockTaskSettle, taskID).Scan(&got)
		cancel()
		if err != nil {
			lastErr = err
			if ctx.Err() != nil && !conn.IsClosed() {
				// The CALLER's context ended (a client went away, a shutdown)
				// and the connection survived it. It is not at fault; dropping
				// it would cost every other in-flight settlement its lock for
				// nothing. (pgx closes a connection whose query was cancelled
				// mid-flight — then IsClosed is true, and the drop below
				// records that the locks went with it.)
				return settleLease{}, false, fmt.Errorf("%w: %v", errSettleLockUnavailable, ctx.Err())
			}
			settleLockDropConn("lock query failed")
			continue
		}
		if !got {
			return settleLease{}, false, nil
		}
		if settleLock.held == nil {
			settleLock.held = map[string]uint64{}
		}
		settleLock.held[taskID] = settleLock.gen
		return settleLease{taskID: taskID, gen: settleLock.gen}, true, nil
	}
	log.Printf("[payments][settle] task=%s: lock query: %v", taskID, lastErr)
	return settleLease{}, false, fmt.Errorf("%w: lock query failed", errSettleLockUnavailable)
}

// unlockTaskSettlement releases a lease taken by tryLockTaskSettlement.
// On context.Background(): the caller's context may be the one that just
// expired, and a lock that stays held because its release was cancelled is
// a task nobody can settle until the process restarts.
func unlockTaskSettlement(l settleLease) {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	// Only this lease's own mark: a stale lease released twice, or late,
	// must not clear the mark a newer settlement of the same task holds.
	if g, ok := settleLock.held[l.taskID]; ok && g == l.gen {
		delete(settleLock.held, l.taskID)
	}
	if l.gen != settleLock.gen || settleLock.conn == nil || settleLock.conn.IsClosed() {
		// The connection this lock lived on is gone (and, if a newer one
		// exists, any lock on it belongs to someone else). The database
		// released ours when the connection died; settleLockDropConn said
		// so. Nothing to release here.
		if l.gen != settleLock.gen {
			log.Printf("[payments][settle] task=%s: lock lease from a dropped connection — not touching the current one", l.taskID)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var released bool
	if err := settleLock.conn.QueryRow(ctx, `select pg_advisory_unlock($1, hashtext($2))`,
		advisoryLockTaskSettle, l.taskID).Scan(&released); err != nil {
		settleLockDropConn("unlock failed")
		return
	}
	if !released {
		log.Printf("[payments][settle][ERROR] task=%s: unlock found no lock held on the live connection", l.taskID)
	}
}

// isTaskSettlementRunning reports whether some settlement of the task holds
// the lock right now — here (held) or in another process (database) —
// without taking it for longer than one round-trip. Used by the sweep to
// tell "the chain died" from "the chain is slow".
func isTaskSettlementRunning(ctx context.Context, taskID string) (bool, error) {
	lease, got, err := tryLockTaskSettlement(ctx, taskID)
	if err != nil {
		return false, err
	}
	if got {
		unlockTaskSettlement(lease)
		return false, nil
	}
	return true, nil
}

// resetSettleLockForTests drops the shared connection and the held set, so
// a test fixture that swaps databases does not leave a connection to the
// old one (admin_task_ops_test.go).
func resetSettleLockForTests() {
	settleLock.mu.Lock()
	defer settleLock.mu.Unlock()
	if settleLock.conn != nil {
		_ = settleLock.conn.Close(context.Background())
	}
	settleLock.conn = nil
	settleLock.held = nil
}
