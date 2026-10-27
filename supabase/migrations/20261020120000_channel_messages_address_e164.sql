-- D-16 R-3 (Arbiter ruling 2026-10-20): a phone number is personal data and
-- is stored in one format only, E.164. channel_identities.address already has
-- this CHECK; channel_messages.address did not.
--
-- No new table, no grant, no policy: CLAUDE.md Rule 3's four queries are
-- unaffected (expected 3 / 0 / 0 / 0 after apply).
--
-- Safe to apply: every existing row was checked on the staging branch before
-- this file was written (0 non-E.164 rows). NOT VALID is not used, so the
-- apply itself re-checks every row and fails rather than half-applying.
--
-- Reversible:
--   ALTER TABLE public.channel_messages DROP CONSTRAINT channel_messages_address_e164;
--
-- Deploy order (S-21): staging branch first; production with the D-16 go-live,
-- after 20261019120000_messaging_channel.sql.

ALTER TABLE public.channel_messages
  ADD CONSTRAINT channel_messages_address_e164
  CHECK (address ~ '^\+[1-9][0-9]{7,14}$');
