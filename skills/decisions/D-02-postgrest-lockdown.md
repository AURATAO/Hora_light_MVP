# D-02: Lock the PostgREST side door with deny-all RLS

**Date:** 2026-07-11
**Status:** Accepted — CLOSED 2026-07-11, no action required
**Trigger:** Constitution instantiation review of BACKEND_REFERENCE.md: the DB has zero RLS by design, the Go backend connects as `postgres` and is the sole authorization layer — but the Supabase anon key ships in the web client for auth, and Supabase projects expose the PostgREST Data API by default with grants to `anon`/`authenticated` on `public`.

## Outcome (2026-07-11)
The door was already locked. Owner verified:
1. `curl .../rest/v1/tasks` with anon key → `[]` (ambiguous alone)
2. `select tablename, rowsecurity from pg_tables where schemaname='public'` → **rowsecurity = true on ALL tables**

Conclusion: RLS is enabled everywhere with (presumed) zero policies = PostgREST deny-all. Dashboard-created tables enable RLS by default; BACKEND_REFERENCE §1.3 ("None exist on Hora MVP tables") was an inference error — RLS state cannot be inferred from Go code since the postgres role bypasses it. **Correct BACKEND_REFERENCE §1.3 accordingly.**

Residual check (non-blocking, do with next DB session): run the pg_policies query from I-01a to confirm zero policies exist.

I-01a is hereby ACTIVATED as a blocking invariant. S-10 confirmed as written.

## Original proposal (kept for context)
1. **Test exposure first** (2 minutes, do before anything else):
   ```bash
   curl "https://akxsdkerudurzcemurrb.supabase.co/rest/v1/tasks?select=id&limit=1" \
     -H "apikey: $ANON_KEY" -H "Authorization: Bearer $ANON_KEY"
   ```
   Data returned → the entire DB (tasks, profiles incl. phone numbers, live GPS pings) is publicly readable/writable with a key extractable from any browser. Permission error → door already closed; record how, and accept this decision as documentation.

2. **If open, land one migration:** `ALTER TABLE <each public table> ENABLE ROW LEVEL SECURITY;` — **zero policies**. PostgREST paths become deny-all. The Go backend (postgres role, direct pgx connection) bypasses RLS and is completely unaffected. `supabase.auth.*` and the public `avatars` bucket are also unaffected (auth schema / storage policies are separate).

3. Activate I-01a as a blocking invariant.

## Alternatives considered
- **Disable the Data API in project settings** — also valid; but RLS-deny-all is versionable in a migration file, survives settings resets, and doubles as the S-13 pattern for future tables. Can do both.
- **Revoke grants from anon/authenticated** — equivalent effect, harder to audit than `pg_tables.rowsecurity`.
- **Adopt RLS as a real authorization layer** — rejected for now: it would duplicate the Go permission logic in SQL, violating single-source-of-truth, for no current benefit. Revisit only if a client-direct data path is ever deliberately wanted (that would be its own Tier 3 decision).

## Constitution impact
Confirms S-10 as written; activates I-01a.

## Evidence
- BACKEND_REFERENCE §1.3: "None exist on Hora MVP tables… RLS is bypassed [by Go]."
- BACKEND_REFERENCE §3: frontend uses `@supabase/supabase-js` for auth → anon key is in the client bundle.
- Exposure test: curl → `[]`; pg_tables.rowsecurity → all true (owner, 2026-07-11)
- Two uncommitted migration files discovered in `supabase/migrations/`
  implementing exactly this lockdown (all 7 documented tables + orphaned
  `timesheets`), authored by a prior AI session.
- The 07-11 "rowsecurity all true" result is most likely the EFFECT of these
  migrations having been applied — not a dashboard default.
- BACKEND_REFERENCE §1.3 ("no RLS") may therefore have been accurate when
  written (2026-07-07), later fixed by this lockdown.
- Conclusion unchanged: door is locked, I-01a stays active. Provenance corrected.