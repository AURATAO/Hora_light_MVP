package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hora-auth/internal/tracing"
)

// Phoenix tracing (D-22): start-up, shutdown, and deleting a deleted
// account's traces.
//
// The traces of a chat conversation are tagged session.id = the channel
// identity id. Account deletion queues those ids in public.trace_deletions in
// its own transaction (channel.QueueTraceDeletionsTx); the sweeper below asks
// Phoenix to delete each session (DELETE /v1/sessions/{id}, which cascades
// through its traces and spans) and removes the row once Phoenix says it is
// gone. A failure is retried with backoff and logged with its status only —
// no session id, no user id, no text.

// startTracing turns Phoenix tracing on when it is configured and starts the
// deletion sweeper. Unconfigured, it does nothing at all: no exporter, no
// sweeper, no signal handler — the server runs exactly as before. The
// returned wait blocks, after ListenAndServe returns, until the flush on
// shutdown is done (a no-op when tracing is off).
func startTracing(srv *http.Server, sqlDB *sql.DB) (wait func()) {
	noWait := func() {}
	cfg, ok := tracing.ConfigFromEnv()
	if !ok {
		return noWait
	}
	shutdown, err := tracing.Start(cfg)
	if err != nil {
		log.Printf("[tracing] not started: %v", err)
		return noWait
	}
	log.Printf("[tracing] Phoenix tracing ON (D-22): redacted chat traces to the configured collector")
	if d, ok := tracing.DeleterFromEnv(); ok {
		go watchTraceDeletions(context.Background(), sqlDB, d, time.Minute)
	}
	// Flush on shutdown. Render sends SIGTERM and waits 30s: stop taking
	// requests (up to 10s for those in flight), then export what is queued
	// (up to 5s), then exit as SIGTERM would have.
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[shutdown] http: %v", err)
		}
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		if err := shutdown(ctx); err != nil {
			log.Printf("[tracing] flush on shutdown: incomplete")
		}
		cancel()
	}()
	return func() { <-flushed }
}

// sessionDeleter is tracing.Deleter, or a test's fake.
type sessionDeleter interface {
	DeleteSession(ctx context.Context, sessionID string) error
}

// watchTraceDeletions runs sweepTraceDeletions every `every`.
func watchTraceDeletions(ctx context.Context, sqlDB *sql.DB, d sessionDeleter, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("[trace-deletions] sweep panic")
					}
				}()
				sweepTraceDeletions(ctx, sqlDB, d)
			}()
		}
	}
}

// traceDeletionBackoff: 2, 4, 8 … minutes after each failure, at most 6 hours.
func traceDeletionBackoff(attempts int) time.Duration {
	if attempts > 8 {
		return 6 * time.Hour
	}
	d := time.Duration(1<<attempts) * time.Minute
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

// traceDeletionAlertAt: a deletion still failing after this many attempts
// (about a day) is logged as an error for ops.
const traceDeletionAlertAt = 12

// sweepTraceDeletions makes one pass over the due rows. Two instances running
// at once (a deploy's overlap) is harmless: deleting a session twice is a 404
// the second time, which counts as done.
func sweepTraceDeletions(ctx context.Context, sqlDB *sql.DB, d sessionDeleter) (deleted, failed int) {
	var present bool
	if err := sqlDB.QueryRowContext(ctx, `select to_regclass('public.trace_deletions') is not null`).Scan(&present); err != nil {
		log.Printf("[trace-deletions] probe: %v", err)
		return 0, 0
	}
	if !present {
		return 0, 0 // migration not applied; account deletion queues nothing either
	}
	rows, err := sqlDB.QueryContext(ctx, `
		select session_id, attempts from public.trace_deletions
		 where next_attempt_at <= now()
		 order by next_attempt_at
		 limit 50`)
	if err != nil {
		log.Printf("[trace-deletions] read queue: %v", err)
		return 0, 0
	}
	type due struct {
		session  string
		attempts int
	}
	var list []due
	for rows.Next() {
		var x due
		if err := rows.Scan(&x.session, &x.attempts); err != nil {
			log.Printf("[trace-deletions] read queue: %v", err)
			_ = rows.Close() // the scan error is the one reported
			return 0, 0
		}
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[trace-deletions] read queue: %v", err)
		return 0, 0
	}
	if err := rows.Close(); err != nil {
		log.Printf("[trace-deletions] read queue: close: %v", err)
	}

	lastStatus := 0
	for _, x := range list {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := d.DeleteSession(cctx, x.session)
		cancel()
		if err == nil {
			if _, err := sqlDB.ExecContext(ctx, `delete from public.trace_deletions where session_id = $1`, x.session); err != nil {
				log.Printf("[trace-deletions] dequeue: %v", err) // deleted in Phoenix; the next pass gets a 404 and dequeues
			}
			deleted++
			continue
		}
		failed++
		status := 0
		var se *tracing.StatusError
		if errors.As(err, &se) {
			status = se.Status
		}
		lastStatus = status
		attempts := x.attempts + 1
		if _, err := sqlDB.ExecContext(ctx, `
			update public.trace_deletions
			   set attempts = $2, last_status = $3, next_attempt_at = now() + $4 * interval '1 second'
			 where session_id = $1`,
			x.session, attempts, status, int(traceDeletionBackoff(attempts).Seconds())); err != nil {
			log.Printf("[trace-deletions] reschedule: %v", err)
		}
		if attempts == traceDeletionAlertAt {
			log.Printf("[trace-deletions][ERROR] a Phoenix trace deletion has failed %d times (status %d); still retrying — check PHOENIX_COLLECTOR_ENDPOINT / PHOENIX_API_KEY and that Phoenix is 13.13+", attempts, status)
		}
	}
	if deleted > 0 || failed > 0 {
		// Counts and a status only (S-12).
		log.Printf("[trace-deletions] deleted=%d failed=%d last_failed_status=%d", deleted, failed, lastStatus)
	}
	return deleted, failed
}
