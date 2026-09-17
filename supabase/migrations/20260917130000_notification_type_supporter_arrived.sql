-- Live tracking: the one moment in the pre-clock-in window worth a push.
--
-- Its own migration, for the reason every notification_type migration in this
-- project is its own file: a new enum value cannot be USED in the transaction
-- that adds it, so the value has to be committed separately from the code that
-- writes it. This is the COMPLETED_SUPPORTER lesson (20260818150000) — that
-- type was emitted for months against an enum that did not contain it, and
-- notify.Create's INSERT failed into a caller that only logs, so every one of
-- those notifications was dropped silently.
--
-- SUPPORTER_ARRIVED fires at most once per task: the first pre-clock-in ping
-- that lands within 100m of the task location. The latch is
-- tasks.arrival_notified_at (20260917120000), not a count of rows here.
--
-- Reversible: enum values cannot be dropped in PostgreSQL. Reverting means
-- leaving the value in place and unused, which is inert.

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'SUPPORTER_ARRIVED';
