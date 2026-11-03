-- Phoenix trace deletion queue (D-22). When an account is deleted, its chat
-- conversations' traces are deleted from our self-hosted Phoenix. The account
-- deletion queues one row per conversation in the same transaction that
-- purges the chat rows (PurgeUserTx), and a Go sweeper calls Phoenix's
-- DELETE /v1/sessions/{session_id} until it succeeds — so a Phoenix outage
-- delays the deletion, never loses it.
--
-- WHAT THIS ADDS
--
--   public.trace_deletions   session_id is the conversation (channel
--                            identity) id the traces were tagged with — an
--                            opaque uuid, not personal data; the identity row
--                            itself is already gone when this is read. No
--                            message text, no number, no user id is stored.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): RLS ENABLED, ZERO policies, and an
-- explicit REVOKE from PUBLIC/anon/authenticated (load-bearing against the
-- schema's default privileges; see 20260911120000). Go is the only reader and
-- writer.
--
-- Invariant check after applying — all four CLAUDE.md Rule 3 queries: 1 still
-- returns exactly the three deny-all policies and names not this table; 2–4
-- return nothing.
--
-- Reversible: DROP TABLE public.trace_deletions;  (pending deletions are then
-- lost: run it only with the table empty.)
--
-- Deploy order: may be applied before or after the Go build that reads it.
-- Without the table, account deletion skips the queue (to_regclass probe, as
-- PurgeUserTx does) and logs that traces were left to the retention policy.

CREATE TABLE IF NOT EXISTS public.trace_deletions (
  session_id      text        PRIMARY KEY,
  requested_at    timestamptz NOT NULL DEFAULT now(),
  -- The first attempt waits a few minutes, so spans still in the exporter's
  -- queue (a message routed while the account was deleted) land first.
  next_attempt_at timestamptz NOT NULL DEFAULT now() + interval '3 minutes',
  attempts        integer     NOT NULL DEFAULT 0,
  last_status     integer,
  CONSTRAINT trace_deletions_session_id_not_blank CHECK (length(session_id) > 0)
);

CREATE INDEX IF NOT EXISTS trace_deletions_next_attempt_idx
  ON public.trace_deletions (next_attempt_at);

ALTER TABLE public.trace_deletions ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.trace_deletions FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.trace_deletions IS
  'Phoenix sessions (conversation ids) whose traces must be deleted after an account deletion (D-22). Go only.';
COMMENT ON COLUMN public.trace_deletions.last_status IS
  'Phoenix HTTP status of the last failed attempt; 0 for a transport error.';
