package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hora-auth/internal/tracing"
)

// Phoenix trace deletion (D-22) against a real Postgres: the account deletion
// queues its conversations, the sweeper deletes them in Phoenix, and a
// failure is retried, not lost.

var channelMigrations = []string{
	"20261019120000_messaging_channel.sql",
	"20261020120000_channel_messages_address_e164.sql",
	"20261022120000_channel_approval_columns.sql",
	"20261022130000_channel_approval_question_pid.sql",
	"20261024120000_channel_cancel_by_chat.sql",
	"20261024210000_channel_handoff_timeout.sql",
}

const dropChannelTables = `DROP TABLE IF EXISTS public.trace_deletions, public.channel_setup_tokens,
	public.channel_conversations, public.channel_messages, public.channel_identities CASCADE`

// setupTraceDeletionDB is the account deletion fixture plus the chat tables
// and the trace deletion queue, from their migrations.
func setupTraceDeletionDB(t *testing.T, withChannel bool) {
	t.Helper()
	setupDeletionDB(t)
	mustExec(t, dropChannelTables)
	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), dropChannelTables); err != nil {
			t.Errorf("drop chat tables: %v", err)
		}
	})
	files := []string{"20261103120000_trace_deletions.sql"}
	if withChannel {
		files = append(channelMigrations, files...)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("..", "supabase", "migrations", f))
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, string(b))
	}
}

type fakeDeleter struct {
	fail  map[string]error
	asked []string
}

func (f *fakeDeleter) DeleteSession(_ context.Context, id string) error {
	f.asked = append(f.asked, id)
	return f.fail[id]
}

func TestSweepTraceDeletionsDeletesAndRetries(t *testing.T) {
	setupTraceDeletionDB(t, false)
	ctx := context.Background()
	mustExec(t, `insert into public.trace_deletions (session_id, next_attempt_at) values
		('s-ok', now() - interval '1 minute'),
		('s-down', now() - interval '1 minute'),
		('s-later', now() + interval '1 hour')`)
	d := &fakeDeleter{fail: map[string]error{"s-down": &tracing.StatusError{Status: 502}}}

	deleted, failed := sweepTraceDeletions(ctx, sqldb, d)
	if deleted != 1 || failed != 1 {
		t.Fatalf("deleted=%d failed=%d, want 1/1", deleted, failed)
	}
	if len(d.asked) != 2 {
		t.Fatalf("asked Phoenix about %v; a row not yet due was tried", d.asked)
	}
	var n, attempts, status int
	var waitMin float64
	_ = db.QueryRow(ctx, `select count(*) from public.trace_deletions where session_id = 's-ok'`).Scan(&n)
	if n != 0 {
		t.Fatal("a deleted session stayed queued")
	}
	if err := db.QueryRow(ctx, `select attempts, last_status, extract(epoch from next_attempt_at - now()) / 60
	                             from public.trace_deletions where session_id = 's-down'`).Scan(&attempts, &status, &waitMin); err != nil {
		t.Fatalf("a failed deletion was lost: %v", err)
	}
	if attempts != 1 || status != 502 || waitMin < 1 || waitMin > 3 {
		t.Fatalf("failed row: attempts=%d status=%d retry in %.1f min", attempts, status, waitMin)
	}

	// Not due yet: nothing is asked. Due again and Phoenix back: dequeued.
	d.asked = nil
	if del, fl := sweepTraceDeletions(ctx, sqldb, d); del != 0 || fl != 0 || len(d.asked) != 0 {
		t.Fatalf("early retry: %d/%d %v", del, fl, d.asked)
	}
	mustExec(t, `update public.trace_deletions set next_attempt_at = now() - interval '1 second' where session_id = 's-down'`)
	delete(d.fail, "s-down")
	if del, _ := sweepTraceDeletions(ctx, sqldb, d); del != 1 {
		t.Fatalf("retry deleted %d", del)
	}
	_ = db.QueryRow(ctx, `select count(*) from public.trace_deletions where session_id = 's-down'`).Scan(&n)
	if n != 0 {
		t.Fatal("retried deletion stayed queued")
	}
	// A transport error is retried too, with status 0.
	mustExec(t, `insert into public.trace_deletions (session_id, next_attempt_at) values ('s-net', now() - interval '1 second')`)
	d.fail["s-net"] = errors.New("phoenix: delete session: dial tcp: connection refused")
	if _, fl := sweepTraceDeletions(ctx, sqldb, d); fl != 1 {
		t.Fatal("transport error not counted as a failure")
	}
	_ = db.QueryRow(ctx, `select last_status from public.trace_deletions where session_id = 's-net'`).Scan(&status)
	if status != 0 {
		t.Fatalf("transport error status %d", status)
	}
}

func TestSweepWithoutTheQueueTableIsQuiet(t *testing.T) {
	setupTraceDeletionDB(t, false)
	mustExec(t, `drop table public.trace_deletions`)
	d := &fakeDeleter{}
	if del, fl := sweepTraceDeletions(context.Background(), sqldb, d); del != 0 || fl != 0 || len(d.asked) != 0 {
		t.Fatalf("%d/%d %v", del, fl, d.asked)
	}
}

func TestTraceDeletionBackoff(t *testing.T) {
	prev := traceDeletionBackoff(0)
	for a := 1; a < 40; a++ {
		b := traceDeletionBackoff(a)
		if b < prev || b.Hours() > 6 {
			t.Fatalf("backoff(%d) = %s after %s", a, b, prev)
		}
		prev = b
	}
}

// The account deletion queues the account's conversations when Phoenix is
// configured, and only then.
func TestAccountDeletionQueuesItsTraces(t *testing.T) {
	for _, configured := range []bool{false, true} {
		setupTraceDeletionDB(t, true)
		if configured {
			t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "https://phoenix.invalid")
			t.Setenv("PHOENIX_API_KEY", "k")
		} else {
			t.Setenv("PHOENIX_COLLECTOR_ENDPOINT", "")
			t.Setenv("PHOENIX_API_KEY", "")
		}
		w := seedOpsWorld(t, "completed")
		ctx := context.Background()
		var identityID string
		if err := db.QueryRow(ctx, `insert into public.channel_identities (channel, address, user_id, verified_at)
		                             values ('imessage', '+15550000909', $1::uuid, now()) returning id::text`, w.requesterID).Scan(&identityID); err != nil {
			t.Fatal(err)
		}

		code, body := deleteAs(t, w.requesterID, requesterEmail)
		if code != 200 {
			t.Fatalf("delete: %d %v", code, body)
		}
		var queued int
		_ = db.QueryRow(ctx, `select count(*) from public.trace_deletions where session_id = $1`, identityID).Scan(&queued)
		var metaRaw []byte
		if err := db.QueryRow(ctx, `select meta from public.audit_logs where action = 'ACCOUNT_DELETED' order by created_at desc limit 1`).Scan(&metaRaw); err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			t.Fatal(err)
		}
		var want int
		var wantMeta any // tracing off: the audit row is as before, without the key
		if configured {
			want, wantMeta = 1, float64(1)
		}
		if queued != want || meta["trace_sessions_queued"] != wantMeta {
			t.Fatalf("configured=%v: queued=%d audit=%v, want %d", configured, queued, meta["trace_sessions_queued"], want)
		}
		var left int
		_ = db.QueryRow(ctx, `select count(*) from public.channel_identities where id = $1::uuid`, identityID).Scan(&left)
		if left != 0 {
			t.Fatal("the identity survived the account deletion")
		}
	}
}
