# D-12: Live supporter tracking ships on the existing `task_gps_pings` pipeline, opt-in from an "On my way" tap, with the state derived server-side

**Date:** 2026-09-17
**Status:** Accepted — supersedes D-09
**Trigger:** The #1 requester ask out of Traction 3: "where is my supporter". D-09 shipped the interim last-known-location row and explicitly deferred "the full live map" to v1.1, naming three things that would have to be decided when it came back — a native map dependency, a tile provider, and the fact that it would need a native rebuild. This is that decision.

## Decision

Four things are now true that weren't:

1. **Sharing starts when the supporter says so, not at clock-in.** A new
   `POST /tasks/:id/enroute` sets `tasks.enroute_at` and opens a second,
   narrower ping window: assigned task, still open, and **no worklog yet**.
   `saveGpsPing` accepts `source='enroute'` inside that window and nowhere
   else. Until the tap, nothing is shared — that is the privacy default, and it
   is the default in the literal sense: no backfill, so every task that existed
   before this change stays exactly as private as it was.

2. **The window is self-closing.** It is keyed on *zero worklogs existing*, not
   on `enroute_at` alone. `enroute_at` is never cleared, so a window keyed on
   that column would re-open after clock-out and keep sharing a supporter's
   position for the rest of the task's life. Counting worklogs makes clock-in
   end the enroute phase and clock-out end sharing altogether, which is what
   the supporter was told would happen.

3. **The state is derived on the server.** `GET /tasks/:id/live` returns
   `{state, lat, lng, updated_at, distance_m, supporter, destination}`, with
   `state` computed from the haversine distance to `job_lat/job_lng`:
   `<100m at_door`, `100–500m almost_there`, `>500m on_the_way`, `working`
   once a worklog is open, `unavailable` when the newest ping is older than two
   minutes. The client never re-derives it (S-05) — and the 100m threshold is
   *the same constant* that fires the arrival push, so the label and the
   notification cannot disagree.

4. **`react-native-maps`, Apple Maps, no tile-provider decision.** The three
   things D-09 flagged resolve as: the dependency is `react-native-maps@1.27.2`
   (Expo SDK 57 compatible); the provider is `PROVIDER_DEFAULT`, which is
   MapKit on iOS and needs **no API key and no GoogleMaps pod** — confirmed by
   `Podfile.lock`, which gained `react-native-maps` and nothing else; and the
   native rebuild rides build 9, which is already native for Apple Pay.

Two supporting decisions worth recording because they were close calls:

- **Requester-only, answered 404.** `/live` is not requester-*or*-assignee like
  `/gps-latest`. A supporter has no business reading a distance-to-door derived
  from their own movements, and a supporter probing the endpoint should not be
  able to tell "you may not read this" from "there is nothing here".

- **No polyline, no ETA, no marker interpolation.** A polyline is a road we did
  not measure, an ETA is a promise we cannot keep, and interpolation invents
  positions the phone never reported. The markers jump on each poll, which is
  exactly as often as the truth changes. This overrides D-09's sketch of "a
  moving pin and a route trail" — the trail data does exist, and drawing it
  would still be a claim about a route nobody checked.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. Both new endpoints go through the Go backend
  (CLAUDE.md Rule 1 / S-01); the migrations add two nullable columns, widen one
  CHECK, and add one enum value, with no policy and no client grant — the four
  queries in CLAUDE.md Rule 3 still return three rows, none, none, none.
- D-09 is superseded: its v1.1 is this, with the route trail deliberately
  dropped.

## Context and alternatives

**Why not a WebSocket / Supabase Realtime.** A 15s poll on an endpoint that
reads one indexed row is cheap, works identically on web and in a backgrounded
React Native app, and needs no new transport to reason about. Realtime would
also mean a client-direct subscription to a Hora table, which Rule 1 forbids.

**Why a rate limiter at all**, on an auth-gated endpoint scoped to one task the
caller owns: not for an attacker, for the ordinary accident — an interval that
never got cleared, a retry loop with no backoff, the same task open on a phone
and two tabs. In-memory and per-instance on purpose; N instances allow N×,
which is still a bound, and the thing being bounded is a runaway client.

**Why the arrival latch is a column and not a `count(*)` on notifications.**
The notification can fail to insert — that is exactly the COMPLETED_SUPPORTER
bug (20260818150000) — and the latch has to hold anyway. `UPDATE … WHERE
arrival_notified_at IS NULL` is both the latch and the concurrency lock: eight
pings crossing 100m at once all run it, one matches, one push goes out.

**Why `enroute` is a `source` value and not a boolean on the request.** The
without-a-worklog exception is keyed on it, so it has to be something a client
cannot reach by sending `background` from a clocked-out phone. The cost is that
the foreground/background split is not recorded during the enroute window;
accepted, because the gap-measuring queries that column was added for
(20260827120000) are about the clocked-in span, which is unchanged.

## Evidence
- `supabase/migrations/20260917120000_task_enroute_live_tracking.sql` (columns
  + widened CHECK) and `…130000_notification_type_supporter_arrived.sql` (the
  enum value, split into its own file so it is committed before anything writes
  it — the COMPLETED_SUPPORTER lesson).
- `server/live_tracking.go` — `liveState`, `enrouteWindowOpen`,
  `maybeNotifyArrival`, `getLiveLocation`, `liveRateAllow`.
- `server/live_tracking_test.go` — the state table including both boundaries
  (exactly 100m is `almost_there`, exactly 500m is `almost_there`), the
  nine-case window table, the requester-only leak test, and the
  eight-goroutine arrival-concurrency test. Full suite green against
  postgres:16: `go test ./... -count=1` → ok.
- `app/src/lib/liveTracking.test.mjs` — 9 tests pinning the copy, including the
  guard that no public string in the module can render a coordinate.
- `mobile/ios/Podfile.lock` — `react-native-maps (1.27.2)` present, no
  `GoogleMaps`/`Google-Maps-iOS-Utils` pod; `pod install` clean under the
  existing `useFrameworks: static`.
- DESIGN.md §8 checks 1–3: OK. `tsc --noEmit`: clean. `vite build`: clean.
