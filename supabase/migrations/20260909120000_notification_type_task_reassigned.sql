-- Admin reassignment of a task's supporter (POST /admin/tasks/:id/reassign).
--
-- Three people are told: the incoming supporter, the outgoing one, and the
-- requester. All three get this one type — the per-recipient wording is
-- composed in Go (server/admin_reassign.go) and the email template renders it,
-- so one enum value covers the event rather than three near-identical ones.
--
-- ORDER_ACCEPTED was the obvious candidate to reuse for the incoming
-- supporter and does not fit: its email copy is hardcoded requester-facing
-- ("<name> has accepted your task").
--
-- notifications.type is an enum, so an unlisted value makes notify.Create's
-- INSERT fail and the notification is dropped silently — the value has to land
-- here before the code that emits it deploys (the same mistake that lost every
-- COMPLETED_SUPPORTER row; see 20260818150000).

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'TASK_REASSIGNED';
