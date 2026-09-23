-- Promo codes: requester-side fixed-amount discounts, absorbed by the platform.
--
-- WHAT THIS ADDS
--
--   public.promo_codes         the codes ops create: amount, window, limits
--   public.promo_redemptions   one row per (code, requester, task) — the fact
--                              that a task was posted under a code, and the
--                              discount it carries
--   payouts.funding            'charge' (funded by a captured payment — every
--                              row that exists today) or 'promo_subsidy' (the
--                              platform's own money, making the supporter
--                              whole for a discount the requester never paid)
--
-- THE MODEL, in one paragraph. A code takes a fixed number of cents off what
-- the REQUESTER pays: off the hold at post, and off the total at settlement,
-- never below $0. The SUPPORTER's pay is computed from the undiscounted
-- amounts and does not change — the discount is the platform's cost, not
-- theirs. That last sentence is why payouts.funding exists: a transfer tied to
-- the requester's charge (source_transaction) cannot exceed that charge, so
-- the part of the supporter's pay the discount ate is a second transfer, drawn
-- on the platform's own balance, and it needs a row that can name no funding
-- payment.
--
-- PER-USER-ONCE IS A UNIQUE INDEX, not a check in a handler. Two posts racing
-- with the same code cannot both redeem it; the loser's INSERT fails on
-- uq_promo_redemptions_user_code and the handler answers "already used".
-- Likewise one code per task (uq_promo_redemptions_task).
--
-- A redemption is written when the task is POSTED (inside the same
-- transaction as the task row) and DELETED when that post comes to nothing:
-- the hold was refused and the task discarded, or the task was cancelled free
-- (unaccepted, or inside the grace window) — nobody was charged, so the code
-- was not used. A cancel that charges keeps it: the discount was applied.
--
-- discount_cents on the redemption is a SNAPSHOT of the code's amount at the
-- moment of posting. Deactivating or editing a code afterwards must not change
-- the terms of a task that was posted under it.
--
-- Security (S-10 / S-13, CLAUDE.md Rule 3): both tables ship with their
-- control in THIS migration — RLS ENABLED, ZERO policies, and an explicit
-- REVOKE against the schema's ALTER DEFAULT PRIVILEGES (see the long note in
-- 20260911120000 for why the REVOKE is load-bearing and not boilerplate).
-- promo_redemptions is a money table: a client-direct INSERT here would be a
-- self-granted discount, and a client-direct read of promo_codes would list
-- every live code. Deny-all, no policies, Go only.
--
-- Invariant check after applying (CLAUDE.md Rule 3, all four queries): query 1
-- still returns exactly the three deny-all policies and names neither table;
-- 2–4 return nothing.
--
-- Reversible:
--   ALTER TABLE public.payouts DROP CONSTRAINT payouts_funding_shape;
--   DROP INDEX public.uq_payouts_promo_subsidy_task;
--   ALTER TABLE public.payouts DROP COLUMN funding;
--   ALTER TABLE public.payouts ALTER COLUMN payment_id SET NOT NULL;
--   DROP TABLE public.promo_redemptions;
--   DROP TABLE public.promo_codes;
--   (the payment_id SET NOT NULL fails while any promo_subsidy row exists —
--   delete those first; they are the platform's own transfers and are
--   reconcilable from Stripe by transfer id.)
--
-- Deploy order (S-21): apply BEFORE the Go build that reads these.

-- ── promo_codes ────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.promo_codes (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  -- Stored as typed by ops; matched case-insensitively (the unique index
  -- below is on lower(code)). Trimmed and non-empty by CHECK so a code can
  -- never be a string nobody can type.
  code             text NOT NULL CHECK (code = btrim(code) AND code <> ''),
  amount_cents     integer NOT NULL CHECK (amount_cents > 0),
  valid_from       timestamp with time zone,
  valid_until      timestamp with time zone,
  -- NULL means unlimited.
  max_redemptions  integer CHECK (max_redemptions IS NULL OR max_redemptions > 0),
  -- Only a requester with no previous task may use it.
  first_task_only  boolean NOT NULL DEFAULT false,
  active           boolean NOT NULL DEFAULT true,
  -- For ops: what the code is for ("Instagram launch", "support gesture").
  note             text,
  created_by       uuid REFERENCES public.users(id),
  created_at       timestamp with time zone NOT NULL DEFAULT now(),
  deactivated_at   timestamp with time zone,

  CONSTRAINT promo_codes_window CHECK (
    valid_from IS NULL OR valid_until IS NULL OR valid_until > valid_from
  ),
  CONSTRAINT promo_codes_active_shape CHECK (
    active = (deactivated_at IS NULL)
  )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_codes_code_ci
  ON public.promo_codes USING btree (lower(code));

ALTER TABLE public.promo_codes ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.promo_codes FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.promo_codes IS
  'Requester-side fixed-amount discounts, absorbed by the platform. Created and deactivated by ops through the Go backend (server/promo.go, /admin/promo-codes). RLS deny-all, zero policies (S-10).';
COMMENT ON COLUMN public.promo_codes.amount_cents IS
  'Fixed discount, off the hold at post and off the total at settlement, never below $0. The supporter''s pay is unaffected.';

-- ── promo_redemptions ──────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS public.promo_redemptions (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  promo_code_id   uuid NOT NULL REFERENCES public.promo_codes(id),
  user_id         uuid NOT NULL REFERENCES public.users(id),
  task_id         uuid NOT NULL REFERENCES public.tasks(id),
  -- The code's amount at the moment of posting. See the header.
  discount_cents  integer NOT NULL CHECK (discount_cents > 0),
  created_at      timestamp with time zone NOT NULL DEFAULT now()
);

-- Per-user-once, enforced rather than checked.
CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_redemptions_user_code
  ON public.promo_redemptions USING btree (promo_code_id, user_id);
-- One code per task.
CREATE UNIQUE INDEX IF NOT EXISTS uq_promo_redemptions_task
  ON public.promo_redemptions USING btree (task_id);
-- The ops list: redemption counts per code.
CREATE INDEX IF NOT EXISTS idx_promo_redemptions_code
  ON public.promo_redemptions USING btree (promo_code_id);

ALTER TABLE public.promo_redemptions ENABLE ROW LEVEL SECURITY;
REVOKE ALL ON TABLE public.promo_redemptions FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.promo_redemptions IS
  'One row per (code, requester, task): the task was posted under the code and carries this discount. Written inside the post transaction, deleted when the post comes to nothing (hold refused, or cancelled free). RLS deny-all, zero policies (S-10) — a client-direct INSERT here would be a self-granted discount.';
COMMENT ON COLUMN public.promo_redemptions.discount_cents IS
  'Snapshot of promo_codes.amount_cents at posting. What the task''s hold and settlement are discounted by (min(this, total), never below $0).';

-- ── payouts.funding ────────────────────────────────────────────────────────
--
-- Every existing row is funded by a captured payment and keeps saying so
-- (DEFAULT 'charge'). A promo_subsidy row is the platform paying the supporter
-- the part of their earnings the requester's discount did not cover; it names
-- no payment because no charge funded it, so payment_id becomes nullable and a
-- CHECK ties the two together: a charge-funded row must name its payment, a
-- subsidy row must not.
--
-- "One payout per payment" (uq_payouts_payment_id) is untouched — NULLs are
-- distinct in a unique index — and the subsidy gets its own idempotency claim:
-- one subsidy transfer per task.

ALTER TABLE public.payouts
  ADD COLUMN IF NOT EXISTS funding text NOT NULL DEFAULT 'charge'
    CHECK (funding IN ('charge','promo_subsidy'));

ALTER TABLE public.payouts ALTER COLUMN payment_id DROP NOT NULL;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'public.payouts'::regclass AND conname = 'payouts_funding_shape'
  ) THEN
    ALTER TABLE public.payouts
      ADD CONSTRAINT payouts_funding_shape
      CHECK ((funding = 'charge') = (payment_id IS NOT NULL));
  END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_payouts_promo_subsidy_task
  ON public.payouts USING btree (task_id)
  WHERE funding = 'promo_subsidy';

COMMENT ON COLUMN public.payouts.funding IS
  '''charge'': funded by the captured payment in payment_id (source_transaction). ''promo_subsidy'': the platform''s own money, covering the part of the supporter''s pay a promo discount took off the requester''s charge; payment_id is NULL and the transfer is drawn on the platform balance.';
