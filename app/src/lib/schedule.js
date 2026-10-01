// The Post Task "When" fields, as the two strings <input type="date"> and
// <input type="time"> hold. Local time throughout: the form is filled in on
// the requester's clock, and only the final ISO string is UTC.

/** Minute step of the time field — the same 5 minutes mobile's picker uses. */
export const SCHEDULE_STEP_MINUTES = 5

const pad = (n) => String(n).padStart(2, '0')

/** "2026-10-01" in LOCAL time. `toISOString().split('T')[0]` is the UTC date,
 *  which in New York is already tomorrow from 8 PM — as the date field's `min`
 *  it made tonight unpickable, exactly when the evening rate applies. */
export function localDateStr(d) {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** "21:35" in local time. */
export function localTimeStr(d) {
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** `d` moved forward to the next step boundary, seconds zeroed. A time already
 *  on a boundary is left where it is. */
export function roundUpToStep(d, stepMinutes = SCHEDULE_STEP_MINUTES) {
  // Epoch arithmetic is safe here: every UTC offset is a multiple of the step.
  const stepMs = stepMinutes * 60 * 1000
  return new Date(Math.ceil(d.getTime() / stepMs) * stepMs)
}

/** The date and time fields for a given moment, snapped to the step. */
export function scheduleFields(d) {
  const at = roundUpToStep(d)
  return { date: localDateStr(at), time: localTimeStr(at) }
}

/** Where the fields start when "Schedule" is first chosen: an hour from now,
 *  the same default mobile uses, so the form is valid the moment it appears. */
export function defaultSchedule(now = new Date()) {
  return scheduleFields(new Date(now.getTime() + 60 * 60 * 1000))
}
