-- Text bookings: one task per drafted errand (audit 2026-10-30, gap B.3).
-- Forward-only, additive; RLS unchanged (deny-all, Go only — S-10). No
-- grants: tasks stays Go-only.
--
-- tasks.booking_key is the idempotency key of a booking made by text: one
-- per drafted errand, reused on every retry of it. createTask looks a task
-- up by (requester_id, booking_key) before inserting, so a YES after a crash
-- or a Stripe timeout reuses the first attempt's task — and with it its
-- payments row and its preauth_<payment id> Stripe key — instead of making a
-- second task and a second hold. NULL for every task posted from the apps.
--
-- Deploy order: apply BEFORE the build that writes it (createTask with a
-- booking key). Reversal: drop the index, then the column.
ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS booking_key text;

CREATE UNIQUE INDEX IF NOT EXISTS tasks_requester_booking_key_key
  ON public.tasks (requester_id, booking_key)
  WHERE booking_key IS NOT NULL;

COMMENT ON COLUMN public.tasks.booking_key IS
  'Idempotency key of a text booking (one per drafted errand, reused on retry). NULL for app posts. Go only.';
