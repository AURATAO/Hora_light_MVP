# D-10: Mobile takes the accept-race loser back to the feed; web still only toasts — backlog item to port it

**Date:** 2026-08-16
**Status:** Accepted
**Trigger:** An accept-race polish pass on mobile asked whether web still showed a raw `HTTP 400` to the loser of a concurrent accept. It doesn't — commit d975a35 already fixed that on both web call sites — but comparing the two clients surfaced a different, smaller divergence worth recording before it is forgotten.

## Decision
Mobile's loser flow is now: an informational (brand-tint, not danger) notice
reading "Someone just grabbed this one — check out the other tasks.", a refetch
of the task, and an automatic return to the Work list ~1.5s later, where the
existing focus reload drops the taken task out of Available.

Web keeps its current behaviour — a toast reading "This task was just accepted
by someone else." plus a refresh — and is left as-is. Two gaps are deferred and
tracked here:

1. **Copy drift.** The two clients word the same event differently. Mobile's
   line points somewhere useful; web's only reports.
2. **Behaviour drift.** `app/src/pages/My.jsx` refreshes the Available list, so
   the user is already looking at somewhere to go next — that one is fine. But
   `app/src/pages/TaskDetail.jsx` toasts and leaves the user on the detail page
   of a task they cannot have, with no push back to a list.

### Related: the same gap on the edit path, and it is load-bearing

Found while checking the above, recorded here rather than in its own file
because it is the same decision — mobile handled, web deferred and tracked.

`app/src/pages/TaskDetail.jsx:574` gates its Edit button on `isOwner &&
task.status === 'open'`. Accepting a task never moves it out of `open` (only
completion and cancellation do), so **web currently offers Edit on tasks that
already have a supporter working on them** — and today the PATCH succeeds.

The server-side guard added alongside this record (`updateTask` now refuses
`assigned_to_id IS NOT NULL`) closes that hole. The consequence for web is that
those saves now fail, and `saveEdit`'s catch is `toast(e.message || 'Failed to
save')` — `e.message` for this client is the literal `"HTTP 400"`, exactly the
string commit d975a35 removed from the accept path. So after that server deploy,
a web requester editing an accepted task sees `HTTP 400`.

This is still a net improvement — an unexplained refusal beats silently
rewriting a task out from under its supporter — so it does not block the
deploy. Two follow-ups, both small:

1. `saveEdit` should read `e?.body?.error` before `e.message`, like the accept
   handlers already do.
2. The Edit button's condition should be `isOwner && status === 'open' &&
   !task.assigned_to_id`, matching what mobile and the server now enforce.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none

## Context and alternatives
Porting the mobile treatment to web in the same task was out of scope (this pass
was mobile-only) and web's `TaskDetail.jsx` has no equivalent of the mobile Work
tab to pop back to — it would need a deliberate choice of destination
(`/my?tab=available`, most likely) rather than a mechanical copy. Aligning the
copy alone would be a two-line change and could be done at any time; it is
bundled here so both halves get considered together.

Worth noting for whoever picks this up: both clients discriminate this failure
by string-matching the response body (`error === "not available"`). That is the
de-facto cross-client contract, and it is fragile — the string appears twice in
`acceptTask` and any rewording server-side silently downgrades both clients to
their generic error path. A machine-readable code would be better; it was not
introduced here because it would mean changing the server and both clients at
once, for a race whose handling already works.

## Evidence
- `server/main.go` `acceptTask` — assignment is a single guarded `UPDATE …
  WHERE id=$3 AND status='open' AND assigned_to_id IS NULL` followed by a
  `RowsAffected() == 0` check; the read before it is explicitly advisory. Grep
  for writers of `assigned_to_id` returns exactly this one statement, so there
  is no second path that can assign a task. Confirmed atomic — no fix needed.
- `app/src/pages/TaskDetail.jsx:308` and `app/src/pages/My.jsx:157` — both
  already branch on `e?.body?.error === 'not available'` and toast a friendly
  line (commit d975a35). No raw `HTTP 400` reaches the user on web today.
- `mobile/src/app/task/[id].tsx` — the pre-existing mobile handling set an
  error string that rendered *inside* the `isAvailableToAccept` block, which
  unmounts as soon as the following refetch returns the now-assigned task. The
  loser therefore saw nothing at all. Fixed in the same change as this record.
