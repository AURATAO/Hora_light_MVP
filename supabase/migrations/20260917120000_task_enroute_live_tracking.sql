-- Live supporter tracking: the "On my way" window, and the arrival latch.
--
-- Today a supporter's position is only visible to the requester while a
-- worklog is open — tracking starts at clock-in, which is the moment the
-- supporter ARRIVES. The whole span the requester actually wants to watch
-- (accepted → on the doorstep) has never been shared at all.
--
-- This opens exactly that window, and opens it only when the supporter
-- deliberately says so. Three columns, all on tasks, all nullable:
--
--   enroute_at          when the supporter tapped "On my way". NULL means they
--                       never did, and nothing is shared before clock-in —
--                       that is the privacy default, and it is the default in
--                       the literal sense: no backfill, so every task that
--                       exists today stays exactly as private as it is now.
--
--   arrival_notified_at the latch behind "«name» has arrived". Set once, by a
--                       guarded UPDATE (... WHERE arrival_notified_at IS NULL)
--                       so two pings crossing the 100m threshold in the same
--                       second cannot both send it. A column rather than a
--                       count of notifications rows because the notification
--                       may fail to insert and the latch must still hold.
--
-- The window CLOSES at clock-in (a worklog exists from then on) and the sharing
-- ends at clock-out/completion. Nothing here expresses that — it is enforced in
-- saveGpsPing, which is the only writer — but it is why enroute_at is never
-- cleared: it is a record that the tap happened, not a live flag.
--
-- Reversible:
--   ALTER TABLE public.tasks DROP COLUMN enroute_at, DROP COLUMN arrival_notified_at;
--   (and restore the two-value source CHECK below).
--
-- Security: existing tables, no new table, no policy, no grant. RLS stays on
-- with the three deny-all policies (CLAUDE.md Rule 3) — the Go backend is
-- still the only reader and writer.

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS enroute_at timestamptz;

ALTER TABLE public.tasks
  ADD COLUMN IF NOT EXISTS arrival_notified_at timestamptz;

COMMENT ON COLUMN public.tasks.enroute_at IS
  'When the assigned supporter tapped "On my way" (POST /tasks/:id/enroute). NULL = never tapped, and nothing is shared with the requester before clock-in. Set once; never cleared.';

COMMENT ON COLUMN public.tasks.arrival_notified_at IS
  'Latch for the once-per-task "has arrived" notification, set by saveGpsPing the first time a pre-clock-in ping lands within 100m of the task location. Guarded UPDATE, so it is also the concurrency lock.';

-- The third capture path. 'foreground' and 'background' say WHICH client
-- mechanism produced the fix; 'enroute' says WHICH PHASE OF THE TASK it
-- belongs to, and that difference is deliberate: it is the value the server
-- keys the without-a-worklog exception on, so it has to be a value the client
-- cannot get away with sending during any other phase.
--
-- The cost is that the foreground/background split is not recorded during the
-- enroute window — a fix captured by the background TaskManager task before
-- clock-in stores as 'enroute', not 'background'. Accepted: the gap-measuring
-- queries that column was added for (20260827120000) are about the clocked-in
-- span, which is unchanged.
ALTER TABLE public.task_gps_pings DROP CONSTRAINT IF EXISTS task_gps_pings_source_check;
ALTER TABLE public.task_gps_pings ADD CONSTRAINT task_gps_pings_source_check
  CHECK (source = ANY (ARRAY['foreground'::text, 'background'::text, 'enroute'::text]));

COMMENT ON COLUMN public.task_gps_pings.source IS
  'Which client capture path wrote this ping: foreground (app open), background (iOS location task), or enroute (the pre-clock-in "On my way" window, the one phase where a ping is accepted with no open worklog). Set by POST /tasks/:id/gps-ping; defaults to foreground for clients that omit it.';
