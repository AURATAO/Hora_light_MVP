package main

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/jackc/pgx/v5/pgconn"
)

// resolveSupabaseUser maps a Supabase identity (the JWT's sub and email) to
// the internal public.users id, creating the row when the person is new.
//
// Priority, unchanged from the three hand-copied versions this replaced:
//
//  1. supabase_sub match → that row.
//  2. email match → attach the sub to that row (a Google sign-in, a review
//     account, or an earlier Supabase identity for the same address) and
//     return it. The match is case-insensitive: users_email_lower_uidx
//     guarantees at most one row either way, and Supabase can hand back the
//     address in a different case than the one stored.
//  3. Neither → insert.
//
// Why the retry: a first-time login fires /auth/exchange and a Bearer
// /auth/me at the same moment, and dualAuth also runs this. Both requests
// find nothing at steps 1 and 2 and both insert; the slower one hit
// users_email_key and /auth/exchange answered 500 (production, 2026-10-28
// 23:06 UTC — the person was in fact logged in by the other request). On a
// unique violation the loser now simply looks again: the winner's row is
// there, step 1 or 2 finds it, and both requests return the same id. One
// retry is enough — a second violation means something other than this race
// and is returned as the error it is.
func resolveSupabaseUser(ctx context.Context, sdb *sql.DB, extSub, email string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		id, err := lookupOrInsertSupabaseUser(ctx, sdb, extSub, email)
		if err == nil {
			if attempt > 0 {
				// How often the first-login race actually fires. No email.
				log.Printf("[auth] resolve supabase user: won on retry after unique violation")
			}
			return id, nil
		}
		lastErr = err
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			break
		}
	}
	// dualAuth and tryAuth turn any failure into a plain 401 / no uid, so
	// this is the one place the cause is written down. No email in the line.
	log.Printf("[auth] resolve supabase user err=%v", lastErr)
	return "", lastErr
}

func lookupOrInsertSupabaseUser(ctx context.Context, sdb *sql.DB, extSub, email string) (string, error) {
	var internalID string
	err := sdb.QueryRowContext(ctx,
		`select id from public.users where supabase_sub = $1::uuid`, extSub,
	).Scan(&internalID)
	if errors.Is(err, sql.ErrNoRows) && email != "" {
		err = sdb.QueryRowContext(ctx,
			`update public.users set supabase_sub = $1::uuid where lower(email) = lower($2) returning id`,
			extSub, email,
		).Scan(&internalID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Truly new user — create a fresh row. No name: the person has not
		// been asked yet, and an email prefix stored here would read as an
		// answer (names.go).
		err = sdb.QueryRowContext(ctx, `
			insert into public.users (supabase_sub, email, name)
			values ($1::uuid, $2, '')
			returning id
		`, extSub, email).Scan(&internalID)
	}
	if err != nil {
		return "", err
	}
	if internalID == "" {
		return "", errors.New("empty id")
	}
	return internalID, nil
}
