## Database Access Rules

Non-negotiable, for all client code (`app/src/` and future `mobile/`).
Authoritative version: `skills/constitution/STANDARDS.md` S-01, S-10, S-11.

### Rule 1 — All Hora table reads/writes go through the Go backend API

All reads and writes to Hora MVP tables (`users`, `profiles`, `tasks`,
`worklogs`, `notifications`, `task_gps_pings`, `reviews`) **MUST** go through
the Go backend API. Never use the Supabase JS client, PostgREST, or any
direct database connection from client code for these tables.

### Rule 2 — Permitted direct Supabase calls from clients

Exactly two:
- `supabase.auth.*` (signInWithOtp, verifyOtp, getSession, onAuthStateChange, signOut, etc.)
- Reading public avatar URLs from the `avatars` storage bucket

### Rule 3 — RLS is a lock, not an authorization layer

Every table has RLS **enabled**, no client role holds any DML grant, and the
only policies that exist are three explicit `deny all (client)` ones. This
blocks all anon/authenticated PostgREST access; it authorizes nothing. The Go
backend is the ONLY authorization layer — it connects as the owner `postgres`
over pgx and bypasses RLS entirely (`relforcerowsecurity` must stay false).

Never add an RLS policy to "let the client read something", and never grant a
client role DML on a Hora table — either one opens a client-direct path and
violates Rule 1.

**This claim carries a query, not a date.** Run all four; 1 returns exactly
three rows, 2–4 return none. Any other result is a finding:

```sql
-- 1. Policies. Exactly three, all deny-all.
select tablename, policyname, roles::text, cmd, qual, with_check
from pg_policies where schemaname = 'public';

-- 2. Client DML grants. None.
select table_name, grantee, privilege_type
from information_schema.role_table_grants
where table_schema = 'public'
  and grantee in ('anon', 'authenticated', 'PUBLIC')
  and privilege_type in ('SELECT', 'INSERT', 'UPDATE', 'DELETE');

-- 3. Tables with RLS off. None.
select c.relname from pg_class c join pg_namespace n on n.oid = c.relnamespace
where n.nspname = 'public' and c.relkind = 'r' and not c.relrowsecurity;

-- 4. SECURITY DEFINER functions a client can call. None.
select p.proname from pg_proc p join pg_namespace n on n.oid = p.pronamespace
where n.nspname = 'public' and p.prosecdef
  and (has_function_privilege('anon', p.oid, 'EXECUTE')
       or has_function_privilege('authenticated', p.oid, 'EXECUTE'));
```

Out of scope for these checks, and deliberately so: the `storage` schema's six
`objects` policies, which serve the public `avatars` bucket reads Rule 2
permits.

#### Why the query, and not a date (2026-09-10)

From 2026-07-11 to 2026-09-10 this rule said "zero policies, verified
2026-07-11". It was wrong the day it was written. Nine PERMISSIVE policies were
sitting in the baseline migration (`20260711094158_remote_schema.sql`,
lines 783–858) — the *same* migration D-02 cited as evidence — granting
`authenticated` direct UPDATE on their own `profiles` row (every column,
`is_verified_supporter` included: self-approval as a supporter), direct UPDATE
on their own `tasks` row (`status`, `prepay_amount_cents`, `assigned_to_id`),
and INSERT/UPDATE on their own `worklogs` (fabricating billable time). The
matching table grants were live.

Three things had to line up for that to stay invisible for two months:

1. **The exposure test used the anon key.** The permissive policies were scoped
   to `authenticated`. `curl /rest/v1/tasks` with the anon key correctly
   returned `[]` — which reads exactly like deny-all and is not.
2. **`pg_tables.rowsecurity` was checked; `pg_policies` was not.** RLS being
   *on* says nothing about what policies permit. D-02 recorded the conclusion
   as "(presumed) zero policies" — the hedge was right there in the word
   *presumed*.
3. **The real check was deferred.** D-02 filed it as "Residual check
   (non-blocking, do with next DB session): run the pg_policies query from
   I-01a". It was never run, and "presumed" hardened into "verified" as the
   claim was copied into this file and S-10.

Closed by `20260910150000_close_client_direct_table_access.sql` and
`20260910160000_revoke_residual_client_grants.sql`. The lesson is the format of
this rule: a security invariant states the query that proves it and the exact
result to expect, so that checking it is mechanical and deferring it is
visible.

### Rule 4 — Why

The Go backend connects directly as the `postgres` role (bypassing RLS) and
enforces all permissions in application code. Client-held anon keys hit the
deny-all wall; identity for business logic is the internal `users.id` UUID,
obtained via the `/auth/exchange` flow — never a raw Supabase token `sub`.