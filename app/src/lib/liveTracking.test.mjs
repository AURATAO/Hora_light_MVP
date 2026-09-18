import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  broadcastPhase,
  formatDistance,
  formatUpdatedAgo,
  liveStateDetail,
  liveStateLabel,
  shouldPollLive,
} from './liveTracking.js'

/**
 * The two rules from the module header, as tests:
 *
 *   1. Never a coordinate.
 *   2. Never a precision we don't have.
 *
 * Everything below is one of them, or the boundary where a requester would
 * notice if it moved.
 */

test('the states read as English, and an unknown one never leaks a raw enum', () => {
  assert.equal(liveStateLabel('on_the_way'), 'On the way')
  assert.equal(liveStateLabel('almost_there'), 'Almost there')
  assert.equal(liveStateLabel('at_door'), 'At your door')
  assert.equal(liveStateLabel('working'), 'Working')
  assert.equal(liveStateLabel('unavailable'), 'Location unavailable')
  // A server that grows a sixth state must not print it at somebody.
  assert.equal(liveStateLabel('teleporting'), 'Location unavailable')
  assert.equal(liveStateLabel(undefined), 'Location unavailable')
})

test('the detail line is about a person, and names them', () => {
  assert.equal(liveStateDetail('on_the_way', 'Sam'), 'Sam is on their way.')
  assert.equal(liveStateDetail('at_door', 'Sam'), 'Sam has arrived at your address.')
  // A missing name degrades to a role, never to "undefined" or an empty gap.
  assert.equal(liveStateDetail('working', ''), 'Your supporter is clocked in and working.')
  assert.equal(liveStateDetail('working', undefined), 'Your supporter is clocked in and working.')
})

test('an unavailable supporter has a phone problem, not a character problem', () => {
  // The copy blames the signal. Somebody in a lift has not gone missing.
  assert.equal(liveStateDetail('unavailable', 'Sam'), "No recent location from Sam's phone.")
})

test('distance rounds to something a person would say', () => {
  assert.equal(formatDistance(1287), '0.8 mi away')
  assert.equal(formatDistance(4800), '3.0 mi away')
  // Past ten miles the decimal is noise.
  assert.equal(formatDistance(30000), '19 mi away')
})

test('close in, it switches to feet rather than printing 0.0 mi', () => {
  assert.equal(formatDistance(30), '100 ft away')
  assert.equal(formatDistance(90), '300 ft away')
  assert.equal(formatDistance(0), 'right here')
})

test('no distance means no line — never a zero standing in for "we do not know"', () => {
  assert.equal(formatDistance(null), null)
  assert.equal(formatDistance(undefined), null)
  assert.equal(formatDistance(Number.NaN), null)
  assert.equal(formatDistance(-5), null)
})

test('nothing in the copy can print a coordinate', () => {
  // The guard behind rule 1: every public string this module can produce, for
  // a payload that DOES carry coordinates, and none of them contain one.
  const coordish = /\d{1,3}\.\d{4,}/
  const strings = [
    liveStateLabel('at_door'),
    liveStateDetail('at_door', 'Sam'),
    formatDistance(1287),
    formatUpdatedAgo(new Date().toISOString()),
  ]
  for (const s of strings) {
    assert.ok(!coordish.test(String(s)), `"${s}" looks like it carries a coordinate`)
  }
})

test('the freshness line is what makes the state trustworthy', () => {
  const now = Date.parse('2026-09-17T12:00:00Z')
  const ago = (seconds) => formatUpdatedAgo(new Date(now - seconds * 1000).toISOString(), now)
  assert.equal(ago(0), 'updated just now')
  assert.equal(ago(3), 'updated just now')
  assert.equal(ago(12), 'updated 12s ago')
  assert.equal(ago(59), 'updated 59s ago')
  assert.equal(ago(120), 'updated 2m ago')
  assert.equal(ago(3 * 3600), 'updated 3h ago')
  // A clock skew that puts the ping in the future reads as now, not as a
  // negative age.
  assert.equal(formatUpdatedAgo(new Date(now + 5000).toISOString(), now), 'updated just now')
  assert.equal(formatUpdatedAgo(null), null)
  assert.equal(formatUpdatedAgo('not a date'), null)
})

test('polling is scoped to a live task somebody is actually on', () => {
  const live = { isRequester: true, status: 'open', assignedToId: 'u1' }
  assert.equal(shouldPollLive(live), true)
  // The supporter never polls their own position.
  assert.equal(shouldPollLive({ ...live, isRequester: false }), false)
  // Nobody has taken it yet.
  assert.equal(shouldPollLive({ ...live, assignedToId: null }), false)
  // Finished, cancelled, taken down: there is no live position to watch, and
  // the server 404s these anyway.
  for (const status of ['completed', 'cancelled', 'removed']) {
    assert.equal(shouldPollLive({ ...live, status }), false, status)
  }
})

// ── Broadcasting: the supporter's side, and what a pause does to it ─────────
//
// The multi-session guard. A supporter who clocks out mid-task is PAUSED, not
// finished — the screen stays live and they can clock back in — and the thing
// that must not stay live with it is their location.

test('broadcasting follows the clock: working while clocked in, nothing while paused', () => {
  const assignee = { isAssignee: true, status: 'open', enrouteAt: '2026-09-18T09:00:00Z' }

  // Tapped "On my way", not yet started: the pre-clock-in window.
  assert.equal(broadcastPhase({ ...assignee, hasOpenWorklog: false, sessionCount: 0 }), 'enroute')
  // Clocked in: everything captured belongs to the worklog.
  assert.equal(broadcastPhase({ ...assignee, hasOpenWorklog: true, sessionCount: 1 }), 'working')

  // PAUSED — clocked out with the task still live. This is the case the
  // feature creates and the one that must send nothing: 'working' is gone with
  // the open worklog, and 'enroute' does NOT come back to fill the hole, even
  // though enroute_at is still set (it is never cleared).
  assert.equal(broadcastPhase({ ...assignee, hasOpenWorklog: false, sessionCount: 1 }), 'none')

  // Still nothing after the second, third, fourth pause.
  assert.equal(broadcastPhase({ ...assignee, hasOpenWorklog: false, sessionCount: 4 }), 'none')
  // And nothing on a supporter who never tapped "On my way" either.
  assert.equal(
    broadcastPhase({ ...assignee, enrouteAt: null, hasOpenWorklog: false, sessionCount: 1 }),
    'none'
  )
})

test('clocking back in resumes broadcasting, scoped to the new session', () => {
  // The same predicate, before and after: the phase is read from the CURRENT
  // open worklog, so a new session re-arms tracking with nothing carried over
  // from the closed one. (The server agrees — it scopes acceptance to the open
  // worklog, not to the task.)
  const base = { isAssignee: true, status: 'open', enrouteAt: '2026-09-18T09:00:00Z' }
  assert.equal(broadcastPhase({ ...base, hasOpenWorklog: false, sessionCount: 2 }), 'none')
  assert.equal(broadcastPhase({ ...base, hasOpenWorklog: true, sessionCount: 3 }), 'working')
})

test('nobody else broadcasts, and no finished task does', () => {
  const working = { isAssignee: true, status: 'open', hasOpenWorklog: true, sessionCount: 1, enrouteAt: null }
  assert.equal(broadcastPhase(working), 'working')
  // A requester's device never sends a position for someone else's task.
  assert.equal(broadcastPhase({ ...working, isAssignee: false }), 'none')
  // A task that is over has no window, open worklog or not.
  for (const status of ['completed', 'cancelled', 'removed']) {
    assert.equal(broadcastPhase({ ...working, status }), 'none', status)
  }
})

test('mobile carries the same broadcast rule', t => {
  const path = fileURLToPath(new URL('../../../mobile/src/lib/live-tracking.ts', import.meta.url))
  let src
  try {
    src = readFileSync(path, 'utf8')
  } catch {
    t.skip('mobile/ not present')
    return
  }
  assert.ok(src.includes('export function broadcastPhase'), 'mobile has no broadcastPhase')
  // The clause that keeps a pause dark: zero worklogs, not merely no open one.
  assert.ok(
    src.includes('args.enrouteAt && (args.sessionCount ?? 0) === 0'),
    'mobile would re-open the enroute window behind a pause'
  )
})
