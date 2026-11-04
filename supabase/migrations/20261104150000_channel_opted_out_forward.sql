-- Messages from a number that stopped texts reach ops (2026-11-04). Before,
-- anything an opted-out number wrote other than START was recorded and seen
-- by no one. We still cannot text them back (TCPA, and Sendblue refuses), but
-- ops can read it and reach the person another way. Forwards are batched:
-- at most one ordinary alert per number per 10 minutes; an urgent one (a
-- request for a person, safety, a dispute or theft) always goes at once.
--
-- WHAT THIS ADDS (three columns on an existing channel table)
--
--   channel_identities.opted_out_forwarded_at       when ops were last sent
--                                                    this number's messages
--   channel_identities.opted_out_forwarded_through  the created_at of the
--                                                    newest message in that
--                                                    forward: the next batch
--                                                    is everything after it
--   channel_identities.opted_out_pending_since      a message is waiting for
--                                                    the window to pass; the
--                                                    hand-off sweep flushes it
--
-- All three are reset when the number opts out again or texts START.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): unchanged — the table keeps RLS
-- ENABLED, no policies of its own, no client grants. Both are re-asserted
-- below, idempotently, as the other channel migrations do. Go is the only
-- reader and writer.
--
-- Invariant check after applying — all four CLAUDE.md Rule 3 queries: 1 still
-- returns exactly the three deny-all policies and names no channel table;
-- 2–4 return nothing.
--
-- Deploy order: apply BEFORE the Go build that reads these columns. Without
-- them that build fails every message from an opted-out number (logged; the
-- message is still not answered) and the sweep logs an error each minute.
--
-- Reversible:
--   ALTER TABLE public.channel_identities
--     DROP COLUMN opted_out_forwarded_at,
--     DROP COLUMN opted_out_forwarded_through,
--     DROP COLUMN opted_out_pending_since;

ALTER TABLE public.channel_identities
  ADD COLUMN IF NOT EXISTS opted_out_forwarded_at timestamp with time zone,
  ADD COLUMN IF NOT EXISTS opted_out_forwarded_through timestamp with time zone,
  ADD COLUMN IF NOT EXISTS opted_out_pending_since timestamp with time zone;

CREATE INDEX IF NOT EXISTS idx_channel_identities_opted_out_pending
  ON public.channel_identities USING btree (opted_out_pending_since)
  WHERE opted_out_pending_since IS NOT NULL;

ALTER TABLE public.channel_identities ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_identities FROM PUBLIC, anon, authenticated;

COMMENT ON COLUMN public.channel_identities.opted_out_forwarded_at IS
  'When ops were last sent the messages of this opted-out number. Rate limit for the forward (one ordinary alert per 10 minutes). Go only (S-10).';
COMMENT ON COLUMN public.channel_identities.opted_out_forwarded_through IS
  'created_at of the newest message in the last forward to ops; the next batch is everything after it. Go only (S-10).';
COMMENT ON COLUMN public.channel_identities.opted_out_pending_since IS
  'Set while a message from this opted-out number waits for the forward window; the hand-off sweep flushes it. Go only (S-10).';
