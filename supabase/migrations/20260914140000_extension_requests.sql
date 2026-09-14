-- Stripe Phase 2b: mid-task asks, and the bookkeeping the time cap needs.
--
-- WHAT THIS ADDS
--
--   public.extension_requests        a supporter's mid-task ask, and its answer
--   tasks.time_cap_warned_at         Layer 2 fired (once per task)
--   tasks.time_cap_reached_at        Layer 1 ceiling hit (once per task)
--   tasks.time_cap_ops_alerted_at    Layer 3 grace elapsed (once per task)
--
-- ONE TABLE, TWO KINDS — the decision, stated once here.
--
-- The plan called for `budget_increase_requests`, and the time-cap work landing
-- in the same phase needed the identical machinery: a supporter asks for more,
-- the requester gets one tap to say yes or no, five minutes of silence means
-- no, and whichever way it goes both sides are told and the authorized hold may
-- have to grow. The only thing that differs between the two is the unit of the
-- ask — cents or minutes — and whether a fallback is meaningful.
--
-- So: one table with `kind IN ('budget','time')`, and a nullable amount column
-- per kind rather than one polymorphic `amount` whose meaning you have to read
-- `kind` to know. A single integer column would have been smaller and would
-- have made `requested_cents` silently mean minutes on half the rows — exactly
-- the kind of unit collision that costs money. The CHECKs below make the
-- correct column mandatory for each kind and the wrong one impossible, so the
-- shape is enforced by the table rather than remembered by the handler.
--
-- The fallback fields ('buy alternative' / 'skip this item') are budget-only.
-- A time request has no fallback to choose: the supporter keeps working while
-- they wait, and an unanswered ask simply stops the meter — which is the
-- fallback, already built into the billing.
--
-- Security (S-10 / S-13): RLS ENABLED, ZERO policies, and the grants that
-- `CREATE TABLE` hands out by default explicitly revoked. See the payments
-- migration (20260911120000) for why the REVOKE is load-bearing here and not
-- boilerplate: this schema's ALTER DEFAULT PRIVILEGES grant anon and
-- authenticated full DML on every new table, and deny-all RLS is then the only
-- thing standing between a client and the row that decides what somebody's
-- card is authorized for.
--
-- Reversible:
--   ALTER TABLE public.tasks
--     DROP COLUMN time_cap_warned_at,
--     DROP COLUMN time_cap_reached_at,
--     DROP COLUMN time_cap_ops_alerted_at;
--   DROP TABLE public.extension_requests;
--
-- Deploy order (S-21): apply BEFORE the Go build that reads these.

CREATE TABLE IF NOT EXISTS public.extension_requests (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  task_id           uuid NOT NULL REFERENCES public.tasks(id),
  supporter_id      uuid NOT NULL REFERENCES public.users(id),

  kind              text NOT NULL CHECK (kind IN ('budget','time')),

  -- Exactly one of these is set, chosen by kind. Both are the ADDITIONAL
  -- amount asked for, never the new total: "I need $8 more" and "I need 15
  -- more minutes" are what the supporter types, and a delta cannot be
  -- misread as a total the way a total can be misread as a delta.
  requested_cents   integer CHECK (requested_cents   IS NULL OR requested_cents   > 0),
  requested_minutes integer CHECK (requested_minutes IS NULL OR requested_minutes > 0),

  reason            text,

  -- Budget only, and required there: what the supporter will do if the answer
  -- is no or never comes. Asked BEFORE the wait rather than after it, because
  -- the whole point of the five-minute timeout is that the supporter is stood
  -- in a shop and needs to act on silence.
  fallback          text CHECK (fallback IN ('buy_alternative','skip_item')),
  fallback_note     text,

  status            text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','approved','denied','expired')),

  created_at        timestamp with time zone NOT NULL DEFAULT now(),
  resolved_at       timestamp with time zone,
  -- Who answered. NULL on an expiry, which nobody answered — that is the
  -- difference between 'denied' and 'expired' recorded rather than inferred.
  resolved_by       uuid REFERENCES public.users(id),

  CONSTRAINT extension_requests_budget_shape CHECK (
    kind <> 'budget' OR (
      requested_cents   IS NOT NULL AND
      requested_minutes IS NULL     AND
      fallback          IS NOT NULL
    )
  ),
  CONSTRAINT extension_requests_time_shape CHECK (
    kind <> 'time' OR (
      requested_minutes IS NOT NULL AND
      requested_cents   IS NULL     AND
      fallback          IS NULL     AND
      fallback_note     IS NULL
    )
  ),
  CONSTRAINT extension_requests_resolution_shape CHECK (
    (status = 'pending') = (resolved_at IS NULL)
  )
);

-- The task screen reads "what is outstanding on this task" on every poll, from
-- both sides, and the 6h sweep reads "what is pending and old" across all of
-- them.
CREATE INDEX IF NOT EXISTS idx_extension_requests_task_id
  ON public.extension_requests USING btree (task_id);
CREATE INDEX IF NOT EXISTS idx_extension_requests_pending
  ON public.extension_requests USING btree (created_at)
  WHERE status = 'pending';

-- One pending ask per task PER KIND, enforced rather than checked.
--
-- Per kind, not per task: a supporter waiting on "can I spend $8 more" must
-- still be able to say "and I need 15 more minutes" — they are different
-- questions with different answers, and blocking the second on the first would
-- make the requester's silence about money also stop the clock. The handler
-- answers 409 on a duplicate of the SAME kind; this index is what makes two
-- concurrent taps impossible rather than merely unlikely.
CREATE UNIQUE INDEX IF NOT EXISTS uq_extension_requests_task_kind_pending
  ON public.extension_requests USING btree (task_id, kind)
  WHERE status = 'pending';

ALTER TABLE public.extension_requests ENABLE ROW LEVEL SECURITY;

-- Not boilerplate — see the header. CREATE TABLE above silently granted anon
-- and authenticated every DML privilege via this schema's ALTER DEFAULT
-- PRIVILEGES, which would let a client approve their own budget increase.
REVOKE ALL ON TABLE public.extension_requests FROM PUBLIC, anon, authenticated;

COMMENT ON TABLE public.extension_requests IS
  'A supporter''s mid-task ask for more budget (kind=budget, cents) or more time (kind=time, minutes), and the requester''s answer. Written only by the Go backend (server/extensions.go). RLS deny-all, zero policies (S-10) — a client-direct write here would be self-approval of a charge.';
COMMENT ON COLUMN public.extension_requests.requested_cents IS
  'ADDITIONAL cents asked for, not the new total. Approval adds this to tasks.shopping_budget_approved_cents.';
COMMENT ON COLUMN public.extension_requests.requested_minutes IS
  'ADDITIONAL minutes asked for, not the new total. Approval raises the task''s billable time ceiling by this much.';

-- ── tasks: the time cap's once-per-task latches ────────────────────────────
--
-- Three timestamps rather than three booleans: "when did we tell them" is the
-- question ops actually asks, and the grace window in Layer 3 is measured from
-- time_cap_reached_at, so the reached latch has to carry its own moment
-- anyway. A NULL means it has not happened.
--
-- Each is claimed with `UPDATE ... WHERE <col> IS NULL RETURNING`, so the
-- notification fires exactly once even when two GPS pings evaluate the cap in
-- the same second.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS time_cap_warned_at      timestamp with time zone,
  ADD COLUMN IF NOT EXISTS time_cap_reached_at     timestamp with time zone,
  ADD COLUMN IF NOT EXISTS time_cap_ops_alerted_at timestamp with time zone;

COMMENT ON COLUMN public.tasks.time_cap_warned_at IS
  'Layer 2: both parties were told ~BillingConfig.CapWarningLeadMinutes of the requested time remained. Latch — set once, never cleared.';
COMMENT ON COLUMN public.tasks.time_cap_reached_at IS
  'Layer 1: logged time reached the consented ceiling and billing stopped accruing. Also the clock Layer 3''s grace period is measured from.';
COMMENT ON COLUMN public.tasks.time_cap_ops_alerted_at IS
  'Layer 3: BillingConfig.GracePeriodMinutes elapsed past the cap with no approval and ops were emailed.';
