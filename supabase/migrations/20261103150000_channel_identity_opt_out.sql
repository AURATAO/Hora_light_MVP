-- Text opt-out on a channel identity (2026-11-03). STOP, QUIT, END, REVOKE,
-- OPT OUT, CANCEL and UNSUBSCRIBE are carrier opt-out keywords (FCC list,
-- Sendblue's list). Sendblue honours them on its own: once a number has sent
-- one, every send to it fails with 400 OPTED_OUT until the person texts
-- START. On 2026-11-03 a test number texted "Cancel" (our own copy told
-- people to) and every later send failed while HO:RA carried on as if it
-- were talking to them.
--
-- WHAT THIS ADDS (one column on an existing channel table)
--
--   channel_identities.opted_out_at   when the number stopped texts from us:
--                                     the person sent an opt-out keyword, or
--                                     Sendblue refused a send with OPTED_OUT.
--                                     NULL = we may text it. Cleared when the
--                                     person texts START. The Go router sends
--                                     nothing to a number while it is set.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): unchanged — the table keeps RLS
-- ENABLED, ZERO policies, no client grants. Both are re-asserted below,
-- idempotently, as the other channel migrations do. Go is the only reader and
-- writer (the app learns the flag through GET /profile and GET /tasks/:id).
--
-- Invariant check after applying — all four CLAUDE.md Rule 3 queries: 1 still
-- returns exactly the three deny-all policies and names no channel table;
-- 2–4 return nothing.
--
-- Deploy order: apply BEFORE the Go build that reads opted_out_at
-- (UpsertIdentity returns it; every send checks it). Without the column that
-- build fails every inbound message and every send.
--
-- Reversible: ALTER TABLE public.channel_identities DROP COLUMN opted_out_at;
-- (the numbers that opted out are then texted again: Sendblue still refuses
-- them, but nothing on our side knows.)

ALTER TABLE public.channel_identities
  ADD COLUMN IF NOT EXISTS opted_out_at timestamp with time zone;

CREATE INDEX IF NOT EXISTS idx_channel_identities_opted_out_user
  ON public.channel_identities USING btree (user_id)
  WHERE opted_out_at IS NOT NULL AND user_id IS NOT NULL;

ALTER TABLE public.channel_identities ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_identities FROM PUBLIC, anon, authenticated;

COMMENT ON COLUMN public.channel_identities.opted_out_at IS
  'When the number opted out of texts (an opt-out keyword, or the provider refusing a send with OPTED_OUT). NULL = may be texted. Cleared by START. Go only (S-10).';
