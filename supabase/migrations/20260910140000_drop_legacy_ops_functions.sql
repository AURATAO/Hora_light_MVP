-- Drop the web-MVP-era ops functions. Go is the authorization layer, and from
-- here it is the only one.
--
-- Four of these did the ops panel's work as SECURITY DEFINER Postgres
-- functions: force_complete, cancel_task, adjust_time and get_ops_feed, each
-- opening with `perform public.assert_ops_admin()`. Three of them are now Go
-- handlers (server/admin_task_ops.go) at POST /admin/tasks/:id/{force-complete,
-- cancel,adjust-time}; get_ops_feed had already been replaced by the SQL that
-- /ops/feed builds against view_ops_tasks.
--
-- They had to go rather than merely being revoked, because they never worked.
-- assert_ops_admin reads auth.jwt(), and Go connects as the `postgres` role
-- over pgx where there is no JWT — so every call raised "not authorized", the
-- handler logged a 500, and the Adjust / Force / Cancel buttons did nothing for
-- the project's entire life. Production is unambiguous: audit_logs holds nine
-- TASK_REMOVED rows written by Go and zero FORCE_COMPLETED, CANCELLED or
-- TIME_ADJUSTED rows. Leaving them in place would leave three ways to
-- reintroduce that.
--
-- is_ops_admin goes with them, and that is the point of the exercise: it held a
-- second hardcoded copy of the ops allowlist which had *already* drifted from
-- the Go one (five emails here against six in server/main.go's ADMIN_EMAILS,
-- missing taoaura.lavoro@gmail.com). Two copies of an admin allowlist is one
-- too many — S-14 makes changing it a Tier 3 decision, which is not meaningful
-- if a stale duplicate lives in the database. ADMIN_EMAILS in Go is now the
-- single source of truth.
--
-- handle_new_user and whoami are simply dead: no trigger anywhere references
-- handle_new_user (the Go backend owns public.users via /auth/exchange, and
-- BACKEND_REFERENCE §1.1 records that there is no FK to auth.users), and whoami
-- has no caller in any client, server or edge function.
--
-- NOT dropped: public.current_user_id(). It is referenced by six live RLS
-- policies on public.tasks and public.worklogs — policies that should not exist
-- under CLAUDE.md Rule 3 and are being raised separately. Dropping the function
-- before that decision is made would take the policies with it.
--
-- Irreversible in the sense that the bodies are gone from the database; they
-- remain in git (supabase/migrations/20260711094158_remote_schema.sql) if any
-- ever needs to be read again. Nothing calls them, so there is no recovery
-- window to plan for.

DROP FUNCTION IF EXISTS public.force_complete(uuid);
DROP FUNCTION IF EXISTS public.cancel_task(uuid, text);
DROP FUNCTION IF EXISTS public.adjust_time(uuid, integer);
DROP FUNCTION IF EXISTS public.get_ops_feed(text, text);

-- After their only callers.
DROP FUNCTION IF EXISTS public.assert_ops_admin();
DROP FUNCTION IF EXISTS public.is_ops_admin();

-- Never had a caller.
DROP FUNCTION IF EXISTS public.handle_new_user();
DROP FUNCTION IF EXISTS public.whoami();
