// Everything that changes between beta test rounds lives in this one object —
// dates, coverage window, purchase cap. The copy below is
// built from it, so the next round is a single edit here rather than a hunt
// through screen files. Web (app/src/components/BetaModal.jsx) still carries
// its own copy of the previous round's text; keep the two in sync by hand when
// the round changes.
import type { TaskCategory } from "./types";

export const TRACTION_3_CONFIG = {
  /** Test round this notice describes — for logs and future round switching. */
  round: "Traction 3",
  /** Dates the round runs, as shown to users. En-dash, matching web. */
  window: "Aug 24–28",
  /**
   * The same window, machine-readable — the only thing that decides whether a
   * round-specific surface is live (see isTractionWindowActive). `endsAt` is
   * the first instant AFTER the last day, so Aug 28 counts in full. Offsets
   * are NYC's, which is UTC-4 in August; the round is a Manhattan pilot, so
   * the day boundaries are local ones, not the device's.
   */
  startsAt: "2026-08-24T00:00:00-04:00",
  endsAt: "2026-08-29T00:00:00-04:00",
  /** Daily hours supporters are dispatched. En-dash, matching web. */
  coverageHours: "11am–3pm",
  /** Ceiling on what a supporter will spend out of pocket, in whole dollars. */
  purchaseCapDollars: 30,
  /** Billing rate quoted in the post-task questionnaire. */
  perMinuteRate: "$0.50",
  /** Service area for this round. */
  area: "Midtown Manhattan, NYC",
} as const;

const c = TRACTION_3_CONFIG;

// The beta notice, word for word. The dashes here are en/em-dashes, matching
// web exactly — both platforms show the same terms.
export const BETA_NOTICE_COPY = {
  heading: "You're in. Welcome to HO:RA Beta.",
  intro: [
    `HO:RA is a minute-billing platform for urban support — real people helping with real tasks in ${c.area}.`,
    `This is a closed beta pilot, running ${c.window}. Here's what to know:`,
  ],
  points: [
    "All tasks are free this round — no platform fees",
    `Coverage hours: ${c.coverageHours}, ASAP tasks only during this window`,
    `This round runs ${c.window} — access outside this window may be limited`,
    "Every task is manually reviewed by the HO:RA team before a supporter is dispatched",
    "Reimbursement only: you cover actual item costs (e.g. a $5 coffee), nothing else",
    `Purchase cap: $${c.purchaseCapDollars} max per task`,
  ],
  finePrint:
    "By continuing, you agree to use this platform responsibly and understand this is an early-stage test.",
  acknowledgement:
    "I understand this is a beta — things may change and some tasks may not be fulfilled",
  cta: "Enter HO:RA",
} as const;

// Categories switched off for this round: shown everywhere they were before,
// but greyed out and un-selectable. Companionship is one category wearing two
// values — "companion" is what the picker and web submit, "companionship" is
// the label-flavoured value Home's shortcut row and the AI parser still
// produce (see companionship-policy.ts) — so both have to be listed or the
// lock leaks through whichever one is missing.
//
// Re-enabling next round is deleting the entries: every surface reads this
// list, so an empty array restores the previous behaviour exactly.
export const DISABLED_CATEGORIES: readonly TaskCategory[] = ["companion", "companionship"];

export function isCategoryDisabled(category: TaskCategory | undefined): boolean {
  return !!category && DISABLED_CATEGORIES.includes(category);
}

/** Marker under a disabled category in any picker. */
export const DISABLED_CATEGORY_BADGE = "Coming soon";

/** Shown when a task somehow arrives already set to a disabled category. */
export const DISABLED_CATEGORY_NOTICE = "Companionship tasks aren't available this round";

/**
 * QA escape hatch: forces the round open regardless of today's date, so the
 * round-scoped surfaces can be exercised before the window starts. Read from
 * the environment rather than by editing the dates above, because that leaves
 * nothing to remember to revert — EXPO_PUBLIC_* values are inlined at bundle
 * time, so a build made without this variable set (TestFlight, EAS production)
 * ships with it permanently false. Set it only in a local `mobile/.env`, which
 * is gitignored and never part of a release build.
 */
const QA_FORCE_TRACTION_WINDOW = process.env.EXPO_PUBLIC_QA_FORCE_TRACTION === "1";

/**
 * Is the Traction 3 round running right now? The single date check behind every
 * round-scoped surface: when it goes false the app returns to its normal
 * behaviour on its own, with no flag to remember to flip.
 */
export function isTractionWindowActive(now: Date = new Date()): boolean {
  if (QA_FORCE_TRACTION_WINDOW) return true;
  const t = now.getTime();
  return t >= Date.parse(TRACTION_3_CONFIG.startsAt) && t < Date.parse(TRACTION_3_CONFIG.endsAt);
}
