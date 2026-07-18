# D-07: Mobile's public profile shows a reviews section; web has no equivalent — backlog item to port it

**Date:** 2026-07-17
**Status:** Accepted
**Trigger:** Building `mobile/src/app/profile/[id].tsx`'s Reviews section had no web page to audit for parity (`app/src/pages/PublicProfilePage.jsx` only shows completed/in-progress stat cards, never reviews), so the section was designed straight from the `GET /profiles/:id/reviews` backend contract instead of mirroring an existing UI.

## Decision
Ship the mobile Reviews section now (average stars + count, per-review star
row, `value_rating` label, "would rehire" indicator, comment, relative
time; collapses entirely when a profile has zero supporter activity —
`asg_completed === 0` and no reviews — otherwise shows an EmptyState "No
reviews yet"). Web's `PublicProfilePage.jsx` is left as-is; porting the same
section there is deferred, tracked here so it isn't lost now that mobile has
set the pattern.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none

## Context and alternatives
Building the web version in the same task was out of scope (this task was
mobile-only) and `PublicProfilePage.jsx` would need its own layout pass
rather than a mechanical port. Shipping mobile-only first is still a real
improvement and the backend contract (`listProfileReviews` in
`server/main.go`) already supports both clients identically, so the web
port later is additive, not a redesign.

## Evidence
- `app/src/pages/PublicProfilePage.jsx` — confirmed no reviews UI exists on
  web today (only two stat cards).
- `server/main.go` `listProfileReviews` (`GET /profiles/:id/reviews`) —
  returns `{ reviews, count, avg_stars }`; `avg_stars` is `null` when
  `count === 0`. The anonymous `ReviewItem` struct has no `reviewer_id`/
  `supporter_id` (those only exist on `createReview`'s `Review` struct) and
  adds `task_title` instead. `value_rating`/`comment` are plain Go
  `string` with no `omitempty` — always present, `""` when unset, never
  JSON `null`; only `would_rehire` (`*bool`) is genuinely nullable. Mobile's
  `Review` TS type (`mobile/src/lib/types.ts`) previously modeled
  `value_rating`/`comment` as nullable and `reviewer_id`/`supporter_id` as
  always-present — both wrong — and was corrected in the same change as this
  screen.
