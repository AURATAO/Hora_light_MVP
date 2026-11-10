-- The price schedule as data, and every task's price locked at post
-- (Aura 2026-11-10; server/pricing.go).
--
-- 1. public.pricing_schedules — append-only. The newest row IS the schedule;
--    the rows behind it are the history (who changed what, when, and why).
--    The server reads it at start and every minute, and an admin writes a new
--    row from the Ops page (POST /admin/pricing). Seeded with the margin
--    schedule decided today: $15 base (first 15 min included), $25
--    companionship, $0.60/min, $1.00/min evening, 30% platform fee.
--
-- 2. tasks.base_fee_cents, tasks.platform_fee_bps — the two prices a task did
--    not yet store (rate_cents_per_min already was, since 20260915130000).
--    Written at post from the live schedule; every later price of the task
--    reads them. A changed schedule therefore never re-prices a task in
--    flight, never makes a settlement exceed its hold, and never labels a
--    payout with a fee it was not cut at.
--
--    BACKFILL: every task that exists when this runs was posted under the
--    schedule in force until today — $12.00 base ($25.00 companionship) and
--    a 20% fee — so that is what is written onto it. Completed tasks keep
--    showing what they were billed; open ones settle at what they were
--    quoted. The server reads a NULL the same way (the legacy schedule), so a
--    task inserted by the old build between this migration and the deploy is
--    also billed as it was quoted.
--
-- Security: RLS on, no policies, no client grants (S-10, CLAUDE.md Rule 3).
--
-- Reversible:
--   ALTER TABLE public.tasks DROP COLUMN base_fee_cents, DROP COLUMN platform_fee_bps;
--   DROP TABLE public.pricing_schedules;

CREATE TABLE IF NOT EXISTS public.pricing_schedules (
  id                           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  base_fee_default_cents       integer NOT NULL CHECK (base_fee_default_cents BETWEEN 1 AND 20000),
  base_fee_companionship_cents integer NOT NULL CHECK (base_fee_companionship_cents BETWEEN 1 AND 20000),
  per_minute_rate_cents        integer NOT NULL CHECK (per_minute_rate_cents BETWEEN 1 AND 1000),
  surge_rate_cents_per_min     integer NOT NULL CHECK (surge_rate_cents_per_min BETWEEN 1 AND 1000),
  platform_fee_bps             integer NOT NULL CHECK (platform_fee_bps BETWEEN 0 AND 6000),
  note                         text,
  created_by                   uuid REFERENCES public.users(id) ON DELETE SET NULL,
  created_at                   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT pricing_schedules_surge_not_below_standard CHECK (surge_rate_cents_per_min >= per_minute_rate_cents)
);

CREATE INDEX IF NOT EXISTS pricing_schedules_created_at_idx ON public.pricing_schedules (created_at DESC);

ALTER TABLE public.pricing_schedules ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON public.pricing_schedules FROM anon, authenticated, PUBLIC;

INSERT INTO public.pricing_schedules
  (base_fee_default_cents, base_fee_companionship_cents, per_minute_rate_cents,
   surge_rate_cents_per_min, platform_fee_bps, note)
SELECT 1500, 2500, 60, 100, 3000,
       'Margin schedule: $15 base (first 15 min), $0.60/min, 30% fee. Companionship and evening unchanged. (Aura 2026-11-10)'
WHERE NOT EXISTS (SELECT 1 FROM public.pricing_schedules);

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS base_fee_cents   integer CHECK (base_fee_cents IS NULL OR base_fee_cents >= 0),
  ADD COLUMN IF NOT EXISTS platform_fee_bps integer CHECK (platform_fee_bps IS NULL OR platform_fee_bps BETWEEN 0 AND 10000);

UPDATE public.tasks
   SET base_fee_cents   = CASE WHEN category IN ('companionship', 'companion') THEN 2500 ELSE 1200 END,
       platform_fee_bps = 2000
 WHERE base_fee_cents IS NULL;
