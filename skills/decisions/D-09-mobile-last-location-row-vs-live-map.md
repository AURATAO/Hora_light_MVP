# D-09: Mobile requester sees the supporter's last-known location as a single Progress row in v1; the live map is deferred to v1.1

**Date:** 2026-07-26
**Status:** Accepted
**Trigger:** Mobile task "Requester sees supporter's last known location (parity with web's existing display)" — web already shows a last-known-position chip on the task page; mobile only *wrote* GPS pings and had no read side. The task scoped an interim text row, explicitly leaving the full live map for later, which this record captures so the more valuable version isn't lost.

## Decision
v1 (shipped, `mobile/`): a **single "Live location" row** in the task-detail
Progress card, requester-only and shown only while the supporter is clocked in
(an open worklog exists). It polls the **already-existing** backend endpoint
`GET /tasks/:id/gps-latest` (`server/main.go` `getLatestGps`, dualAuth +
requester/assignee check) every 60s while the screen is focused, stopping on
blur or clock-out (`useFocusEffect` — no background timers). Three honest
states, no error state:
- no ping yet — also the case when the supporter denied location permission, so
  nothing arrives — → muted "Waiting for location…";
- a fresh ping → tappable `brand` link opening the coordinates in the maps app
  (`openCoordsInMaps`, `?q=lat,lng` handoff mirroring web);
- a ping older than 5 min (`LOCATION_STALE_MS`) → still shown and still tappable,
  but styled muted with "Last seen 12 min ago" so it never pretends to be live.

No backend, schema, or native changes: the endpoint predated this task, and the
work is JS-only (verified by `expo export` — no native rebuild). Parity note:
web gates its poll on `task.status === 'open'`; mobile narrows to "open worklog"
because with no open worklog no pings are being written, so there is nothing
live to poll for.

v1.1 (deferred, not built): the **full live map** — `react-native-maps` with a
moving pin and a route trail. The trail data already exists: `task_gps_pings`
retains the recent ping history (the ~25-ping trail), so the map is a
presentation-layer upgrade over the same data, not a new data pipeline. That
remains the headline feature; this row is the interim that ships the core value
(the requester can see where their supporter is) at near-zero cost and with zero
new dependencies.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. The read path goes through the Go backend via
  `apiFetch` (`getLatestLocation`), consistent with S-01 — no direct Supabase
  table access from the client. When v1.1 adds `react-native-maps`, that is a
  native dependency and will need a dev-client/native rebuild (not JS-only), and
  a map-tile provider decision — flagging now so it isn't missed later.

## Context and alternatives
Building the live map now was out of scope and materially larger: a native map
dependency (new build, tile provider, permissions/attribution), pin animation,
and trail rendering. The single row reuses an endpoint that already exists and
the existing maps URL-handoff pattern, so it shipped behind `tsc` + DESIGN §8 +
`expo export` with no native work. Shipping it first is a real improvement (the
requester gains location visibility they previously only had on web) while this
record prevents the harder, higher-value map from being treated as "already
done" once a location row is visible in the app.

## Evidence
- `mobile/src/app/task/[id].tsx` — `LiveLocationRow`, the `useFocusEffect`
  60s poll gated on `canSeeLiveLocation`, and the Progress-card placement.
- `mobile/src/lib/api.ts` `getLatestLocation`, `mobile/src/lib/maps.ts`
  `openCoordsInMaps`, `mobile/src/lib/task-utils.ts` `formatLastSeen` /
  `LOCATION_STALE_MS` — v1 client implementation.
- `server/main.go` `getLatestGps` (route registered at `tasksAPI.GET
  "/:id/gps-latest"`) — the pre-existing endpoint reused unchanged; confirms no
  backend work was required.
- `app/src/pages/TaskDetail.jsx` (`latestGps` state, `/tasks/:id/gps-latest`
  poll, `?q=lat,lng` maps link) — the web display this mirrors.
- Verify: `tsc --noEmit` clean, DESIGN §8 checks 1–3 OK, `expo export
  --platform ios` succeeded (JS bundle only, no native module added).
</content>
</invoke>
