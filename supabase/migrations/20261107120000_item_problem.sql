-- Item problem on shopping tasks (Aura 2026-11-07). A supporter in a shop
-- finds an item out of stock and has found an alternative that fits the
-- budget: the requester is asked "1 for the alternative, 2 to skip it". The
-- same table and the same one-question-at-a-time queue as the budget and
-- time asks (server/extensions.go, internal/channel/approval.go), as a third
-- kind:
--
--   kind = 'item'      approved = buy the alternative, denied = skip it,
--                      expired = no answer in 5 minutes: the supporter
--                      decides, shown the requester's substitution
--                      preference. Nothing about the budget moves on any
--                      outcome: an alternative that costs more than the
--                      budget is a budget ask (kind='budget'), never this.
--
-- "Nothing similar" is a notice, not a question, and writes no row here.
--
-- WHAT THIS ADDS
--   item_name              what was out of stock ("oat milk")
--   store_name             where ("Trader Joe's"); '' when not given
--   alternative_name       what the supporter found instead
--   alternative_cents      its price, >= 0, never above the task's approved
--                          shopping budget (checked in Go: the ceiling lives
--                          on tasks and moves with budget approvals)
--   alternative_photo_url  optional, the canonical task-completions URL the
--                          app uploaded (signed per read, never stored signed)
--
-- The unique "one pending ask per task per kind" index already covers the
-- new kind: one open item question per task at a time.
--
-- Security: unchanged. RLS on, zero policies, no client grants (S-10).
--
-- Reversible (only once no kind='item' rows exist):
--   ALTER TABLE public.extension_requests DROP CONSTRAINT extension_requests_item_shape;
--   ALTER TABLE public.extension_requests DROP CONSTRAINT extension_requests_kind_check;
--   ALTER TABLE public.extension_requests ADD CONSTRAINT extension_requests_kind_check
--     CHECK (kind IN ('budget','time'));
--   ALTER TABLE public.extension_requests DROP COLUMN item_name, DROP COLUMN store_name,
--     DROP COLUMN alternative_name, DROP COLUMN alternative_cents, DROP COLUMN alternative_photo_url;

ALTER TABLE public.extension_requests
  ADD COLUMN IF NOT EXISTS item_name             text,
  ADD COLUMN IF NOT EXISTS store_name            text,
  ADD COLUMN IF NOT EXISTS alternative_name      text,
  ADD COLUMN IF NOT EXISTS alternative_cents     integer CHECK (alternative_cents IS NULL OR alternative_cents >= 0),
  ADD COLUMN IF NOT EXISTS alternative_photo_url text;

ALTER TABLE public.extension_requests DROP CONSTRAINT IF EXISTS extension_requests_kind_check;
ALTER TABLE public.extension_requests ADD CONSTRAINT extension_requests_kind_check
  CHECK (kind IN ('budget', 'time', 'item'));

ALTER TABLE public.extension_requests DROP CONSTRAINT IF EXISTS extension_requests_item_shape;
ALTER TABLE public.extension_requests ADD CONSTRAINT extension_requests_item_shape CHECK (
  kind <> 'item' OR (
    requested_cents   IS NULL     AND
    requested_minutes IS NULL     AND
    fallback          IS NULL     AND
    fallback_note     IS NULL     AND
    item_name         IS NOT NULL AND
    alternative_name  IS NOT NULL AND
    alternative_cents IS NOT NULL
  )
);
