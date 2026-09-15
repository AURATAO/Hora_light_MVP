-- The requester whose completion charge failed has to be told, in the app and
-- by email, that they owe money and are blocked from posting.
--
-- notifications.type is an ENUM, so this has to land BEFORE the Go build that
-- emits it or the notification is silently dropped (S-21) — the same failure
-- that lost every COMPLETED_SUPPORTER row for two months.

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'BALANCE_DUE';
