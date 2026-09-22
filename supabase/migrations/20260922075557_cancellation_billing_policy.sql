-- Cancellation billing policy: the base fee is the supporter's guarantee.
--
-- WHAT CHANGED, AND WHY IT NEEDS COLUMNS.
--
-- Until now a requester cancel paid the supporter only if a worklog session
-- had been closed first. Observed on the build 11 device run: a task was
-- cancelled with a 7-minute session still OPEN, the session was discarded,
-- the whole $12 hold was released, and the supporter — who had travelled and
-- was standing there working — was paid nothing. The accepted-but-never-
-- clocked-in case was worse still: the server refused the cancel outright and
-- the only way out was the ops panel.
--
-- The new rule is one sentence: once a supporter has committed, cancelling
-- still pays them. max(base fee, actual billable minutes), paid out in full.
-- Open, unaccepted tasks are unaffected — nobody has committed, so a cancel
-- is a free release, exactly as it has always been.
--
-- Three columns, all on tasks, all nullable, no backfill:
--
--   accepted_at             when the supporter took the task. The grace window
--                           (BillingConfig.CancelGraceMinutes, default 2) is
--                           measured from here, so it cannot be inferred from
--                           an audit row that may not exist. NULL on every
--                           task that predates this migration, which the Go
--                           side reads as "no grace window" — the conservative
--                           direction, since the alternative would hand a free
--                           cancel to a task accepted three weeks ago.
--
--   cancelled_assignee_id   who was assigned WHEN the cancel happened. The
--                           cancel path detaches the supporter (assigned_to_id
--                           is nulled, so a dead task stops showing up in
--                           their active list), and until now the only
--                           surviving link back was a worklog row. That is why
--                           a cancelled task vanished from the supporter's
--                           history entirely — and under the new rule a
--                           supporter can be PAID for a task they never
--                           clocked into, where no worklog exists at all and
--                           the task would be unreachable to the person it
--                           charged somebody for.
--
--   cancel_bill_cents       what the cancel actually billed, recorded at the
--                           moment it was decided. The settlement view used to
--                           re-derive this with its own rule ("a cancelled
--                           task with no worklogs owes nothing"), which was a
--                           second copy of the policy and is now wrong twice
--                           over: an accepted task with no worklog owes the
--                           base fee, and a task cancelled inside the grace
--                           window owes nothing even though it has sessions.
--                           Recording the number ends the disagreement. NULL
--                           on pre-migration rows, where the view falls back
--                           to the old rule — which was correct for them.
--
--   cancel_reason_code      the preset slug behind cancel_reason's text.
--                           cancel_reason is free text and always has been;
--                           relaying it verbatim to the other party leaks
--                           whatever the requester or an ops admin typed. The
--                           code is a closed set (server/cancel_reasons.go)
--                           and is the ONLY thing rendered to the counterparty.
--
-- Reversible:
--   ALTER TABLE public.tasks
--     DROP COLUMN accepted_at,
--     DROP COLUMN cancelled_assignee_id,
--     DROP COLUMN cancel_reason_code,
--     DROP COLUMN cancel_bill_cents;
--
-- Security: existing table, no new table, no policy, no grant. RLS stays on
-- with the three deny-all policies (CLAUDE.md Rule 3) — the Go backend
-- remains the only reader and writer.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS accepted_at timestamptz;

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS cancelled_assignee_id uuid;

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS cancel_reason_code text;

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS cancel_bill_cents integer;

COMMENT ON COLUMN public.tasks.accepted_at IS
  'When a supporter accepted. The free-cancellation grace window is measured from here; NULL means no grace window (pre-migration tasks).';

COMMENT ON COLUMN public.tasks.cancelled_assignee_id IS
  'Who was assigned at cancellation. Preserves the supporter''s read access and history entry after assigned_to_id is detached.';

COMMENT ON COLUMN public.tasks.cancel_bill_cents IS
  'What the cancel billed, recorded when it was decided. NULL on pre-migration rows. The settlement view renders this rather than re-deriving the policy.';

COMMENT ON COLUMN public.tasks.cancel_reason_code IS
  'Preset slug behind cancel_reason. The only cancellation reason ever shown to the counterparty — cancel_reason itself is free text.';

-- No FK on cancelled_assignee_id, deliberately. It is a historical record of
-- who was on the task at a moment in time, and it must survive whatever
-- happens to the user row afterwards — a supporter deleting their account
-- should not silently rewrite what a completed settlement says it paid for.
-- assigned_to_id carries the same shape for the same reason.

-- The supporter's history query is "cancelled tasks that were mine", which
-- reads this column and the status together.
CREATE INDEX IF NOT EXISTS idx_tasks_cancelled_assignee
  ON public.tasks USING btree (cancelled_assignee_id, created_at DESC)
  WHERE cancelled_assignee_id IS NOT NULL;
