# D-02: Lock the PostgREST side door with deny-all RLS

**Date:** 2026-07-11
**Status:** Accepted — CLOSED 2026-07-11, **REOPENED AND SUPERSEDED 2026-09-10** (see Correction below)
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

---

## Correction (2026-09-10) — the residual check, finally run

**The conclusion above was wrong.** `public` held twelve policies, not zero.

Three were the `deny all (client)` ones this record expected. The other nine
were PERMISSIVE grants to `authenticated`, and they were live — anon and
authenticated held full DML grants on the tables involved:

| Policy | Table | What it allowed an authenticated user to do directly |
|---|---|---|
| `profiles_read_own` / `profiles_update_own` | profiles | UPDATE their own row, **every column** — `is_verified_supporter` included, i.e. approve themselves as a supporter |
| `tasks_read_own` / `tasks_insert_self` / `tasks_update_by_requester` | tasks | UPDATE their own task, every column — `status`, `prepay_amount_cents`, `assigned_to_id` |
| `worklogs_read` / `worklogs_insert` / `worklogs_update` | worklogs | INSERT and UPDATE their own worklogs — fabricate billable time |
| `msg_write` | messages | INSERT, granted to `public` |

All nine are in the 2026-07-11 baseline migration
(`20260711094158_remote_schema.sql`, lines 783–858) — **the same migration this
record cites as evidence**. No later migration added a policy. They were there
the whole time.

### Why the verification passed anyway

1. **The exposure test used the anon key; the policies were scoped to
   `authenticated`.** `curl /rest/v1/tasks` with the anon key returned `[]`
   because anon genuinely had no permissive policy. That is indistinguishable
   from deny-all, and this record even flagged it as "ambiguous alone" — then
   treated the second check as resolving the ambiguity, which it did not.
2. **`pg_tables.rowsecurity` answers a different question.** RLS being enabled
   says nothing about what policies permit. This record was accurate in writing
   "**(presumed)** zero policies"; the presumption was never discharged.
3. **The check that would have caught it was deferred.** "Residual check
   (non-blocking, do with next DB session): run the pg_policies query from
   I-01a." It was never run. Meanwhile "presumed" hardened into "verified
   2026-07-11" as the claim was copied into CLAUDE.md Rule 3 and S-10.

No evidence of exploitation: the only client code that ever used this path was
`app/src/services/Tasks.js`, which was orphaned (imported by nothing) and
tree-shaken out of the deployed bundle.

### Closed by

- `20260910150000_close_client_direct_table_access.sql` — drops the nine
  policies, revokes client grants on `tasks`/`worklogs`/`profiles`/`messages`,
  drops `current_user_id()`, deletes `Tasks.js`.
- `20260910160000_revoke_residual_client_grants.sql` — revokes the remaining
  inert grants so the invariant check returns zero rows rather than a list of
  believed-harmless exceptions.
- Preceded by `20260910130000_revoke_execute_on_security_definer_rpcs.sql` and
  `20260910140000_drop_legacy_ops_functions.sql`, which closed the parallel
  SECURITY DEFINER RPC surface.

### Lesson, now encoded in CLAUDE.md Rule 3

A security invariant must state **the query that proves it and the exact result
to expect**, so checking is mechanical and deferring is visible. A date is not
evidence, "presumed" is not "verified", and a negative result from the wrong
role proves nothing. The four I-01a queries now live in Rule 3; run those, not
an anon-key curl.
