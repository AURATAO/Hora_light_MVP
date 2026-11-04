-- Place discovery in the text channel (D-23, phase 1, 2026-11-04). A
-- customer asks HO:RA to find something in NYC; the answer lists places from
-- Google Maps and a reply with a number turns one into a normal booking.
--
-- WHAT THIS ADDS (one new channel table, one row per conversation)
--
--   channel_discovery.offer        what the last answer left open: the
--                                  person's words for the search, the
--                                  Google place ids of each option shown
--                                  (ids ONLY — Google's Places policy allows
--                                  storing place ids, not names, addresses
--                                  or hours), or "where should I look?".
--                                  NULL when nothing is open.
--   channel_discovery.places_day   the New York date the count is for
--   channel_discovery.places_calls Places searches made for this
--                                  conversation that day — the per-person
--                                  daily cap (CHANNEL_PLACES_PER_DAY)
--
-- Deleted with the conversation's identity (ON DELETE CASCADE), like every
-- other channel row.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): RLS ENABLED, no policies, no
-- client grants — the REVOKE is load-bearing against the schema's default
-- privileges. Go is the only reader and writer.
--
-- Invariant check after applying — all four CLAUDE.md Rule 3 queries: 1 still
-- returns exactly the three deny-all policies and names no channel table;
-- 2–4 return nothing.
--
-- Deploy order: apply BEFORE a build that runs with CHANNEL_DISCOVERY=1.
-- With the flag off nothing reads the table. With the flag on and no table,
-- every discovery question is answered "I can't search right now" (the
-- daily count cannot be taken, so no search runs) and picks are not
-- remembered.
--
-- Reversible:
--   DROP TABLE public.channel_discovery;

CREATE TABLE IF NOT EXISTS public.channel_discovery (
  identity_id   uuid PRIMARY KEY REFERENCES public.channel_identities(id) ON DELETE CASCADE,
  offer         jsonb,
  places_day    date,
  places_calls  integer NOT NULL DEFAULT 0 CHECK (places_calls >= 0),
  updated_at    timestamp with time zone NOT NULL DEFAULT now()
);

ALTER TABLE public.channel_discovery ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.channel_discovery FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.channel_discovery IS
  'Place discovery state per text conversation: the open offer (Google place ids only) and the daily Places call count. Go only (S-10).';
