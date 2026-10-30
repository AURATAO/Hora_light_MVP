# D-20: Background work is fire-and-forget, and the server has no graceful shutdown

**Date:** 2026-10-30
**Status:** Proposed. A residual recorded for later; nothing is decided yet.
**Trigger:** The Mac run on 2026-10-30 with `-race` failed `TestPhase2bApprovedTimeExtensionRaisesTheCapAndReArmsTheWarnings`.
- `announceExtensionResolution` ran as a bare `go` and was still sending notices after the test ended.
- It read the global `db` while `setupAdminOpsDB`'s cleanup wrote it.
- The bug is latent and has been there since 5990fa4 (2026-09-14). It appears only when the timing lines up.

## Decision (so far)
Fixed for the tests only (`ac1c41e`):
- Extension notices go through `announceInBackground`, and `announceWG` counts them.
- The fixture cleanup waits on `announceWG` and on `geocodeWG` before it swaps the db back.
- Production behaviour is unchanged.

## The open problem
- `server/` has 18 bare `go` statements outside tests. Examples: push and email notices, the chat relay, `notifyChatOnMyWay`, `lockChatsBetween`, ops emails, the Stripe webhook follow-ups, payouts, and geocoding.
- `main.go` calls `srv.ListenAndServe()` and has no `Shutdown`. On a Render deploy, SIGTERM ends the process with that work still in flight.

What is lost on a deploy, and what is not:
- **Money is not lost.** Settlement has a backstop: `watchUnsettledCompletions` retries every 5 minutes. Geocoding has one too: `geocodeMissing` asks again.
- **Notices are lost.** A push, email or iMessage relay in flight at the moment of a deploy disappears silently, with no log line. Neither side is told.
- **Tests stay exposed.** Any other bare goroutine that reads `db` can hit the same `-race` flake in a test that swaps the pool. Only the extension path is covered today.

## Options for later (the Arbiter's call)
1. **Graceful shutdown, and a single registry for background work.** Use `signal.NotifyContext` and `srv.Shutdown(ctx)`, then wait up to about 20 s on one `bgWG`. Every bare `go` becomes `background(fn)`. This is cheap. It covers deploys and the tests, but not crashes.
2. **Make important notices durable.** Write an outbox row in the same transaction, and have a sender deliver it. This survives crashes too, but costs more: a new table, a sweeper, and deduplication. Worth it only for notices that matter, such as the extension decision and the relay to the requester.
3. **Leave it.** Today a deploy loses a notice only if the notice was in flight at that exact moment.

Recommendation: option 1 first, since it is about 50 lines and one review. Consider option 2 only for the requester relay, if a lost message is ever seen.

## Evidence
- The race trace is from the Mac run of 2026-10-30, at `admin_task_ops_test.go:229` (cleanup write) against `announceExtensionResolution`. The same run also logged `sql: database is closed` from the late notice.
- `grep -n '^\s*go [a-zA-Z]' server/*.go | grep -v _test` finds 18 sites.
- `main.go` ends in `srv.ListenAndServe()`, and no `Shutdown` exists anywhere in `server/`.
