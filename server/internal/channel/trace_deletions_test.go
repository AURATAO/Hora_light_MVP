package channel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// traceQueueDB is testDB plus the trace deletion queue, from its migration.
func traceQueueDB(t *testing.T) *PGStore {
	t.Helper()
	db := testDB(t)
	mig, err := os.ReadFile(filepath.Join("..", "..", "..", "supabase", "migrations", "20261103120000_trace_deletions.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE IF EXISTS trace_deletions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("migration: %v", err)
	}
	return &PGStore{DB: db}
}

// Account deletion queues every conversation of the account — the sessions
// its traces are tagged with — and nobody else's, before the purge removes
// the identities (D-22).
func TestQueueTraceDeletionsNamesOnlyTheAccountsConversations(t *testing.T) {
	s := traceQueueDB(t)
	db := s.DB
	ctx := context.Background()
	me, other := newUser(t, db), newUser(t, db)
	var mine []string
	for i, addr := range []string{"+15550000111", "+15550000112", "+15550000222"} {
		id, err := s.UpsertIdentity(ctx, IMessage, addr)
		if err != nil {
			t.Fatal(err)
		}
		owner := me
		if i == 2 {
			owner = other
		} else {
			mine = append(mine, id.ID)
		}
		if _, err := db.Exec(`UPDATE channel_identities SET user_id = $2, verified_at = now() WHERE id = $1`, id.ID, owner); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := QueueTraceDeletionsTx(ctx, tx, me)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PurgeUserTx(ctx, tx, me); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("queued %d, want 2", n)
	}
	rows, err := db.Query(`SELECT session_id, attempts, next_attempt_at > now() FROM trace_deletions ORDER BY session_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var attempts int
		var later bool
		if err := rows.Scan(&id, &attempts, &later); err != nil {
			t.Fatal(err)
		}
		if attempts != 0 || !later {
			t.Errorf("row %s: attempts=%d first attempt deferred=%v", id, attempts, later)
		}
		got[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[mine[0]] || !got[mine[1]] {
		t.Fatalf("queued %v, want exactly %v", got, mine)
	}

	// Queued twice (a retried deletion): one row each, no error.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO trace_deletions (session_id, next_attempt_at) VALUES ($1, now() + interval '1 hour')`, "already-there"); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueTraceDeletionsTx(ctx, tx, other); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var c int
	if err := db.QueryRow(`SELECT count(*) FROM trace_deletions`).Scan(&c); err != nil || c != 4 {
		t.Fatalf("rows = %d (%v), want 4", c, err)
	}
}

// A database without the queue (migration not applied) queues nothing and
// does not fail the account deletion.
func TestQueueTraceDeletionsWithoutTheTableIsANoop(t *testing.T) {
	s := traceQueueDB(t)
	if _, err := s.DB.Exec(`DROP TABLE trace_deletions`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if n, err := QueueTraceDeletionsTx(ctx, tx, newUser(t, s.DB)); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
