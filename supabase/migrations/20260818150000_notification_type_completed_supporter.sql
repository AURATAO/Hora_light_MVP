-- Repair: notifications.type has never accepted 'COMPLETED_SUPPORTER'.
--
-- server/main.go's completeTask has emitted that type since the completion flow
-- shipped, and notify.go renders a dedicated email for it, but the enum was
-- never extended. notify.Create returns the failed INSERT to a caller that only
-- logs it, so every "your task is complete" notification to a supporter has been
-- dropped silently — prod has 0 rows of this type against 16 COMPLETED ones.
--
-- Additive only. The dropped notifications are gone; this stops the bleeding.

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'COMPLETED_SUPPORTER';
