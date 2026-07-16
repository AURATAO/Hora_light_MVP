# D-06: Forgot-to-clock-out protection ships client-only in v1; server watchdog deferred to v1.1

**Date:** 2026-07-14
**Status:** Accepted
**Trigger:** Mobile supporter-flow task asked for "forgot-to-clock-out protection, client-side only, no backend changes" — the request itself scoped out the more complete server-side version, which this record captures so it isn't lost.

## Decision
v1 (shipped, `mobile/`): purely client-side. The Work session card shows an
overtime visual state (danger-colored elapsed timer + "Over the estimated
time" caption) once elapsed time exceeds `task.estimated_minutes`, and
`mobile/src/lib/overtime-reminders.ts` schedules local device notifications
(`expo-notifications`) on clock-in — first nudge at
`computeFirstReminderDelayMinutes` (see `task-utils.ts` `OVERTIME_REMINDER`),
then 3 more every 30 minutes — cancelled on clock-out or completion. This is
best-effort and local to the device that clocked in: it does nothing if the
app is force-quit and the OS reclaims scheduled notifications, and the
requester sees nothing.

v1.1 (deferred, not built): a **server-side watchdog** — a Go cron/scheduled
job that scans for worklogs with `end_at IS NULL` past
`estimated_minutes` (+ grace), and on trigger: (a) notifies both the assignee
and the requester via the existing notification system (S-32 pattern, not a
new channel), and (b) gives an ops/admin an **adjust-time** flow to correct
a worklog's `end_at` after the fact for the cases where a supporter
genuinely forgot and the clock ran for hours. This is the version that
actually protects the requester (who is billed by elapsed time) and doesn't
depend on the assignee's device still having the app installed/notifications
enabled.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. When v1.1 is built: new endpoint(s) need
  auth middleware in the same diff (S-13), and the admin adjust-time flow is
  Tier 3 (S-40, touches billing-relevant worklog data) — flagging now so
  that isn't missed later.

## Context and alternatives
Building the server watchdog now was out of scope for this task (explicit
"no backend changes" constraint) and is a materially bigger piece of work:
a scheduled job, a notification fan-out to both parties, and an admin UI
affordance to correct time — none of which exist yet. Shipping the client
nudge first is still a real improvement (catches the common case: supporter
is on their phone and forgot) at near-zero cost, while the record here
prevents the harder, more valuable version from being forgotten once
"forgot to clock out" is perceived as already solved.

## Evidence
- `mobile/src/lib/overtime-reminders.ts`, `mobile/src/lib/task-utils.ts`
  (`OVERTIME_REMINDER`, `computeFirstReminderDelayMinutes`) — v1 client
  implementation.
- `server/main.go` `clockOut`/`completeTask` — confirms no existing
  server-side elapsed-time enforcement or admin time-adjustment path today.
