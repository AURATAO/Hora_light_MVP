-- What a supporter confirmed before accepting a task (PLAN 13c, Aura
-- 2026-11-16). One row per item accepted: who, which item, which version of
-- its wording (and a hash of the words shown), for which task, when.
--
-- The wording lives in server/accept_acks.json. The server writes a row when
-- an accept carries an acknowledgment; ACCEPT_ACK_ENFORCED=1 makes them
-- required.
--
-- Evidence outlives the task: task_id is SET NULL when a task is deleted.
-- It does not outlive the account (ON DELETE CASCADE), like the rest of a
-- deleted user's rows.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): RLS ENABLED, no policies, no
-- client grants; the REVOKE is load-bearing against the schema's default
-- privileges. Go is the only reader and writer.
--
-- Invariant check after applying: all four CLAUDE.md Rule 3 queries. 1 still
-- returns exactly the three deny-all policies; 2-4 return nothing.
--
-- Deploy order: apply BEFORE the build that reads it. Without the table and
-- with ACCEPT_ACK_ENFORCED unset, an accept that carries acknowledgments
-- logs the error and still accepts; GET accept-requirements answers an
-- empty list.
--
-- Reversible:
--   DROP TABLE public.supporter_acknowledgments;

CREATE TABLE IF NOT EXISTS public.supporter_acknowledgments (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  task_id      uuid REFERENCES public.tasks(id) ON DELETE SET NULL,
  ack_key      text NOT NULL,
  version      text NOT NULL,
  text_sha256  text NOT NULL,
  accepted_at  timestamptz NOT NULL DEFAULT now()
);

-- "Has this supporter accepted this item at this version?"
CREATE INDEX IF NOT EXISTS supporter_acknowledgments_lookup_idx
  ON public.supporter_acknowledgments (user_id, ack_key, version);

ALTER TABLE public.supporter_acknowledgments ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON public.supporter_acknowledgments FROM anon, authenticated, PUBLIC;
