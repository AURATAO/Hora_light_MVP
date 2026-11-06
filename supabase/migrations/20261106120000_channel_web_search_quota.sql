-- Errand playbooks (D-24, 2026-11-06): the per-person daily count of web
-- searches the text channel makes to answer a question about a place
-- (CHANNEL_WEB_SEARCH_PER_DAY). Two columns on the existing
-- channel_discovery row, counted like places_calls.
--
-- WHAT THIS ADDS
--
--   channel_discovery.web_day    the New York date the count is for
--   channel_discovery.web_calls  web searches made for this conversation
--                                that day
--
-- No new table, no grant, no policy: channel_discovery keeps RLS on with no
-- policies and no client grants (CLAUDE.md Rule 3). The four Rule 3
-- queries must still return 3 / 0 / 0 / 0 after this.
--
-- Deploy order: apply BEFORE a build that runs with CHANNEL_PLAYBOOKS=1.
-- Without these columns every web search is refused (the count cannot be
-- taken) and answers use Google's data only; nothing else breaks.
--
-- Reversible:
--   ALTER TABLE public.channel_discovery DROP COLUMN web_day, DROP COLUMN web_calls;

ALTER TABLE public.channel_discovery
  ADD COLUMN IF NOT EXISTS web_day   date,
  ADD COLUMN IF NOT EXISTS web_calls integer NOT NULL DEFAULT 0 CHECK (web_calls >= 0);
