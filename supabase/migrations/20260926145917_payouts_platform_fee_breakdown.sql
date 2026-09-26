-- payouts: the platform fee, itemized on every row.
--
-- Until this migration the platform took nothing (BillingConfig
-- .ApplicationFeeBasisPoints = 0) and payouts.amount_cents was simply time
-- cost + reimbursed receipt. From 2026-09-26 the platform keeps 20% of a
-- task's SERVICE revenue — base fee, billable minutes, any approved time
-- extension — and NONE of a receipt reimbursement, which is the supporter's
-- own money fronted and paid back in full (BillingConfig.PlatformFeeBps,
-- D-14).
--
-- A single net amount on the row would make that unauditable: "$31.60" says
-- nothing about whether it was $39.50 of service less a fee, or $19.50 of
-- service and $12.40 of groceries. So the row carries the breakdown, and the
-- database checks that it reconciles:
--
--   amount_cents = service_cents - fee_cents + reimbursement_cents
--
-- NULLABLE, DELIBERATELY. Every row paid before this migration was paid at
-- 100% and is left exactly as it was — no backfill, no reinterpretation. All
-- four columns are NULL on those rows and NOT NULL on every row written after
-- (the CHECK forbids a partial breakdown), so "was this paid under the fee"
-- is answerable from the row itself. fee_bps records the rate in force when
-- the row was written, so a later change to the rate cannot misdescribe
-- history.
--
-- Rollback: ALTER TABLE public.payouts DROP CONSTRAINT payouts_breakdown_reconciles;
--           ALTER TABLE public.payouts DROP COLUMN service_cents, DROP COLUMN fee_cents,
--             DROP COLUMN reimbursement_cents, DROP COLUMN fee_bps;

ALTER TABLE public.payouts
  ADD COLUMN IF NOT EXISTS service_cents       integer,
  ADD COLUMN IF NOT EXISTS fee_cents           integer,
  ADD COLUMN IF NOT EXISTS reimbursement_cents integer,
  ADD COLUMN IF NOT EXISTS fee_bps             integer;

ALTER TABLE public.payouts DROP CONSTRAINT IF EXISTS payouts_breakdown_reconciles;
ALTER TABLE public.payouts
  ADD CONSTRAINT payouts_breakdown_reconciles CHECK (
    (service_cents IS NULL AND fee_cents IS NULL AND reimbursement_cents IS NULL AND fee_bps IS NULL)
    OR (
      -- Spelled out: a CHECK passes on NULL, so "fee_cents >= 0" alone would
      -- wave through a row with three columns set and the fourth missing.
      service_cents IS NOT NULL AND fee_cents IS NOT NULL
      AND reimbursement_cents IS NOT NULL AND fee_bps IS NOT NULL
      AND service_cents >= 0 AND fee_cents >= 0 AND reimbursement_cents >= 0 AND fee_bps >= 0
      AND fee_cents <= service_cents
      AND amount_cents = service_cents - fee_cents + reimbursement_cents
    )
  );

COMMENT ON COLUMN public.payouts.amount_cents IS
  'What the supporter receives: service_cents - fee_cents + reimbursement_cents (payouts_breakdown_reconciles). Rows with a NULL breakdown predate the platform fee and were paid at 100% of service + reimbursement. With separate charges and transfers the fee is simply money not transferred — Stripe application_fee_amount does not apply.';
COMMENT ON COLUMN public.payouts.service_cents IS
  'The GROSS service revenue this transfer pays for — base fee + billable minutes + approved extensions — before the platform fee. Undiscounted: a promo is the requester''s, not the supporter''s.';
COMMENT ON COLUMN public.payouts.fee_cents IS
  'The platform fee kept out of service_cents (fee_bps of the task''s whole service revenue, round half up, computed once per task and attributed to the first transfer that carries service). Never taken from a reimbursement.';
COMMENT ON COLUMN public.payouts.reimbursement_cents IS
  'The verified receipt paid back in full. The supporter''s own money, never commissioned.';
COMMENT ON COLUMN public.payouts.fee_bps IS
  'BillingConfig.PlatformFeeBps in force when the row was written (2000 = 20%). Recorded so a later rate change cannot misdescribe an old payout.';
