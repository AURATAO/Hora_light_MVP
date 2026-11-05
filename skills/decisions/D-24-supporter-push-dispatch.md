# D-24: Supporter push dispatch (borough round, then everyone, then ops)

**Date:** 2026-11-06
**Status:** Proposed. Ruled by Aura on 2026-11-06; build on `feat/supporter-dispatch`.
**Tier:** T3. It adds a migration, touches profiles and tasks, and sends notifications.

## Context
- Today a new task emails ops only. Aura dispatches by hand, so "Now" depends on her reacting.
- The pilot is planned for early or mid December. About 50 supporters are waiting to be onboarded.
- PLAN §13b proposed broadcast dispatch, and §13b-2 an escalating flat price.
- On 2026-11-06 Aura chose to build the broadcast first, scoped by borough.

## Decision (Aura, 2026-11-06)
1. **Round 1.** When a task goes live, it is pushed to every eligible supporter who is available and serves the task's borough.
2. **Round 2.** After 3 minutes with no accept, it is pushed to every other eligible, available supporter.
3. **Ops.** After 5 minutes with no accept, ops are emailed. This is PLAN §13b's N. It applies to immediate tasks only: a scheduled task has already reached ops as "New task posted", and nothing about it is urgent at minute 5.
4. **Areas.** Supporters pick their boroughs, from the five NYC boroughs, multi-select. A task's borough is its first stop's, taken from the service-area check. A task with no borough skips round 1.
5. **Availability.** "I'm available" lasts 4 hours, then turns itself off. There is no separate night rule.
6. **Claiming.** First accept wins, using the existing claim. Supporters' live location is not used for dispatch.
7. **Feature flag.** `DISPATCH_PUSH_ENABLED=1` turns all of it on. Unset, behaviour is exactly as today. `GET /config` tells the app whether to show the toggle.

## Eligible supporter
A supporter receives the push only if all of these hold:
- They are a verified supporter.
- `available_until > now()`.
- They have at least one push token.
- They are not the requester.
- Neither has blocked the other.
- They are in the requester's sandbox partition: the review sandbox only ever reaches sandbox users, and the reverse.
- They are payout-ready when payments are enforced. This is the same gate `acceptTask` applies, so nobody is pinged for a task they cannot take.
- **They have no overlapping task** (Aura, 2026-11-06). An open task already assigned to them must not intersect the new task's window.
  - **The new task's window:** from now (ASAP) or its start, for its estimate (minimum 30 min), plus 30 min to get there.
  - **An ASAP job already assigned:** holds its supporter from now for its estimate plus 30 min.
  - **A scheduled job:** holds them from its start for the same length.
  - **An ASAP job accepted more than a day ago:** holds nobody, so a task left open by mistake cannot bench a supporter forever.

## Push content
The push is shown on the lock screen, so it carries no address, no requester name and no description.
- **Title:** "New task near you · ~$X", where X is the supporter's estimated earnings: base plus estimated time, less the platform fee.
- **Body:** the task's title, its borough, and "Now" or the scheduled time.
- **Data:** `{task_id, type: NEW_TASK_OFFER, url: /task/<id>}`.
- **Delivery:** `interruptionLevel: "time-sensitive"` and `priority: "high"` (Aura, 2026-11-06), so the push breaks through Focus. The app must ship the `com.apple.developer.usernotifications.time-sensitive` entitlement; without it, iOS shows an ordinary notification.

## Taken by someone else (Aura, 2026-11-06)
- The first accept wins (acceptTask's claim).
- **Required:** anyone else who opens the task from the push sees "This task was just taken". This is app work, with no new server call: acceptTask already answers "not available".
- **Stretch, not blocking the submission: withdrawing the delivered notification from other lock screens.** Expo's message format has no collapse id. The likely route is a silent push (`_contentAvailable`) that has the app dismiss the notification, which needs the `remote-notification` background mode. iOS throttles silent pushes, so it is best-effort at most. Research it before building.

## Alignment with the supporter-only app prompt (Aura, 2026-11-06)
Aura's prompt for the supporter-only app plan asked for "nearest 2–3 within a radius". She ruled to **keep the borough round instead**: no live location, no new location permission. Everything else in that prompt is adopted:
- the 2–3 minute wave (3 minutes here)
- everyone in wave 2
- the atomic claim
- "already taken"
- ops alerted when nobody accepts
- no overlapping task
- Time Sensitive
- pricing as today

## Residuals (declared)
- **Notification fatigue.** There is no per-supporter rate cap yet. With 50 supporters and a pilot's volume this is fine; a cap is on the roadmap.
- **Round 2 recipients.** Round 2 is everyone eligible at widen time except the ids round 1 reached (`round1_user_ids`). Someone who turned availability on after round 1 gets round 2, which is intended.
- **Sweep timing.** The sweep runs every 30 s, so widening happens at 3:00–3:30 and ops are emailed at 5:00–5:30.
- **The borough costs one lookup.** It is looked up again at dispatch time from the first stop, which already resolves with a city, so it is one Google call. Nothing on the post path changed.
- **Expo receipts.** Expo push receipts (delivery confirmations) are not polled. That matches today's push.
