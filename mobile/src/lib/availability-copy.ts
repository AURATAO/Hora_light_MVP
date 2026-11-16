/**
 * The availability card's words (Tasks tab).
 *
 * How long "available" lasts is the SERVER's (available_for_minutes), so the
 * card never states a number the backend has moved on from. An older backend
 * sends none, and the card then says it turns off by itself without naming
 * a length.
 */

/** 240 -> "4 hours", 60 -> "1 hour", 90 -> "90 minutes". "" when unknown. */
export function availabilityWindow(minutes: unknown): string {
  const m = typeof minutes === "number" && Number.isFinite(minutes) ? Math.round(minutes) : 0;
  if (m <= 0) return "";
  if (m % 60 === 0) return m === 60 ? "1 hour" : `${m / 60} hours`;
  return `${m} minutes`;
}

/** The line under "Get task offers". */
export function availabilityOffLine(minutes: unknown): string {
  const window = availabilityWindow(minutes);
  return window
    ? `Get new tasks as notifications. Turns off after ${window}.`
    : "Get new tasks as notifications. Turns off by itself.";
}

/** The line under "You're available". */
export function availabilityOnLine(until: string): string {
  return until ? `Until ${until}` : "On for now";
}
