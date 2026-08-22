// Traction 3 round configuration for the webapp.
//
// This is a deliberate, small duplicate of mobile/src/lib/beta-notice.ts
// (TRACTION_3_CONFIG + isTractionWindowActive). The two codebases don't share a
// package, so the dates live in both — EDIT BOTH when the round changes, or web
// and mobile will disagree about whether the questionnaire is live.
export const TRACTION_3_CONFIG = {
  /** Test round this config describes — for logs and future round switching. */
  round: 'Traction 3',
  /** Dates the round runs, as shown to users. En-dash, matching mobile. */
  window: 'Aug 24–28',
  /**
   * The machine-readable window — the only thing that decides whether a
   * round-scoped surface is live (see isTractionWindowActive). `endsAt` is the
   * first instant AFTER the last day, so Aug 28 counts in full. Offsets are
   * NYC's, which is UTC-4 in August; the round is a Manhattan pilot, so the day
   * boundaries are local ones, not the browser's.
   *
   * `startsAt` deliberately runs ahead of the `window` copy above: the round is
   * announced as Aug 24–28, but the questionnaire opens Aug 18 so it is live
   * and testable the moment the build ships. Only the opening edge moves.
   */
  startsAt: '2026-08-18T00:00:00-04:00',
  endsAt: '2026-08-29T00:00:00-04:00',
  /** Billing rate quoted in the post-task questionnaire. */
  perMinuteRate: '$0.50',
}

/**
 * QA escape hatch, mirroring mobile's EXPO_PUBLIC_QA_FORCE_TRACTION: forces the
 * round open regardless of today's date so the questionnaire can be exercised
 * outside the window. VITE_* values are inlined at build time, so a build made
 * without it set (Vercel, production) ships with it permanently false. Set it
 * only in a local, gitignored app/.env.
 *
 * Read through optional chaining so this module also imports cleanly under
 * plain node (`npm test`), where `import.meta.env` doesn't exist.
 */
const QA_FORCE_TRACTION_WINDOW = import.meta.env?.VITE_QA_FORCE_TRACTION === '1'

/**
 * Is the Traction 3 round running right now? The single date check behind every
 * round-scoped surface: when it goes false the app returns to its normal
 * behaviour on its own, with no flag to remember to flip.
 */
export function isTractionWindowActive(now = new Date()) {
  if (QA_FORCE_TRACTION_WINDOW) return true
  const t = now.getTime()
  return t >= Date.parse(TRACTION_3_CONFIG.startsAt) && t < Date.parse(TRACTION_3_CONFIG.endsAt)
}
