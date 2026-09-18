/**
 * Multi-session work: turning a task's worklogs into the timeline both sides
 * read, and the words that go around it.
 *
 * A supporter may clock out and clock back in as many times as a task needs —
 * a laundry drop-off, a queue that turns out to be an hour, a wash cycle. The
 * backend has always allowed it (server/main.go clockIn refuses only a SECOND
 * OPEN session) and has always billed it correctly (billing.go
 * totalClosedMinutes sums closed sessions; billableMinutes consumes the
 * 15-minute inclusion ONCE per task, not once per session). What was missing
 * was any way to see it: one running timer, and a screen that went terminal
 * after the first clock-out.
 *
 * THE ONE RULE THIS MODULE EXISTS TO MAKE VISIBLE: the gaps are free. Time
 * between a clock-out and the next clock-in is not billed, is not rounded into
 * anything, and never reaches the invoice. A supporter who sits in a
 * launderette for forty minutes on the clock is charging somebody for sitting
 * in a launderette; one who clocks out is not. The timeline says so on every
 * gap, in both roles' copy, because a billing rule nobody can see is a billing
 * rule nobody trusts.
 *
 * NO MONEY IS COMPUTED HERE (S-05). Minutes per session, yes — those are a
 * rendering of two timestamps the server sent, and the rounding below is
 * matched to the server's so the list cannot add up to something other than
 * the total beside it. Every cents figure on screen comes from the server's
 * quote, untouched.
 *
 * `mobile/src/lib/work-sessions.ts` is the same logic for React Native. Metro
 * cannot import across that package boundary, so the two are kept in step by
 * hand and by test (workSessions.test.mjs reads the mobile file). Change one,
 * change the other.
 */

/**
 * One session's billable minutes, rounded EXACTLY as Postgres rounds them in
 * billing.go's totalClosedMinutes:
 *
 *     ceil(extract(epoch from (end_at - start_at))/60.0), floored at 1,
 *     over rows where end_at is not null and end_at > start_at
 *
 * Both halves matter. Ceiling-per-session (not on the sum) is why two
 * 30-second visits are two minutes rather than one. The one-minute floor is
 * why a session that logged real time can never show 0 min. And a session with
 * a non-positive length contributes nothing at all, matching the WHERE clause
 * rather than the floor — a clock-out that landed before its own clock-in is a
 * clock glitch, not a minute of work.
 */
export function sessionMinutes(startAt, endAt) {
  const start = new Date(startAt).getTime()
  const end = new Date(endAt).getTime()
  if (!Number.isFinite(start) || !Number.isFinite(end)) return 0
  const ms = end - start
  if (ms <= 0) return 0
  return Math.max(1, Math.ceil(ms / 60000))
}

/**
 * The minutes a gap lasted. Unbilled, so nothing downstream depends on this
 * number being the server's — it is rounded the same way only so that a
 * timeline reads consistently, and floored at one so a real pause never
 * renders as "0 min free".
 */
function gapMinutes(startAt, endAt) {
  return sessionMinutes(startAt, endAt)
}

/**
 * Worklogs → the ordered timeline of sessions and the free gaps between them.
 *
 * `entries` are `{ id, startAt, endAt }` with a null `endAt` for the session
 * still running; each client maps its own wire shape into that (web's payload
 * spells them `start`/`end`, mobile's normalizes to `start_at`/`end_at`).
 *
 * Output, in chronological order:
 *
 *   { kind: 'session', id, startAt, endAt, minutes, running }
 *   { kind: 'gap',     startAt, endAt, minutes }
 *
 * A gap is emitted between a closed session and whatever follows it — only
 * between two sessions, never before the first or after the last. The trailing
 * pause after the most recent clock-out is deliberately NOT a gap: it has not
 * finished, nobody knows how long it will be, and drawing it would put a
 * growing "free" row under a supporter who may simply be done. The paused
 * banner says that instead.
 *
 * `nowMs` closes the running session for display only. Its minutes are the
 * live elapsed time, which is what the supporter's own clock shows; they are
 * NOT in the billed total, because the server bills closed sessions only.
 */
export function buildSessionTimeline(entries, { nowMs = Date.now() } = {}) {
  const sorted = [...(entries || [])]
    .filter(e => e && e.startAt)
    .sort((a, b) => new Date(a.startAt) - new Date(b.startAt))

  const timeline = []
  let previousEnd = null

  for (const entry of sorted) {
    const running = !entry.endAt
    // Only a CLOSED session opens a gap behind it. Two open sessions cannot
    // exist (the server refuses the second), so this cannot silently swallow
    // one.
    if (previousEnd) {
      const minutes = gapMinutes(previousEnd, entry.startAt)
      if (minutes > 0) {
        timeline.push({ kind: 'gap', startAt: previousEnd, endAt: entry.startAt, minutes })
      }
    }
    timeline.push({
      kind: 'session',
      id: entry.id,
      startAt: entry.startAt,
      endAt: entry.endAt || null,
      minutes: running
        ? sessionMinutes(entry.startAt, new Date(nowMs).toISOString())
        : sessionMinutes(entry.startAt, entry.endAt),
      running,
    })
    previousEnd = running ? null : entry.endAt
  }

  return timeline
}

/**
 * Total minutes logged so far INCLUDING the session still running — the same
 * number the server's liveLoggedMinutes computes, and the one every cap
 * question is asked against.
 *
 * This is the fix for the thing multi-session quietly broke on both clients:
 * an "over the estimated time" badge derived from the CURRENT session's
 * elapsed time. Across four sessions of twenty minutes each, that badge never
 * fires on a task an hour past its estimate, because no single session is.
 * The ceiling was never per-session — billing clamps the SUM (billing.go
 * cappedMinutes) and the cap warning fires on the SUM (timecap.go
 * liveLoggedMinutes) — so the screen has to ask the same question.
 *
 * Prefer the server's own figure where there is one: `capState.logged_minutes`
 * is authoritative and arrives on every worklogs read. This exists for the
 * seconds between a local clock-in and the refetch that confirms it, and for
 * tasks with no estimate, where there is no cap state to read.
 */
export function cumulativeLoggedMinutes(entries, { nowMs = Date.now() } = {}) {
  return (entries || []).reduce((total, entry) => {
    if (!entry || !entry.startAt) return total
    const end = entry.endAt || new Date(nowMs).toISOString()
    return total + sessionMinutes(entry.startAt, end)
  }, 0)
}

/** Is the supporter between sessions — clocked out, with the task still live? */
export function isPaused({ hasOpenWorklog, sessionCount, status }) {
  return Boolean(status === 'open' && sessionCount > 0 && !hasOpenWorklog)
}

// ── The copy ───────────────────────────────────────────────────────────────
//
// Calm, not alarming (DESIGN.md §6: sentence case, no exclamation marks). A
// pause is a normal, WANTED thing — the whole feature exists to make clocking
// out the easy choice — so nothing here is styled or worded as a warning. The
// requester's line and the supporter's line say the same fact from the two
// sides of it, and both lead with the money, because that is the part either
// of them would otherwise worry about.

/** Shown on the supporter's own active-task card while they are between sessions. */
export const PAUSED_SUPPORTER = 'Paused — not billing. Clock back in when you resume.'

/** The requester's half of the same fact, on their copy of the task. */
export const PAUSED_REQUESTER = "Paused — you're not being charged for this time."

/** The label on every gap row in the timeline, both roles. */
export const GAP_LABEL = 'Paused — not billed'

/**
 * The educational line, on the supporter's active screen for laundry tasks
 * only.
 *
 * Multi-session is available on EVERY category — a plain errand simply taps
 * Complete after clocking out, and needs no explanation. Laundry is where the
 * habit has to be taught, because the wait is long, it is obviously "part of
 * the task", and staying on the clock through a wash cycle is both the
 * intuitive thing to do and the expensive one. One line, once, on the one
 * category where it pays for itself.
 */
export const LAUNDRY_WAIT_HINT =
  'Waiting for a wash or pickup? Clock out while you wait — waiting time is free.'

/** Categories that get LAUNDRY_WAIT_HINT. */
export function showsWaitHint(category) {
  return category === 'laundry'
}

/**
 * The line under the session list explaining why the minutes above may not sum
 * to what is being billed. Only said when there is actually a gap to explain —
 * an unprompted "gaps are free" on a task that never paused is noise.
 */
export function gapsNote(timeline) {
  const gaps = (timeline || []).filter(e => e.kind === 'gap')
  if (gaps.length === 0) return null
  const minutes = gaps.reduce((total, gap) => total + gap.minutes, 0)
  return `${minutes} min paused, not billed.`
}
