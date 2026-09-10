-- Take the SECURITY DEFINER helpers off the anonymous PostgREST surface.
--
-- Seven functions in public are SECURITY DEFINER, which means they run as their
-- owner (postgres) and RLS does not apply to what they touch. All seven were
-- also executable through /rest/v1/rpc/<name> by anon and authenticated, so the
-- deny-all RLS described in CLAUDE.md Rule 3 — the lock on the PostgREST side
-- door — did not cover them. They were the one way in that the lock misses.
--
-- Nothing was actually exposed: adjust_time, cancel_task, force_complete and
-- get_ops_feed all open with `perform public.assert_ops_admin()`, which checks
-- auth.jwt()->>'email' against an allowlist and raises for an anonymous caller.
-- Verified against production before this migration: all four answered
-- "not authorized". whoami() did answer 200, but only ever reflects the
-- caller's own token back at them ({"uid":null,"email":null} for anon).
--
-- So this is defense in depth, not an incident. The guard is one `perform` away
-- from being edited out by accident, and it is a *second, already-stale* copy of
-- the ops allowlist (public.is_ops_admin lists five emails; the Go allowlist in
-- server/main.go lists six). Authorization belongs in Go — S-01, S-11, S-14 —
-- and these functions should not be reachable from a client at all.
--
-- THE SUBTLE PART: revoking from anon and authenticated alone is a no-op here.
-- The ACL on all seven reads `=X/postgres`, and the empty grantee is PUBLIC —
-- Postgres grants EXECUTE on new functions to PUBLIC by default. Every role is
-- a member of PUBLIC, so anon keeps EXECUTE through that grant no matter how
-- many times it is revoked by name. PUBLIC has to be revoked too, and it is
-- named first below for exactly that reason.
--
-- Safe for the backend. Go connects as `postgres` over pgx (SUPABASE_DB_URL),
-- not through PostgREST, and the owner's own grant (`postgres=X/postgres`) is
-- untouched by these statements. service_role keeps its explicit grant as well.
--
-- Forward-only. To reverse, GRANT EXECUTE back to the named roles — but not to
-- PUBLIC, which is how this surface appeared in the first place.

REVOKE EXECUTE ON FUNCTION public.adjust_time(uuid, integer) FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.assert_ops_admin()         FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.cancel_task(uuid, text)    FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.force_complete(uuid)       FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.get_ops_feed(text, text)   FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.handle_new_user()          FROM PUBLIC, anon, authenticated;
REVOKE EXECUTE ON FUNCTION public.whoami()                   FROM PUBLIC, anon, authenticated;

-- PostgREST caches the schema, including which functions a role may call.
-- REVOKE fires Supabase's DDL watch on its own; this is belt and braces so the
-- change takes effect immediately rather than at the next reload.
NOTIFY pgrst, 'reload schema';
