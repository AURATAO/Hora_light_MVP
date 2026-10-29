package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// newSub is a fresh Supabase-shaped sub (a v4 UUID) without adding a uuid
// module to go.mod for a test.
func newSub(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// The production users table has what the shared fixture leaves out: the
// supabase_sub column and the case-insensitive email index. Both are what
// resolveSupabaseUser's behaviour depends on, so they are added here rather
// than assumed.
func setupSupabaseUserDB(t *testing.T) {
	t.Helper()
	setupCreatedViaDB(t)
	if _, err := db.Exec(context.Background(), `
		alter table public.users add column if not exists supabase_sub uuid unique;
		create unique index if not exists users_email_lower_uidx on public.users (lower(email));
	`); err != nil {
		t.Fatalf("supabase_sub fixture: %v", err)
	}
}

// Two requests for the same never-seen person at the same instant — the
// first-time login shape (/auth/exchange plus a Bearer /auth/me). Before the
// retry, the loser's insert failed on users_email_key and the caller
// answered 500. Now both get the one row.
func TestResolveSupabaseUserConcurrentFirstLoginShareOneRow(t *testing.T) {
	setupSupabaseUserDB(t)
	ctx := context.Background()

	for round := 0; round < 5; round++ {
		sub := newSub(t)
		email := "first." + sub[:8] + "@example.test"
		const n = 6
		ids := make([]string, n)
		errs := make([]error, n)
		var start, done sync.WaitGroup
		start.Add(1)
		done.Add(n)
		for i := 0; i < n; i++ {
			go func(i int) {
				defer done.Done()
				start.Wait()
				ids[i], errs[i] = resolveSupabaseUser(ctx, sqldb, sub, email)
			}(i)
		}
		start.Done()
		done.Wait()
		for i := 0; i < n; i++ {
			if errs[i] != nil {
				t.Fatalf("round %d call %d: %v", round, i, errs[i])
			}
			if ids[i] != ids[0] {
				t.Fatalf("round %d: call %d got %s, call 0 got %s", round, i, ids[i], ids[0])
			}
		}
		var rows int
		if err := db.QueryRow(ctx, `select count(*) from public.users where email = $1`, email).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("round %d: %d rows for one person", round, rows)
		}
	}
}

// The email-linking step: a row that exists without a sub (Google sign-in,
// review account) is attached, not duplicated — and the match ignores case,
// which users_email_lower_uidx would otherwise turn into a unique violation
// on the insert.
func TestResolveSupabaseUserLinksExistingEmailCaseInsensitively(t *testing.T) {
	setupSupabaseUserDB(t)
	ctx := context.Background()

	var existing string
	if err := db.QueryRow(ctx,
		`insert into public.users (email, name) values ($1, $2) returning id::text`,
		"Linked.Person@Example.test", "Linked",
	).Scan(&existing); err != nil {
		t.Fatal(err)
	}

	sub := newSub(t)
	got, err := resolveSupabaseUser(ctx, sqldb, sub, "linked.person@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got != existing {
		t.Fatalf("linked to %s, expected the existing row %s", got, existing)
	}

	var storedSub string
	if err := db.QueryRow(ctx, `select supabase_sub::text from public.users where id = $1::uuid`, existing).Scan(&storedSub); err != nil {
		t.Fatal(err)
	}
	if storedSub != sub {
		t.Fatalf("supabase_sub not attached: %q", storedSub)
	}

	// Second login with the same sub: step 1 finds it, nothing else runs.
	again, err := resolveSupabaseUser(ctx, sqldb, sub, "linked.person@example.test")
	if err != nil || again != existing {
		t.Fatalf("second login: id=%s err=%v", again, err)
	}

	// The other direction: stored lower-case, JWT upper-case.
	var lowerRow string
	if err := db.QueryRow(ctx,
		`insert into public.users (email, name) values ($1, $2) returning id::text`,
		"other.person@example.test", "Other",
	).Scan(&lowerRow); err != nil {
		t.Fatal(err)
	}
	gotUpper, err := resolveSupabaseUser(ctx, sqldb, newSub(t), "OTHER.PERSON@EXAMPLE.TEST")
	if err != nil || gotUpper != lowerRow {
		t.Fatalf("upper-case JWT email: id=%s err=%v, want %s", gotUpper, err, lowerRow)
	}
}

// The race, made deterministic: a transaction inserts the winner's row and
// holds it uncommitted. The loser's steps 1 and 2 see nothing (MVCC), its
// insert blocks on the uncommitted unique entry, and when the winner commits
// the insert fails with 23505 — exactly the production sequence, without
// relying on goroutine timing. The loser must come back with the winner's
// row, by whichever step finds it.
func TestResolveSupabaseUserLoserOfTheRaceGetsTheWinnersRow(t *testing.T) {
	setupSupabaseUserDB(t)
	ctx := context.Background()

	cases := []struct {
		name            string
		winnerSub       string
		winnerEmail     string
		loserSub        string
		loserEmail      string
		blockingOn      string
		loserAttachesTo string // which step must find the row afterwards
	}{
		{
			// The incident: same person, same sub, same email, two requests.
			name: "same sub and email", winnerSub: newSub(t), winnerEmail: "race.same@example.test",
			blockingOn: "users_email_key / users_supabase_sub_key", loserAttachesTo: "step 1 (sub)",
		},
		{
			// Same email, a different Supabase identity: blocks on users_email_key,
			// the retry links by email (step 2).
			name: "same email different sub", winnerSub: newSub(t), winnerEmail: "race.email@example.test",
			loserSub: newSub(t), blockingOn: "users_email_key", loserAttachesTo: "step 2 (email)",
		},
		{
			// Same sub, a different email: blocks on users_supabase_sub_key, the
			// retry finds the row by sub (step 1).
			name: "same sub different email", winnerSub: newSub(t), winnerEmail: "race.sub.a@example.test",
			loserEmail: "race.sub.b@example.test", blockingOn: "users_supabase_sub_key", loserAttachesTo: "step 1 (sub)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loserSub, loserEmail := tc.loserSub, tc.loserEmail
			if loserSub == "" {
				loserSub = tc.winnerSub
			}
			if loserEmail == "" {
				loserEmail = tc.winnerEmail
			}

			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
			var winner string
			if err := tx.QueryRow(ctx,
				`insert into public.users (supabase_sub, email, name) values ($1::uuid, $2, '') returning id::text`,
				tc.winnerSub, tc.winnerEmail,
			).Scan(&winner); err != nil {
				t.Fatal(err)
			}

			type result struct {
				id  string
				err error
			}
			done := make(chan result, 1)
			go func() {
				id, err := resolveSupabaseUser(ctx, sqldb, loserSub, loserEmail)
				done <- result{id, err}
			}()

			// Wait until the loser is blocked on the uncommitted row.
			if !waitForBlockedInsert(t, ctx, 5*time.Second) {
				t.Fatalf("loser never blocked on %s — the race was not reproduced", tc.blockingOn)
			}
			select {
			case r := <-done:
				t.Fatalf("loser returned before the winner committed: id=%s err=%v", r.id, r.err)
			default:
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}

			var r result
			select {
			case r = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("loser still blocked 10s after the winner committed")
			}
			if r.err != nil {
				t.Fatalf("loser: %v (should have been found by %s)", r.err, tc.loserAttachesTo)
			}
			if r.id != winner {
				t.Fatalf("loser got %s, winner's row is %s (%s)", r.id, winner, tc.loserAttachesTo)
			}
			var rows int
			if err := db.QueryRow(ctx,
				`select count(*) from public.users where lower(email) in (lower($1), lower($2)) or supabase_sub in ($3::uuid, $4::uuid)`,
				tc.winnerEmail, loserEmail, tc.winnerSub, loserSub,
			).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("%d rows after the race, want 1", rows)
			}
			// Which step found it: by sub, the row is untouched; by email, the
			// loser's sub now sits on the winner's row (the r1 #2 residual —
			// the newest identity for an address owns it).
			var storedSub, storedEmail string
			if err := db.QueryRow(ctx, `select supabase_sub::text, email from public.users where id = $1::uuid`, winner).Scan(&storedSub, &storedEmail); err != nil {
				t.Fatal(err)
			}
			if storedEmail != tc.winnerEmail {
				t.Fatalf("winner's email changed to %q", storedEmail)
			}
			if storedSub != loserSub {
				t.Fatalf("winner's row has sub %s, want %s (%s)", storedSub, loserSub, tc.loserAttachesTo)
			}
		})
	}
}

// waitForBlockedInsert polls pg_stat_activity until some other backend is
// waiting on a lock in an INSERT into public.users.
func waitForBlockedInsert(t *testing.T, ctx context.Context, timeout time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for ctx.Err() == nil {
		var n int
		if err := db.QueryRow(ctx, `
			select count(*) from pg_stat_activity
			where datname = current_database()
			  and pid <> pg_backend_pid()
			  and wait_event_type = 'Lock'
			  and query ilike '%insert into public.users%'
		`).Scan(&n); err != nil {
			if ctx.Err() != nil {
				return false
			}
			t.Fatal(err)
		}
		if n > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// The empty-email paths: Supabase can omit the claim (phone sign-in). A
// known sub is still found by step 1; a new one is inserted with an empty
// email — and a SECOND new sub with an empty email collides on
// users_email_key (” = ”). That collision is not the race, so the retry
// must return it after one look-again rather than hide it. Pre-existing:
// the old code failed the same way; every HO:RA sign-in method carries an
// email today, so this pins the behaviour, not endorses it.
func TestResolveSupabaseUserEmptyEmailPaths(t *testing.T) {
	setupSupabaseUserDB(t)
	ctx := context.Background()

	sub := newSub(t)
	if _, err := resolveSupabaseUser(ctx, sqldb, sub, "taken@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSupabaseUser(ctx, sqldb, sub, ""); err != nil {
		t.Fatalf("sub match with empty email: %v", err)
	}
	if _, err := resolveSupabaseUser(ctx, sqldb, newSub(t), ""); err != nil {
		t.Fatalf("first new sub with empty email: %v", err)
	}
	_, err := resolveSupabaseUser(ctx, sqldb, newSub(t), "")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second empty-email sub: want the 23505 surfaced, got %v", err)
	}
}
