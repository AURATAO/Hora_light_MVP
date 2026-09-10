-- Close the client-direct path to Hora tables, so that CLAUDE.md Rule 1 and
-- S-01 are enforced by the database rather than only by convention.
--
-- WHAT WAS ACTUALLY THERE
--
-- The documented invariant (CLAUDE.md Rule 3, S-10, BACKEND_REFERENCE §1.3,
-- decision D-02) is "RLS enabled on every table with zero policies" — a
-- deny-all lock on the PostgREST side door, with Go as the only authorization
-- layer. That was not true. public carried twelve policies: the three
-- documented `deny all (client)` ones on users, notifications and audit_logs,
-- and nine PERMISSIVE grants to the `authenticated` role:
--
--   profiles_read_own        SELECT own profile row
--   profiles_update_own      UPDATE own profile row — every column, including
--                            is_verified_supporter. Self-approval as a
--                            supporter, bypassing /ops/supporter-approve.
--   tasks_read_own           SELECT own/assigned tasks
--   tasks_insert_self        INSERT tasks as self
--   tasks_update_by_requester UPDATE own task — every column, including status,
--                            prepay_amount_cents and assigned_to_id.
--   worklogs_read            SELECT worklogs on own/assigned tasks
--   worklogs_insert          INSERT worklogs for assigned tasks — i.e. write
--   worklogs_update          UPDATE them. Fabricating one's own billable time.
--   msg_write                INSERT into messages, granted to `public`.
--
-- These were live, not inert: anon and authenticated held
-- SELECT/INSERT/UPDATE/DELETE grants on all four tables, so the policies had
-- something to permit.
--
-- HOW IT DRIFTED
--
-- It did not. Every one of these policies is in the 2026-07-11 baseline
-- migration (20260711094158_remote_schema.sql, lines 783-858) — the same
-- migration D-02 cites when it records "verified 2026-07-11". No later
-- migration adds a policy. The claim was wrong when it was written: it was
-- never checked with a query, or the three `deny all (client)` policies were
-- seen and generalised to "zero policies". A date is not evidence. The doc
-- updates accompanying this migration replace the date with the query.
--
-- WHY IT IS SAFE TO REMOVE
--
-- Nothing uses this path. The only client code that ever did is
-- app/src/services/Tasks.js, deleted in the same commit — it was orphaned,
-- imported by nothing, and Vite tree-shook it out of the deployed bundle
-- (listOpenTasks/createTask appear zero times in app/dist). A sweep of app/src
-- and mobile/src for supabase.from(/.rpc( finds no other caller, and every
-- remaining supabase-js call is supabase.auth.*, which Rule 2 permits.
--
-- WHAT THIS LEAVES
--
-- tasks, worklogs, profiles and messages keep RLS enabled with zero policies,
-- which is deny-all and is the documented invariant. The three explicit
-- `deny all (client)` policies stay as they are. relforcerowsecurity stays
-- false on every table, which is what lets Go — connecting as the table owner
-- `postgres` — continue to bypass RLS entirely.
--
-- NOT TOUCHED: the storage.objects policies for the avatars bucket. Reading
-- public avatar URLs is the second of the two client-direct calls Rule 2
-- permits, and it lives in the storage schema, not public.
--
-- Reversible in principle (the definitions are in the baseline migration), but
-- restoring any of these would re-open a client-direct write path and violate
-- Rule 1. The intended direction is forward only.

-- ── 1. The nine permissive policies ────────────────────────────────────────

DROP POLICY IF EXISTS "tasks_read_own"            ON public.tasks;
DROP POLICY IF EXISTS "tasks_insert_self"         ON public.tasks;
DROP POLICY IF EXISTS "tasks_update_by_requester" ON public.tasks;

DROP POLICY IF EXISTS "worklogs_read"             ON public.worklogs;
DROP POLICY IF EXISTS "worklogs_insert"           ON public.worklogs;
DROP POLICY IF EXISTS "worklogs_update"           ON public.worklogs;

DROP POLICY IF EXISTS "profiles_read_own"         ON public.profiles;
DROP POLICY IF EXISTS "profiles_update_own"       ON public.profiles;

DROP POLICY IF EXISTS "msg_write"                 ON public.messages;

-- ── 2. The table grants that made them reachable ───────────────────────────
--
-- Dropping the policies alone would be enough while RLS stays on, but the
-- grants are the other half of the door and there is no reason for a client
-- role to hold DML on these tables at all. anon is revoked alongside
-- authenticated: it had the same grants and only lacked a policy to use them.
-- PUBLIC is named on messages because msg_write was written for that role.

REVOKE ALL ON TABLE public.tasks    FROM anon, authenticated;
REVOKE ALL ON TABLE public.worklogs FROM anon, authenticated;
REVOKE ALL ON TABLE public.profiles FROM anon, authenticated;
REVOKE ALL ON TABLE public.messages FROM PUBLIC, anon, authenticated;

-- ── 3. The helper the policies existed for ─────────────────────────────────
--
-- current_user_id() resolves auth.uid() to a public.users row. It was held back
-- from 20260910140000_drop_legacy_ops_functions.sql precisely because those six
-- task/worklog policies depended on it. They are gone as of step 1, so it can
-- follow the rest of the web-MVP-era helpers. Nothing in Go, the webapp, the
-- mobile app or the edge functions references it.

DROP FUNCTION IF EXISTS public.current_user_id();

-- PostgREST caches which roles may touch which relations.
NOTIFY pgrst, 'reload schema';
