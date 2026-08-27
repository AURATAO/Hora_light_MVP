-- Record which capture path produced a GPS ping.
--
-- Why: pings were foreground-only, so a locked phone left a 15-60 min hole in
-- the trail between clock-in and clock-out. The mobile app now also pings from
-- an iOS background location task. Without this column the two are
-- indistinguishable, and there is no way to measure whether background
-- tracking actually closed the gap.
--
--   'foreground' -- setInterval + getCurrentPositionAsync while the app is open
--   'background' -- expo-location TaskManager task ('hora-gps-tracking')
--
-- Shape: additive, NOT NULL with a default, so it is safe to apply BEFORE the
-- Go build that writes it (S-21 expand step). Existing rows and the current
-- TestFlight build — which sends no `source` field at all — both land on
-- 'foreground', which is what they in fact are. Reversible:
-- `ALTER TABLE public.task_gps_pings DROP COLUMN source;`.
--
-- Security: existing table; RLS is already enabled with zero policies
-- (S-10 / D-02). No new surface, no policy added.

ALTER TABLE public.task_gps_pings
  ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'foreground';

-- Mirrors tasks_removal_reason_check / timesheets_type_check: the set is
-- closed, so keep it closed in the database as well as in the handler.
ALTER TABLE public.task_gps_pings DROP CONSTRAINT IF EXISTS task_gps_pings_source_check;
ALTER TABLE public.task_gps_pings ADD CONSTRAINT task_gps_pings_source_check
  CHECK (source = ANY (ARRAY['foreground'::text, 'background'::text]));

COMMENT ON COLUMN public.task_gps_pings.source IS
  'Which client capture path wrote this ping: foreground (app open) or background (iOS location task). Set by POST /tasks/:id/gps-ping; defaults to foreground for clients that omit it.';
