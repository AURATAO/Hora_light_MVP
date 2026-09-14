-- Stripe Phase 2b: the four notification types settlement introduces.
--
-- notifications.type is a Postgres ENUM, so an unlisted value makes
-- notify.Create's INSERT fail and the notification, its email and its push
-- vanish leaving only a log line — the exact failure that lost every
-- COMPLETED_SUPPORTER row for two months (see 20260818150000). This migration
-- therefore has to land BEFORE the Go build that emits these values (S-21).
--
--   BUDGET_INCREASE_REQUESTED  supporter asks the requester for more money
--   EXTENSION_RESOLVED         either side is told approved / denied / expired
--   TIME_CAP_WARNING           ~5 min of the requested time left (both parties)
--   TIME_CAP_REACHED           the consented ceiling is hit; billing has stopped
--
-- Why EXTENSION_RESOLVED rather than one type per outcome: the outcome is in
-- the title and body, composed in Go (server/extensions.go), and the email
-- template renders whatever it is handed. Three enum values for approved /
-- denied / expired would be three near-identical templates and three places to
-- forget. The same reasoning TASK_REASSIGNED was added under.
--
-- Why no separate TIME_EXTENSION_REQUESTED: a time extension is requested by
-- the supporter TAPPING the TIME_CAP_WARNING/REACHED notification they already
-- received, and the requester is told about it with BUDGET_INCREASE_REQUESTED's
-- sibling — no. It has its own: a requester who gets "your supporter needs
-- 15 more minutes" is being asked a question, and a warning is not a question.
-- TIME_EXTENSION_REQUESTED is that value.
--
-- Reversible: enum values cannot be dropped in Postgres. Rolling this back
-- means leaving the values in place unused, which is harmless — nothing reads
-- the enum but the INSERT.

ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'BUDGET_INCREASE_REQUESTED';
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'TIME_EXTENSION_REQUESTED';
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'EXTENSION_RESOLVED';
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'TIME_CAP_WARNING';
ALTER TYPE public.notification_type ADD VALUE IF NOT EXISTS 'TIME_CAP_REACHED';
