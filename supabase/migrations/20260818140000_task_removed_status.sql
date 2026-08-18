-- Platform takedown of a task ("removed"), distinct from requester cancellation.
--
--   cancelled = the requester withdrew their own task (cancelTask).
--   removed   = the HO:RA team took it down (out of beta scope, inappropriate…).
--
-- Both are terminal. Only 'open' tasks can be removed, so nothing needs a
-- backfill: every existing row keeps the status it has.

ALTER TABLE public.tasks DROP CONSTRAINT IF EXISTS tasks_status_check;
ALTER TABLE public.tasks ADD CONSTRAINT tasks_status_check
  CHECK (status = ANY (ARRAY['open'::text, 'completed'::text, 'cancelled'::text, 'removed'::text]));

-- Mirrors cancelled_at / cancel_reason so the requester's task detail can say
-- why. removal_reason is a slug (the admin panel's dropdown); removal_note is
-- the admin's free text and stays internal — it is never sent to a client.
ALTER TABLE public.tasks ADD COLUMN IF NOT EXISTS removed_at timestamptz;
ALTER TABLE public.tasks ADD COLUMN IF NOT EXISTS removal_reason text;
ALTER TABLE public.tasks ADD COLUMN IF NOT EXISTS removal_note text;

ALTER TABLE public.tasks DROP CONSTRAINT IF EXISTS tasks_removal_reason_check;
ALTER TABLE public.tasks ADD CONSTRAINT tasks_removal_reason_check
  CHECK (removal_reason IS NULL OR removal_reason = ANY (ARRAY[
    'out_of_scope_private_residence'::text,
    'out_of_scope_other'::text,
    'inappropriate'::text,
    'other'::text
  ]));

-- The in-app + push notification both parties get. notifications.type is an
-- enum, so an unlisted value makes the INSERT fail silently inside notify.Create
-- (that is exactly why zero COMPLETED_SUPPORTER rows exist in prod) — the value
-- has to be added here before any code emits it.
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'TASK_REMOVED';
