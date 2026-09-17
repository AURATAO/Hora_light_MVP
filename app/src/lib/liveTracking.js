/**
 * The copy for live supporter tracking, and nothing else.
 *
 * Pure functions, in their own module, for the same reason paymentCopy.js is:
 * the words ARE the feature here. A requester watching somebody approach their
 * home is reading these strings every fifteen seconds, and two rules govern
 * all of them:
 *
 *   1. NEVER A COORDINATE. Not in a label, not in a tooltip, not as a
 *      fallback when the distance is unknown. "45.46412, 9.18998" is a
 *      debugging tool; a person's live position rendered as text to another
 *      person is something else. The map draws the dot — the text says how
 *      far, and that is all.
 *
 *   2. NEVER A PRECISION WE DON'T HAVE. Consumer GPS is good to 10-50m, so
 *      distances round hard and a position the server has marked stale gets no
 *      distance at all (the server already withholds it; this is the second
 *      lock). "0.8 mi away" is a claim we can stand behind. "0.83 mi away" is
 *      not.
 *
 * `mobile/src/lib/live-tracking.ts` is the same logic for React Native — Metro
 * cannot import across that package boundary, so the two are kept in step by
 * hand, exactly like design-tokens/colors.js and mobile/src/theme/tokens.ts.
 * Change one, change the other.
 */

/**
 * Imperial, per the product spec ("0.8 mi away"). One constant, so a pilot
 * city that thinks in kilometres is a one-line change rather than a hunt
 * through the copy.
 */
export const DISTANCE_UNITS = 'imperial'

const METERS_PER_MILE = 1609.344
const METERS_PER_FOOT = 0.3048

/** The states the server derives. Anything else is treated as unavailable. */
export const LIVE_STATES = ['on_the_way', 'almost_there', 'at_door', 'working', 'unavailable']

const STATE_LABELS = {
  on_the_way: 'On the way',
  almost_there: 'Almost there',
  at_door: 'At your door',
  working: 'Working',
  unavailable: 'Location unavailable',
}

/**
 * The one word the card leads with. An unknown state reads as unavailable
 * rather than as itself: a server that grows a sixth state must not be able to
 * print a raw enum value at somebody.
 */
export function liveStateLabel(state) {
  return STATE_LABELS[state] || STATE_LABELS.unavailable
}

/**
 * A sentence under the label, so the state is never a bare adjective. Takes
 * the supporter's name because "Sam is on the way" is a different message from
 * "On the way" — one of them is about a person.
 */
export function liveStateDetail(state, supporterName) {
  const who = (supporterName || '').trim() || 'Your supporter'
  switch (state) {
    case 'on_the_way':
      return `${who} is on their way.`
    case 'almost_there':
      return `${who} is nearly there.`
    case 'at_door':
      return `${who} has arrived at your address.`
    case 'working':
      return `${who} is clocked in and working.`
    default:
      // Deliberately about the PHONE, not about the person. A supporter whose
      // signal dropped in a lift has not gone missing, and the copy should not
      // imply they have.
      return `No recent location from ${who}'s phone.`
  }
}

/**
 * Metres → the distance a person says out loud.
 *
 * Under a tenth of a mile it switches to feet, rounded to 10, because "0.0 mi"
 * is not an answer and "at the door" deserves a number that moves. Null in,
 * null out: no distance means no line, never a zero standing in for a value we
 * do not have (rule 1 of paymentCopy.js, same reasoning).
 */
export function formatDistance(meters) {
  if (meters == null || !Number.isFinite(meters) || meters < 0) return null
  const miles = meters / METERS_PER_MILE
  if (miles < 0.1) {
    const feet = Math.round(meters / METERS_PER_FOOT / 10) * 10
    if (feet <= 0) return 'right here'
    return `${feet} ft away`
  }
  if (miles < 10) return `${miles.toFixed(1)} mi away`
  return `${Math.round(miles)} mi away`
}

/**
 * "updated 12s ago". The half of the card that tells a requester whether to
 * trust the other half — a beautiful state label on a four-minute-old fix is
 * the failure this line exists to prevent, so it is never omitted.
 */
export function formatUpdatedAgo(updatedAt, nowMs = Date.now()) {
  if (!updatedAt) return null
  const then = new Date(updatedAt).getTime()
  if (!Number.isFinite(then)) return null
  const seconds = Math.max(0, Math.round((nowMs - then) / 1000))
  if (seconds < 5) return 'updated just now'
  if (seconds < 60) return `updated ${seconds}s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `updated ${minutes}m ago`
  const hours = Math.round(minutes / 60)
  return `updated ${hours}h ago`
}

/**
 * Should the requester's screen be polling at all?
 *
 * The card is for a task that is live and has somebody on it. Everything else
 * — unassigned, finished, cancelled, taken down — has no live position, and
 * the server agrees (GET /tasks/:id/live 404s outside exactly this set). Kept
 * here so web and mobile cannot drift into polling different things.
 */
export function shouldPollLive({ isRequester, status, assignedToId }) {
  return Boolean(isRequester && status === 'open' && assignedToId)
}
