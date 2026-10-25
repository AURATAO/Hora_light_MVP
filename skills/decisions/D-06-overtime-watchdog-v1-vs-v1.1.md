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

## Addendum 2026-10-24 — the live sweep (Aura, staging)

"Pick up laundry from dry cleaning": estimate 30 + 15 approved + 15
auto-extend = 60-minute cap; the supporter (web app) clocked 80 minutes.
Billing WAS capped (45 billable minutes, $34.50) — the design held. But the
warning fired at minute 59 and "cap reached" at minute 73 (18:34:59),
because the only evaluation points are the mobile app's GPS ping, screen
opens, clock-out and the 6-hour backstop: a web-app supporter sends no
ping, so the layers fired whenever someone happened to open a screen.
Aura: "整個超過 time cap 但都沒有被制止".

Built (channel branch, server-side only): `watchLiveTimeCaps` — every
minute, `sweepLiveTimeCaps` evaluates every open task with a session
RUNNING (`worklogs.end_at is null`); nothing else changes — no automatic
stop (the D-06 rule stands), the latches still fire once, idle tasks stay
with the 6h backstop. Cost: one indexed query a minute plus one evaluation
per running session. Still open (product): "no automatic stop" means a
supporter can keep the clock running unpaid past the cap, and the receipt
shows the clocked time (1h 20min) next to the capped bill — whether the
cap should also end the session is the Arbiter's call, not made here.

**Arbiter 2026-10-24 23:34 — option 2: the cap clocks the session out.**
Reaching the ceiling ends the running session at the cap minute (`end_at =
start_at + what the cap had left`, never in the future); the task stays
open; the supporter is told to ask for more time and clock in again once
it is approved; the requester is told the clock was stopped. "No automatic
cancellation" still holds — only the session ends. A phone that keeps
pinging after that gets the existing 403 "not clocked in". Built with the
live sweep, so the clock stops within a minute of the cap on either app.

**Review r1 (FAIL) → fixes.** The cut runs on every evaluation when the
cap is reached (idempotent; a session clocked in again without an
approval, or one left running when a failure spent the latch, is cut just
the same) — the latch gates only the notifications; the notification
words follow what happened (a session cut, or none running); the ops
"unresponsive requester" alert fires only when time is actually past the
cap (a session the cut could not close) — with the clock stopped nobody
works unpaid; a manual clock-out racing the cut keeps the cap-minute
end_at; the live sweep logs iteration errors. Accepted: a forced split
can bill up to one extra rounded minute (bounded by the cap).

**Review r2 (FAIL) → fixes.** The cut runs first, before any notification,
and only writes when a session is open; a cut that fails claims no latch
(retried by the next evaluation, words stay true); a session started with
the cap used up is closed at zero minutes (never bills, never eats the
next approval) and the clock-in endpoint refuses it outright (403
`time_cap_reached`, "ask for more time and clock in again once it's
approved"); Layer 3 keeps both shapes with honest words — "still clocked
in past the cap" when the cut could not close a session, "stalled at its
time cap, no answer" when the clock is stopped and nobody answers; a
manual clock-out that lost the race to the cut sends no second
notification; both sweeps log iteration errors.

**Review r3: PASS** (residuals fixed: the live sweep no longer skips tasks
once ops were alerted — the cut is idempotent; the ops figures are read
after the cut; a cut logs a line). Client follow-up (mobile, after the
freeze; web app now): treat 403 `time_cap_reached` on clock-in as "ask for
more time", and 400 "no active session" on clock-out as "already clocked
out". Accepted: the "stalled at its time cap" ops email fires for a silent
requester with nothing at stake (as before; one per approval cycle).

## Addendum 2026-10-25 — completion answers the browser before the money moves

**Incident.** Both web-app completions on staging (10/24 20:33, 10/25 21:39)
took ~15 s inside `POST /tasks/:id/complete` — capture, balance charge, two
payouts, five emails, one iMessage, all synchronous — and Render's edge
answered the browser **502** (same 223 KB error page both times) while Gin
logged 200. The supporter saw "Complete failed" on a task that had completed
two seconds in; the requester already had the confirmation email.

**Change (0ce5781 + review fixes).** The status write is the commit and the
handler answers right after it with `getTask`. Settlement + receipts +
completion emails run detached (`completionDetach`, a goroutine in
production, inline in tests) on `context.Background()` with **no deadline**
— a deadline of our own would expire mid-capture and then expire the
`capture_failed` write that records it — and a recover that **emails ops**
(a settlement that vanished quietly is the one failure the payments code
promises never happens). The web app, on a non-4xx failure, re-reads the
task and treats `completed` as success.

**Review r1 (FAIL, 4 MAJOR) → fixes.**
1. A chain lost to a restart left no record → `sweepUnsettledCompletions`
   (`payments_resettle.go`, every 5 min): a `completed` task older than 10
   minutes whose task payment is still live is settled again from its own
   row; a hold Stripe already captured fails the re-capture into
   `capture_failed` + the ops email (honest, ops reconcile by the runbook).
   Completion emails are not re-sent (courtesy; the receipt is the record).
2. The 3-minute deadline and the silent recover → removed / ops-emailed.
3. (Reviewer could not see `setupStripeWebhookDB`; it delegates to
   `setupAdminOpsDB`, so every completion test runs the chain inline.)
4. `settlement.state` read `estimated` for a completed task with a live hold
   → new `settling`; both clients fall through to their neutral rendering on
   an unknown state (checked: exact-match switches only).
5. (MINOR) Two concurrent completes both settled → the UPDATE is
   `… AND status='open'`, zero rows → 400 "already closed"; one chain.
6. (MINOR, accepted) The web fallback's re-read: only the assignee can
   complete, so "someone else completed it" is the same person retrying;
   `completed` is not `settled` — the page says done, the money follows.
7. (MINOR) The panic log prints `%T`, not `%v` (a formatted completion note
   could ride along in a panic value — S-12).

Tests: `TestResettleSweepSettlesAnOrphanedCompletion`,
`TestCompleteTwiceIsOneCompletion`, `TestSettlementStateSettlingWhileHoldIsLive`.
Runbook follow-up: add "`[payments][RESETTLE]` — a completion's chain died;
the sweep settled it" to the log-only entries.

**Review r2 (FAIL, 4 MAJOR) → fixes.**
1. The sweep covered only the pre-capture window; a chain dying after the
   capture (payout / balance / receipt missing) left no trace → a second
   shape, *captured but `settled_total_cents` NULL*, is recorded once
   (`SETTLEMENT_INCOMPLETE` audit row) and escalated to ops by email; it is
   not re-run, because settlement cannot resume from the middle and
   guessing with money is worse than a person reconciling (runbook entry 7).
2. No mutual exclusion: Stripe's idempotent capture would *replay* into an
   overlapping second caller, which would then charge the balance twice →
   `settleCompletedTask` takes a per-task advisory try-lock
   (`advisoryLockTaskSettle`, in a transaction that holds only the lock);
   a second caller gets `Busy` and does nothing. Covers the chain, cancel,
   force-complete and the sweep alike.
3. No lookback bound: a first tick would have captured any historical
   backlog → 7-day window; older orphans are counted in a log line only.
   (Production checked: 14 completed tasks, all captured and settled — the
   first tick finds nothing.)
4. The double-complete test could not fail → test seams
   `completeTaskBeforeWrite` / `cancelTaskBeforeWrite` let a racing
   complete land between the status check and the write; the loser gets
   400 and starts no chain. Same guard added to **cancelTask**'s UPDATE
   (`and status='open'`): a cancel after a complete no longer overwrites it.
5. (MINOR) Sweep actor → system actor; per-task recover with ops email;
   pass-level 4-minute query deadline; the sweep's comments no longer claim
   an idempotency the balance charge does not have; the panic email says
   what the sweep will and will not do.
6. (MINOR, accepted) `settling` is outside mobile's TypeScript union; at
   runtime both clients fall through to the neutral "Settlement" rendering
   (exact-match switches). Add to the union with the next mobile build.
7. (MINOR) Web: a 400 "already closed" on complete is also settled by
   re-reading the task (a retry whose first attempt landed).
8. (MINOR, accepted) An admin time adjustment inside the settling window
   could change the figure the sweep later captures; the adjustment path
   refuses once captured, so the two never both land.

**Review r3 (FAIL, 2 MAJOR) → fixes.**
1. The lock held a *pool* connection for the whole ~15 s chain; with
   `DB_MAX_CONNS=3` three overlapping settlements would deadlock the pool
   and every handler behind it → the lock moved to ONE dedicated connection
   outside the pool (`payments_settle_lock.go`: session-level
   `pg_try_advisory_lock` / `pg_advisory_unlock`, a Go mutex around the
   round-trips only, lazy reconnect; process death drops the locks, which
   is when the sweep takes over). Budget: one connection beyond the pool.
2. `Busy` also stood for "could not ask for the lock", and cancelTask turned
   that into a silent zero → the two are split: lock *unavailable* is an
   error and goes through `reportCaptureFailure` (audit row + ops email;
   the hold stays live, so a completed task is retried by the sweep); lock
   *held* is `Busy`, and the two callers for whom the sweep cannot follow
   up — cancelTask (cancelled tasks are never swept) and force-complete —
   log an `[ERROR]` and email ops instead of reporting a success.
3. (MINOR) The pass-level 4-minute deadline could cut a settlement
   mid-capture → removed, same rule as the chain.
4. (MINOR) Shape two could flag a chain that captured and is still running
   → the sweep asks the lock first (`isTaskSettlementRunning`); held = slow,
   not dead, skipped.
5. (MINOR) The once-only marker relied on `writeAudit`, which swallows
   errors → `writeAuditErr`; no email unless the row landed.
Tests added: `TestResettleSweepDoesNotFlagARunningSettlement`; the overlap
test now exercises the session lock against a transaction-level holder.
Proposed standards (from the reviewer, for the Arbiter): "a lock or
pool-holding construct in payments code states its connection budget
against DB_MAX_CONNS"; "a skipped settlement is distinguishable from an
errored one, and an errored one hits the ops path".

**Self-found while briefing r4 (fixed before the verdict).** Session-level
advisory locks are re-entrant within one session: a second
`pg_try_advisory_lock` for the same key on the shared connection returns
true, so two goroutines of one process would both have been let through.
The lock now keeps an in-process `held` set (the in-process half); the
database lock is the cross-process half. `TestSettleCompletedTaskRefusesToOverlapInProcess`
fails without it.

**Review r4 (FAIL: 1 BLOCKER, 3 MAJOR) → fixes.** The BLOCKER (in-process
re-entrancy) and two of the MAJORs (the running-probe, and tests that only
proved cross-session exclusion) were the self-found hole above, already
fixed in 4d02a5f before the verdict; the remaining MAJOR and minors:
1. A connection break dropped every in-flight settlement's lock silently,
   and a late unlock could release a newer settlement's lock on the remade
   connection → leases carry a connection generation; a stale lease
   releases nothing on the live connection and only its own in-process
   mark; the drop itself is logged `[ERROR]` and emailed to ops with the
   tasks mid-flight (ids only). `TestSettleLockStaleLeaseDoesNotReleaseANewerLock`.
2. (MINOR) An idle connection the server dropped would have reported a
   capture failure on its first use → one reconnect-and-retry before the
   error path.
3. (MINOR) pgx connect errors (host/user/db) reached the audit row and the
   ops email through `out.Err` → logged only; the error carried is fixed text.
4. (MINOR) cancelTask also lands in `reportCaptureFailure` → its email copy
   now says "closed (completed, or cancelled with work owed)". Accepted: a
   cancelled task whose lock could not be asked for is not retried by the
   sweep (cancelled tasks are never swept); the audit row and email are the
   record.
5. (MINOR) Fixture: `resetSettleLockForTests` on the DB swap's cleanup.
6. (MINOR) Budget comment: one connection beyond the pool AND `sqldb`.
