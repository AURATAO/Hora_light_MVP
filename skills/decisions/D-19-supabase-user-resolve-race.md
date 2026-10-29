# D-19: One race-safe resolver for Supabase identities

**Date:** 2026-10-29
**Status:** Accepted
**Trigger:** Incident. Production 2026-10-28 23:06 UTC: a first-time user's webapp fired `POST /auth/exchange` and a Bearer `GET /auth/me` at the same instant. Both ran the same lookup (sub → email → insert), both inserted, the slower one hit `users_email_key` and `/auth/exchange` answered 500 (`[exchange] upsert user err=… duplicate key … "users_email_key"`). The person was logged in by the other request; the 500 was the only symptom.

## Decision
The Supabase-identity → `public.users` lookup lives in exactly one function, `resolveSupabaseUser` (`server/supabase_user.go`), used by `/auth/exchange`, `dualAuth` and `tryAuth`. On a unique violation (SQLSTATE 23505) it looks again once — the winner's row is committed by then and step 1 or 2 finds it — and returns the same id to both requests. The email step matches `lower(email)`, the expression `users_email_lower_uidx` enforces, so a case-different address links the existing row instead of failing the insert.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. Money and safety tables untouched; `users` writes still happen only in the Go backend.

## Context and alternatives
- `INSERT … ON CONFLICT` was the obvious alternative. Rejected for this change: two unique indexes on the same column (`email`, `lower(email)`) plus `supabase_sub` means the arbiter choice is subtle, and it would silently change the link semantics. The retry keeps the three steps exactly as they were and only adds "look again".
- Semantics deliberately preserved, including one that is a declared residual: the email step attaches the *newest* sub to a row without checking whether a sub is already there (two Supabase identities for one address flip-flop ownership). Pre-existing; unchanged; Arbiter's call whether to guard on `supabase_sub is null` or `email_verified`.
- Empty-email sign-ins (none today — every HO:RA method carries an email) insert `''`, and a second one collides on `users_email_key`. Pre-existing; now pinned by a test; the retry returns the second 23505 rather than hiding it.

## Evidence
- Render request/app logs 2026-10-28T23:06:17–19Z for the IP above; the row `367b57b8-…` has `created_at 23:06:18.51`, between the two requests' start and the 500.
- Adversarial review (isolated folder, sonnet): r1 PASS with two MEDIUMs (probabilistic race test; sub-overwrite + case-widening question) → r2 PASS, LOWs applied (race test asserts which row and which sub; `pg_stat_activity` poll scoped and bounded; retry-success logged).
- Tests (`server/supabase_user_test.go`, DB-backed, need `TEST_DATABASE_URL`): deterministic race via an uncommitted winner transaction in three variants (same sub+email; same email/different sub → `users_email_key`; same sub/different email → `users_supabase_sub_key`), case-insensitive link both directions, empty-email paths, 6-goroutine smoke.
- This container cannot build Go (no `proxy.golang.org`); gofmt clean; the Mac run with `REQUIRE_DB_TESTS=1 go test -p 1 ./... -race -count=1` is the merge gate, as in the runbook.
