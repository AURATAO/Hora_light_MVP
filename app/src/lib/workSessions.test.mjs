// Multi-session clock in/out: the timeline, the rounding, and the two
// properties the whole feature rests on.
//
//   npm test   (node's built-in runner, no dependencies)
//
// The rounding tests are not pedantry. This module renders a per-session
// minute count beside a total the SERVER computed, on the same card, to the
// person being charged. If the two round differently the card contradicts
// itself — four rows reading 3 min over a total reading 11 min — and the
// number a requester will believe is the one they can add up themselves.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import {
  GAP_LABEL,
  LAUNDRY_WAIT_HINT,
  PAUSED_REQUESTER,
  PAUSED_SUPPORTER,
  buildSessionTimeline,
  cumulativeLoggedMinutes,
  gapsNote,
  isPaused,
  sessionMinutes,
  showsWaitHint,
} from './workSessions.js'

const iso = (hh, mm, ss = 0) =>
  `2026-09-18T${String(hh).padStart(2, '0')}:${String(mm).padStart(2, '0')}:${String(ss).padStart(2, '0')}Z`

const session = (id, start, end) => ({ id, startAt: start, endAt: end })

// ── The rounding, against the SQL it has to match ──────────────────────────

test('a session rounds up to the whole minute, exactly as Postgres does', () => {
  // ceil(epoch/60): the server's arithmetic, not Math.round.
  assert.equal(sessionMinutes(iso(9, 0), iso(9, 10)), 10)
  assert.equal(sessionMinutes(iso(9, 0), iso(9, 10, 1)), 11)
  assert.equal(sessionMinutes(iso(9, 0), iso(9, 10, 59)), 11)
  assert.equal(sessionMinutes(iso(9, 0), iso(10, 30)), 90)
})

test('the one-minute floor: a 30-second visit is a minute, and twice is two', () => {
  // greatest(m,1) per session — which is why the ceiling is applied per
  // session and never to the sum. Two 30-second sessions bill two minutes;
  // one 60-second session bills one.
  assert.equal(sessionMinutes(iso(9, 0), iso(9, 0, 30)), 1)
  const twoVisits = [
    session('a', iso(9, 0), iso(9, 0, 30)),
    session('b', iso(11, 0), iso(11, 0, 30)),
  ]
  assert.equal(cumulativeLoggedMinutes(twoVisits), 2)
})

test('a non-positive session contributes nothing — a clock glitch is not work', () => {
  // Matches the server's WHERE end_at > start_at, which drops the row before
  // greatest(m,1) can floor it up to a minute.
  assert.equal(sessionMinutes(iso(9, 0), iso(9, 0)), 0)
  assert.equal(sessionMinutes(iso(9, 5), iso(9, 0)), 0)
  assert.equal(sessionMinutes('not a date', iso(9, 0)), 0)
})

// ── The timeline ───────────────────────────────────────────────────────────

test('a single closed session is one row and no gaps', () => {
  const timeline = buildSessionTimeline([session('a', iso(9, 0), iso(9, 25))])
  assert.equal(timeline.length, 1)
  assert.deepEqual(timeline[0], {
    kind: 'session',
    id: 'a',
    startAt: iso(9, 0),
    endAt: iso(9, 25),
    minutes: 25,
    running: false,
  })
  assert.equal(gapsNote(timeline), null)
})

test('the laundry shape: drop off, wait, collect — and the wait is a free gap', () => {
  const timeline = buildSessionTimeline([
    session('a', iso(9, 0), iso(9, 20)),   // drop off
    session('b', iso(10, 5), iso(10, 15)), // collect, 45 min later
  ])
  assert.deepEqual(
    timeline.map(e => [e.kind, e.minutes]),
    [['session', 20], ['gap', 45], ['session', 10]]
  )
  // 30 minutes of work, 45 minutes of waiting, and the waiting is named.
  assert.equal(cumulativeLoggedMinutes(timeline.filter(e => e.kind === 'session').map(
    e => session(e.id, e.startAt, e.endAt)
  )), 30)
  assert.equal(gapsNote(timeline), '45 min paused, not billed.')
})

test('gaps are emitted only BETWEEN sessions — never before the first', () => {
  const timeline = buildSessionTimeline([session('a', iso(9, 0), iso(9, 20))])
  assert.equal(timeline.filter(e => e.kind === 'gap').length, 0)
})

test('the trailing pause after the last clock-out is not a gap', () => {
  // It has not finished. Drawing it would put a growing "free" row under a
  // supporter who may simply be done — the paused banner says it instead.
  const timeline = buildSessionTimeline([session('a', iso(9, 0), iso(9, 20))], {
    nowMs: Date.parse(iso(11, 0)),
  })
  assert.equal(timeline.length, 1)
  assert.equal(timeline[0].kind, 'session')
})

test('the running session is live, flagged, and left out of the closed total', () => {
  const timeline = buildSessionTimeline(
    [session('a', iso(9, 0), iso(9, 20)), session('b', iso(10, 0), null)],
    { nowMs: Date.parse(iso(10, 7)) }
  )
  assert.deepEqual(
    timeline.map(e => [e.kind, e.minutes, e.running ?? null]),
    [['session', 20, false], ['gap', 40, null], ['session', 7, true]]
  )
  // The billed figure still comes from the server, which counts CLOSED
  // sessions only — this module never asserts a cost.
  assert.equal(timeline.filter(e => e.kind === 'session' && !e.running)[0].minutes, 20)
})

test('worklogs arriving out of order are sorted before anything is derived', () => {
  const timeline = buildSessionTimeline([
    session('b', iso(10, 5), iso(10, 15)),
    session('a', iso(9, 0), iso(9, 20)),
  ])
  assert.deepEqual(timeline.map(e => e.kind), ['session', 'gap', 'session'])
  assert.equal(timeline[0].id, 'a')
})

test('an empty or missing list is an empty timeline, not a crash', () => {
  assert.deepEqual(buildSessionTimeline([]), [])
  assert.deepEqual(buildSessionTimeline(null), [])
  assert.deepEqual(buildSessionTimeline(undefined), [])
  assert.equal(cumulativeLoggedMinutes(null), 0)
})

// ── The cap, on CUMULATIVE time ────────────────────────────────────────────
//
// The backend has always clamped the SUM (billing.go cappedMinutes) and warned
// on the SUM (timecap.go liveLoggedMinutes). These are the UI-facing guards on
// the same rule: the screen must ask the question the same way the biller does,
// because the version that broke silently under multi-session was a badge
// derived from the CURRENT session's elapsed time.

test('the ceiling is reached by the sum of sessions, not by any one of them', () => {
  // Estimate 30, auto-extend consent → cap 45. Four sessions of 20 minutes:
  // no single session is over the estimate, and the task is 35 minutes past
  // the ceiling.
  const capMinutes = 45
  const sessions = [
    session('a', iso(9, 0), iso(9, 20)),
    session('b', iso(10, 0), iso(10, 20)),
    session('c', iso(11, 0), iso(11, 20)),
    session('d', iso(12, 0), iso(12, 20)),
  ]
  for (const s of sessions) {
    assert.ok(sessionMinutes(s.startAt, s.endAt) < capMinutes, 'no single session is over the cap')
  }
  assert.equal(cumulativeLoggedMinutes(sessions), 80)
  assert.ok(cumulativeLoggedMinutes(sessions) > capMinutes, 'the SUM is well past it')
})

test('the est−5 warning fires on cumulative time, across a pause', () => {
  // A 30-minute estimate with consent caps at 45 and warns at 25
  // (timeCapWarningMinutes = AGREED − CapWarningLeadMinutes, where agreed is
  // the estimate plus approved extensions and NOT the auto-extend fuse). Two
  // sessions of 21 minutes cross that line; neither session alone comes close.
  const warnAt = 25
  const first = session('a', iso(9, 0), iso(9, 21))
  const second = session('b', iso(10, 0), iso(10, 21))
  assert.ok(sessionMinutes(first.startAt, first.endAt) < warnAt)
  assert.ok(sessionMinutes(second.startAt, second.endAt) < warnAt)
  assert.equal(cumulativeLoggedMinutes([first, second]), 42)
  assert.ok(cumulativeLoggedMinutes([first, second]) >= warnAt, 'the warning is owed')
})

test('cumulative time includes the session still running — a warning cannot wait for a clock-out', () => {
  // The server's liveLoggedMinutes coalesces a null end_at to now() for exactly
  // this reason: a cap that only notices closed sessions warns after the thing
  // it was warning about.
  const entries = [session('a', iso(9, 0), iso(9, 30)), session('b', iso(10, 0), null)]
  assert.equal(cumulativeLoggedMinutes(entries, { nowMs: Date.parse(iso(10, 12)) }), 42)
})

// ── Paused state ───────────────────────────────────────────────────────────

test('paused means clocked out on a live task that has been worked', () => {
  const paused = { hasOpenWorklog: false, sessionCount: 1, status: 'open' }
  assert.equal(isPaused(paused), true)
  // Clocked in is not paused.
  assert.equal(isPaused({ ...paused, hasOpenWorklog: true }), false)
  // Never started is not paused — it is "not started", and the screen says so.
  assert.equal(isPaused({ ...paused, sessionCount: 0 }), false)
  // A finished task is not paused, whatever its sessions look like.
  for (const status of ['completed', 'cancelled', 'removed']) {
    assert.equal(isPaused({ ...paused, status }), false, status)
  }
})

// ── The copy ───────────────────────────────────────────────────────────────

test('both roles are told the pause is free, in their own words', () => {
  assert.equal(PAUSED_SUPPORTER, 'Paused — not billing. Clock back in when you resume.')
  assert.equal(PAUSED_REQUESTER, "Paused — you're not being charged for this time.")
  assert.equal(GAP_LABEL, 'Paused — not billed')
})

test('the pause copy is calm — no alarm words, no exclamation (DESIGN.md §6)', () => {
  for (const line of [PAUSED_SUPPORTER, PAUSED_REQUESTER, GAP_LABEL, LAUNDRY_WAIT_HINT]) {
    assert.ok(!line.includes('!'), `"${line}" shouts`)
    assert.ok(
      !/\b(warning|error|alert|stopped|failed|problem)\b/i.test(line),
      `"${line}" reads as a fault — a pause is a normal, wanted thing`
    )
  }
})

test('the wait hint is laundry-only — every other category just taps Complete', () => {
  assert.equal(showsWaitHint('laundry'), true)
  for (const category of ['task', 'grocery', 'delivery', 'queue', 'companion', undefined]) {
    assert.equal(showsWaitHint(category), false, String(category))
  }
  assert.equal(
    LAUNDRY_WAIT_HINT,
    'Waiting for a wash or pickup? Clock out while you wait — waiting time is free.'
  )
})

// ── Parity with mobile ─────────────────────────────────────────────────────

// The logic is duplicated across the package boundary Metro will not let us
// cross (same arrangement as liveTracking.js ↔ live-tracking.ts). This is the
// guard on that duplication. Skipped, not failed, when mobile isn't checked
// out beside app/.
test('mobile carries the same session logic and the same words', t => {
  const path = fileURLToPath(new URL('../../../mobile/src/lib/work-sessions.ts', import.meta.url))
  let src
  try {
    src = readFileSync(path, 'utf8')
  } catch {
    t.skip('mobile/ not present')
    return
  }

  // The copy, byte for byte. Drift here means the two platforms describe the
  // same billing rule differently to the same pair of users.
  for (const [name, value] of [
    ['PAUSED_SUPPORTER', PAUSED_SUPPORTER],
    ['PAUSED_REQUESTER', PAUSED_REQUESTER],
    ['GAP_LABEL', GAP_LABEL],
    ['LAUNDRY_WAIT_HINT', LAUNDRY_WAIT_HINT],
  ]) {
    assert.ok(
      src.includes(JSON.stringify(value)),
      `${name} differs from mobile — the platforms would describe the same rule differently`
    )
  }

  // The rounding, which is the half that shows up as money.
  assert.ok(src.includes('Math.max(1, Math.ceil(ms / 60000))'), 'mobile rounds sessions differently')
  assert.ok(src.includes('if (ms <= 0) return 0;'), 'mobile keeps a non-positive session')

  for (const fn of [
    'export function sessionMinutes',
    'export function buildSessionTimeline',
    'export function cumulativeLoggedMinutes',
    'export function isPaused',
    'export function showsWaitHint',
    'export function gapsNote',
  ]) {
    assert.ok(src.includes(fn), `mobile is missing ${fn}`)
  }
})
