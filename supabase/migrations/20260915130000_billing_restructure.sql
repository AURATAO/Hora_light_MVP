-- Billing restructure: reserve exactly what the requester sees, settle
-- overages at completion, and price evening work at its own rate.
--
-- THREE CHANGES, one theme. The hold used to be 1.5x the time estimate plus
-- the budget plus $5, which meant a requester quoted $19.50 watched $34.25
-- vanish from their available balance with nothing anywhere explaining the
-- gap. It is now exactly the estimate plus the budget — the number on the
-- confirmation and the number on the statement are the same number. What that
-- gives up is the guarantee that a capture always fits inside its hold, so
-- this migration also opens the two payments values that let a completion
-- collect the difference afterwards.
--
--   1. tasks.rate_cents_per_min       the rate, resolved once at post
--   2. drop tasks_prepay_amount_cents_cap    no ceiling on a shopping budget
--   3. payments.kind   += 'completion_balance'
--      payments.status += 'balance_due'
--
-- Reversible:
--   ALTER TABLE public.tasks DROP COLUMN rate_cents_per_min;
--   ALTER TABLE public.tasks ADD CONSTRAINT tasks_prepay_amount_cents_cap
--     CHECK (prepay_amount_cents <= 3000) NOT VALID;
--   ...and narrow the two payments CHECKs back, while no row uses the values.
--
-- Deploy order (S-21): apply BEFORE the Go build that reads these.

-- ── 1. The rate, stored per task ───────────────────────────────────────────
--
-- Resolved ONCE, at post, from the task's scheduled start (posting time for an
-- ASAP task) in America/New_York, and never re-derived. Deriving it at read
-- time would re-price a task in flight: a 20:50 job running to 21:10 would
-- settle partly at a rate nobody quoted, and the same task would cost
-- different amounts depending on when a screen happened to be opened. A price
-- is a term of an agreement; it is fixed when the agreement is made.
--
-- DEFAULT 50 and backfilled to 50, not NULL-and-interpret-later: every task
-- that exists was quoted, worked and settled at $0.50/min, so 50 is the
-- historically correct value rather than a guess. server/billing.go's
-- normalizeRate still treats a zero or NULL as the standard rate, because a
-- read path that can return "free" is worse than one that can return "cheap".

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS rate_cents_per_min integer NOT NULL DEFAULT 50
    CHECK (rate_cents_per_min > 0);

COMMENT ON COLUMN public.tasks.rate_cents_per_min IS
  'Per-minute rate past the included block, resolved once at post from the scheduled start (America/New_York) by server/billing.go resolveRateCentsPerMin. NEVER re-resolved — estimate, ceilings, settlement and copy all read this column.';

-- ── 2. No ceiling on a shopping budget ─────────────────────────────────────
--
-- The $30 cap was the wrong shape of protection. It was written when the hold
-- was a multiple of the estimate and the budget was an abstract "authorization
-- ceiling" nobody had been charged against; now the entire budget is reserved
-- on the card at post, so the requester sees and authorizes the exact number
-- before anything happens. The cap only refused legitimate tasks.
--
-- What replaces it warns rather than refuses: the post form shows a red notice
-- above BillingConfig.HighBudgetWarningCents ($500) and still submits.

ALTER TABLE public.tasks
  DROP CONSTRAINT IF EXISTS tasks_prepay_amount_cents_cap;

-- ── 3. Collecting what the hold did not cover ──────────────────────────────
--
-- 'completion_balance' — a second, immediate-capture charge for the part of a
-- settlement that outgrew the hold. Not a hold: the task is over and the
-- amount is known exactly, so there is nothing to reserve and release.
--
-- 'balance_due' — that charge failed. The task is STILL completed and the
-- supporter is STILL paid; the requester owes the difference and is blocked
-- from posting until they settle it. Distinct from 'capture_failed', where
-- nothing at all was collected: these need different messages and different
-- ops responses.
--
-- 'budget_increase' stays in the kind list. No new row will use it — approvals
-- no longer authorize anything — but rows written during Phase 2b may exist
-- and a CHECK that retroactively invalidates history is not a constraint, it
-- is data loss waiting for an UPDATE.

DO $$
DECLARE
  kind_constraint   text;
  status_constraint text;
BEGIN
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
  CHECK (kind IN ('task_payment','budget_increase','completion_balance'));

ALTER TABLE public.payments
  ADD CONSTRAINT payments_status_check
  CHECK (status IN ('requires_auth','authorized','captured','canceled','failed',
                    'capture_failed','balance_due'));

COMMENT ON COLUMN public.payments.kind IS
  'task_payment — the pre-auth placed at post, now exactly estimate + budget. completion_balance — an immediate charge for the part of a settlement that outgrew the hold. budget_increase — legacy (Phase 2b); approvals no longer authorize anything.';
COMMENT ON COLUMN public.payments.status IS
  'requires_auth -> authorized -> captured, or canceled / failed (the hold never landed) / capture_failed (the hold landed, the task completed, the capture did not go through) / balance_due (a completion_balance charge failed — the requester owes it and cannot post until they settle).';

-- The outstanding-balance lookup: "does this requester owe anything", run on
-- every post. Partial, because balance_due is rare by construction.
CREATE INDEX IF NOT EXISTS idx_payments_balance_due
  ON public.payments USING btree (requester_id)
  WHERE status = 'balance_due';
