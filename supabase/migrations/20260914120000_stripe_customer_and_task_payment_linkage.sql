-- Stripe Phase 2a: the card on file, and the task→payment linkage that lets a
-- post place a hold before the task is visible to anyone.
--
-- Phase 1 shipped the payments ledger and a service layer that nothing called.
-- This migration adds the three things wiring it into posting needs:
--
--   users.stripe_customer_id     the Customer saved cards hang off
--   tasks.payment_id             which hold belongs to this task
--   status 'pending_payment'     a task that exists but is not yet posted
--
-- Deploy order (S-21): apply this BEFORE deploying the Go build that reads
-- these columns. The Go side is gated behind PAYMENTS_ENFORCED (default off),
-- so the two deploys are independent in both directions — this migration is
-- inert until that flag is flipped.
--
-- Reversible:
--   ALTER TABLE public.tasks DROP COLUMN payment_id;
--   ALTER TABLE public.users DROP COLUMN stripe_customer_id;
--   ALTER TABLE public.tasks DROP CONSTRAINT tasks_status_check;
--   ALTER TABLE public.tasks ADD CONSTRAINT tasks_status_check
--     CHECK (status = ANY (ARRAY['open','completed','cancelled','removed']));
--   (safe only once no row is in 'pending_payment' — see the sweep query at
--    the bottom of this file.)

-- ── users.stripe_customer_id ───────────────────────────────────────────────
--
-- One Stripe Customer per user, created lazily the first time they open the
-- card sheet — not at signup. Most accounts never add a card during beta, and
-- a Customer per signup is a permanent row in someone else's database for a
-- user who never transacts.
--
-- UNIQUE because the reverse lookup (webhook → user) has to be unambiguous,
-- and because two rows pointing at one Customer means two users sharing a
-- wallet. A partial index rather than a plain UNIQUE so the 97 existing NULLs
-- don't each need to be distinct.

ALTER TABLE public.users
  ADD COLUMN IF NOT EXISTS stripe_customer_id text;

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_stripe_customer_id
  ON public.users USING btree (stripe_customer_id)
  WHERE stripe_customer_id IS NOT NULL;

COMMENT ON COLUMN public.users.stripe_customer_id IS
  'Stripe Customer (cus_…) holding this user''s saved cards. Created lazily on first card add, never at signup. Written only by the Go backend (payments_cards.go).';

-- ── tasks.payment_id ───────────────────────────────────────────────────────
--
-- The forward half of a link payments.task_id already carries backwards. Both
-- directions exist on purpose: the webhook arrives holding a PaymentIntent and
-- needs the task, while every task read needs to answer "is this one funded?"
-- without a join against a table whose live-row predicate (status in
-- requires_auth/authorized) would have to be restated at each call site.
--
-- Nullable forever. A task posted with PAYMENTS_ENFORCED off has no hold and
-- never will, and that is not a defect to be backfilled — it is what those
-- rows are.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS payment_id uuid REFERENCES public.payments(id);

COMMENT ON COLUMN public.tasks.payment_id IS
  'The payments row holding this task''s pre-auth. NULL for tasks posted while PAYMENTS_ENFORCED was off — those have no hold and never will.';

-- ── status 'pending_payment' ───────────────────────────────────────────────
--
-- A task row that exists so the payments row can reference it, and nothing
-- more. It is NOT posted: no feed lists it, no supporter can accept it, and
-- the requester does not see it either.
--
-- Why a status rather than creating the task after the hold lands: payments.
-- task_id is NOT NULL, so the hold cannot be recorded before the task exists.
-- And payments.go writes its row BEFORE calling Stripe on purpose — a live
-- hold on someone's card with no row naming it is unrecoverable, where a row
-- naming an intent that may not exist is merely untidy. Those two together
-- force task → payment row → Stripe call.
--
-- Which leaves the window: for the ~1s the authorization takes, an 'open' task
-- would be in the supporters' feed, acceptable, and unfunded. If it were then
-- declined we would be deleting a task somebody had just accepted and been
-- notified about. 'pending_payment' closes that window by construction — the
-- task becomes 'open' in the same statement that records the authorization.
--
-- The status is transient (sub-second on the happy path) with exactly one
-- durable case: a 3DS challenge the requester never finished. Those rows are
-- the reason the sweep query below exists.

ALTER TABLE public.tasks DROP CONSTRAINT IF EXISTS tasks_status_check;
ALTER TABLE public.tasks ADD CONSTRAINT tasks_status_check
  CHECK (status = ANY (ARRAY[
    'open'::text,
    'pending_payment'::text,
    'completed'::text,
    'cancelled'::text,
    'removed'::text
  ]));

-- The feed indexes are all partial on status='open', so they simply do not see
-- these rows. This one serves the two queries that do: the stranded-3DS sweep
-- and the requester's own confirm call.
CREATE INDEX IF NOT EXISTS tasks_pending_payment_created_idx
  ON public.tasks USING btree (created_at DESC)
  WHERE status = 'pending_payment';

-- Stranded 3DS attempts, for ops:
--
--   select id, requester_id, created_at
--     from public.tasks
--    where status = 'pending_payment'
--      and created_at < now() - interval '1 hour';
--
-- Each is a requester who started a card authentication and closed the sheet.
-- Nothing is held (the intent never reached requires_capture), so there is no
-- money to release; the row is dead weight and safe to delete once its
-- payments row is gone.
