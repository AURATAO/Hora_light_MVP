-- Stripe Phase 2b: the two payments values settlement introduces.
--
--   kind   += 'budget_increase'   a supplementary hold for an approved overage
--   status += 'capture_failed'    the task finished; the money did not move
--
-- WHY 'capture_failed' IS NOT 'failed'
--
-- 'failed' already means "the hold never landed" — a declined card at post
-- time, with nothing authorized and no task. 'capture_failed' is the opposite
-- end of the lifecycle: a hold that WAS authorized, on a task that IS
-- completed, where the capture itself did not go through (the authorization
-- lapsed after ~7 days, Stripe was unreachable, the intent moved underneath
-- us). The two need different ops responses — one is "tell the requester to
-- try another card", the other is "this task was worked and nobody has been
-- charged for it yet" — and collapsing them into one status would make the
-- second invisible inside the first.
--
-- Completion is deliberately NOT blocked by a capture failure (see
-- server/payments_settlement.go): a supporter must never be stuck unable to
-- finish a task because of a money problem. The row lands here instead, an
-- audit row is written and ops are emailed.
--
-- WHY 'budget_increase' IS A SECOND PAYMENT ROW AND NOT A BIGGER FIRST ONE
--
-- The preferred path for an approved budget increase is an INCREMENTAL
-- AUTHORIZATION on the existing intent — same hold, larger amount, one line on
-- the requester's statement. Most online card payments do not support it (it
-- is largely a card-present feature; Stripe answers with an error naming
-- exactly that), so the fallback is a second PaymentIntent authorized for the
-- shortfall, which every card supports. That row is a real, separate hold and
-- needs its own lifecycle, so it is a row with its own kind — which is what
-- the Phase 1 migration anticipated when it wrote the kind CHECK as a list of
-- one.
--
-- The partial unique index uq_payments_task_kind_live is on (task_id, kind),
-- so a task can carry one live 'task_payment' AND one live 'budget_increase'
-- at the same time without any change to it. A second approved increase
-- captures or releases the first supplementary hold before opening another —
-- enforced by that index, not by the handler remembering.
--
-- Reversible (only while no row uses the new values):
--   ALTER TABLE public.payments DROP CONSTRAINT payments_kind_check;
--   ALTER TABLE public.payments ADD CONSTRAINT payments_kind_check
--     CHECK (kind IN ('task_payment'));
--   ...and the same shape for payments_status_check.
--
-- Deploy order (S-21): apply BEFORE the Go build that writes these values.

DO $$
DECLARE
  kind_constraint   text;
  status_constraint text;
BEGIN
  -- The Phase 1 migration wrote both CHECKs inline, so Postgres named them.
  -- Found by definition rather than by assuming that name, because an inline
  -- CHECK's generated name is not part of anybody's contract.
  SELECT conname INTO kind_constraint
    FROM pg_constraint
   WHERE conrelid = 'public.payments'::regclass
     AND contype  = 'c'
     AND pg_get_constraintdef(oid) LIKE '%kind%task_payment%';

  IF kind_constraint IS NOT NULL THEN
    EXECUTE format('ALTER TABLE public.payments DROP CONSTRAINT %I', kind_constraint);
  END IF;

  SELECT conname INTO status_constraint
    FROM pg_constraint
   WHERE conrelid = 'public.payments'::regclass
     AND contype  = 'c'
     AND pg_get_constraintdef(oid) LIKE '%status%requires_auth%';

  IF status_constraint IS NOT NULL THEN
    EXECUTE format('ALTER TABLE public.payments DROP CONSTRAINT %I', status_constraint);
  END IF;
END
$$;

ALTER TABLE public.payments
  ADD CONSTRAINT payments_kind_check
  CHECK (kind IN ('task_payment','budget_increase'));

ALTER TABLE public.payments
  ADD CONSTRAINT payments_status_check
  CHECK (status IN ('requires_auth','authorized','captured','canceled','failed','capture_failed'));

COMMENT ON COLUMN public.payments.kind IS
  'task_payment — the pre-auth placed at post. budget_increase — a supplementary hold for an approved mid-task overage, used only when the card/intent refused an incremental authorization.';
COMMENT ON COLUMN public.payments.status IS
  'requires_auth -> authorized -> captured, or canceled / failed (the hold never landed) / capture_failed (the hold landed, the task completed, and the capture did not go through — ops must settle by hand).';

-- ── tasks: the settlement the requester is shown ───────────────────────────
--
-- receipt_amount_cents and receipt_photo_url landed in Phase 1. What was
-- missing is the split of what was actually captured, which the payments row
-- carries but the task page cannot join to without exposing the ledger. These
-- two are the read model for the settlement view, written at capture.
--
-- Denormalized on purpose and only ever written once, at settlement: the task
-- detail screen is read by two people on every poll, and joining the financial
-- ledger into that query — for the one number it needs — would put payments
-- one careless `select *` away from a client response.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS settled_time_cost_cents integer
    CHECK (settled_time_cost_cents IS NULL OR settled_time_cost_cents >= 0),
  ADD COLUMN IF NOT EXISTS settled_total_cents integer
    CHECK (settled_total_cents IS NULL OR settled_total_cents >= 0),
  ADD COLUMN IF NOT EXISTS settled_at timestamp with time zone;

COMMENT ON COLUMN public.tasks.settled_time_cost_cents IS
  'Time half of the settlement (base fee + billable minutes x rate), as captured. Written once, at completion.';
COMMENT ON COLUMN public.tasks.settled_total_cents IS
  'What the requester was actually charged: settled_time_cost_cents + the verified receipt, clamped to the authorized hold. NULL on a task that was never captured (payments off, or capture_failed).';
