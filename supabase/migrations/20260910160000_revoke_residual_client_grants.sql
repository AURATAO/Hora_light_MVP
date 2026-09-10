-- Revoke the last client-role grants on Hora tables, so the invariant can be
-- checked with a query that expects zero rows.
--
-- 20260910150000 closed the four tables that had permissive policies. Four more
-- still granted anon and authenticated full DML: reviews, task_gps_pings,
-- device_push_tokens and timesheets. Those grants are inert today — each table
-- has RLS enabled with zero policies, so every client read and write is already
-- refused — which is exactly why they were easy to leave alone.
--
-- Leaving them is how this class of bug survives. D-02 verified the lock by
-- checking pg_tables.rowsecurity and deferred the pg_policies check as
-- "residual, non-blocking"; that residual was never done, and nine permissive
-- policies sat in the baseline for two months. An invariant whose verification
-- query returns "eight rows, all believed harmless" is not an invariant — the
-- next reader has to re-derive which rows are the harmless ones, and one day
-- they won't.
--
-- After this, the I-01a grant check documented in CLAUDE.md Rule 3 returns zero
-- rows, and any row it ever returns again is unambiguously a finding.
--
-- No client touches these tables directly: a sweep of app/src and mobile/src
-- for supabase.from(/.rpc( finds no caller, and the deployed bundles contain no
-- PostgREST table access. Go connects as the owner `postgres` and is unaffected.

REVOKE ALL ON TABLE public.reviews            FROM PUBLIC, anon, authenticated;
REVOKE ALL ON TABLE public.task_gps_pings     FROM PUBLIC, anon, authenticated;
REVOKE ALL ON TABLE public.device_push_tokens FROM PUBLIC, anon, authenticated;
REVOKE ALL ON TABLE public.timesheets         FROM PUBLIC, anon, authenticated;

-- users, notifications and audit_logs were never granted to a client role;
-- they keep their explicit `deny all (client)` policies as a second lock.

NOTIFY pgrst, 'reload schema';
