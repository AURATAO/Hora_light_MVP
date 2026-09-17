import type { LiveState } from "./types";

/**
 * The copy for live supporter tracking. The React Native half of
 * `app/src/lib/liveTracking.js` — same functions, same strings, same rules.
 *
 * Duplicated by hand rather than imported for the reason design-tokens are
 * (decisions/D-03): Metro cannot import across the app/ ↔ mobile/ package
 * boundary. Change one, change the other; the web copy carries the node:test
 * suite that pins every string below.
 *
 * The two rules, restated here because this is the file somebody will edit:
 *
 *   1. NEVER A COORDINATE. The map draws the dot; the text says how far. A
 *      person's live position rendered as text to another person is a
 *      different product from the one we are shipping.
 *
 *   2. NEVER A PRECISION WE DON'T HAVE. Consumer GPS is good to 10-50m, so
 *      distances round hard, and a position the server marked stale carries no
 *      distance at all.
 */

const METERS_PER_MILE = 1609.344;
const METERS_PER_FOOT = 0.3048;

const STATE_LABELS: Record<LiveState, string> = {
  on_the_way: "On the way",
  almost_there: "Almost there",
  at_door: "At your door",
  working: "Working",
  unavailable: "Location unavailable",
};

/** An unknown state reads as unavailable — never as a raw enum value. */
export function liveStateLabel(state: string | null | undefined): string {
  return STATE_LABELS[state as LiveState] ?? STATE_LABELS.unavailable;
}

/** The sentence under the label. Named, because it is about a person. */
export function liveStateDetail(state: string | null | undefined, supporterName?: string): string {
  const who = (supporterName ?? "").trim() || "Your supporter";
  switch (state) {
    case "on_the_way":
      return `${who} is on their way.`;
    case "almost_there":
      return `${who} is nearly there.`;
    case "at_door":
      return `${who} has arrived at your address.`;
    case "working":
      return `${who} is clocked in and working.`;
    default:
      // About the phone, not the person. A signal lost in a lift is not a
      // supporter who has gone missing.
      return `No recent location from ${who}'s phone.`;
  }
}

/**
 * Metres → what a person says out loud. Null in, null out: no distance means
 * no line, never a zero standing in for a value we do not have.
 */
export function formatDistance(meters: number | null | undefined): string | null {
  if (meters == null || !Number.isFinite(meters) || meters < 0) return null;
  const miles = meters / METERS_PER_MILE;
  if (miles < 0.1) {
    const feet = Math.round(meters / METERS_PER_FOOT / 10) * 10;
    if (feet <= 0) return "right here";
    return `${feet} ft away`;
  }
  if (miles < 10) return `${miles.toFixed(1)} mi away`;
  return `${Math.round(miles)} mi away`;
}

/**
 * "updated 12s ago" — the half of the card that tells the requester whether to
 * trust the other half, so it is never omitted.
 */
export function formatUpdatedAgo(
  updatedAt: string | null | undefined,
  nowMs: number = Date.now()
): string | null {
  if (!updatedAt) return null;
  const then = new Date(updatedAt).getTime();
  if (!Number.isFinite(then)) return null;
  const seconds = Math.max(0, Math.round((nowMs - then) / 1000));
  if (seconds < 5) return "updated just now";
  if (seconds < 60) return `updated ${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `updated ${minutes}m ago`;
  return `updated ${Math.round(minutes / 60)}h ago`;
}

/**
 * Should this screen be polling at all? Matches exactly what the server will
 * answer — GET /tasks/:id/live 404s outside this set — so the two clients
 * cannot drift into polling different things.
 */
export function shouldPollLive(args: {
  isRequester: boolean;
  status: string | null | undefined;
  assignedToId: string | null | undefined;
}): boolean {
  return Boolean(args.isRequester && args.status === "open" && args.assignedToId);
}
