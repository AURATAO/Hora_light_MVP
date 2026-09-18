// The beta notice's content, in one object, plus what remains of the Traction
// 3 round.
//
// TWO THINGS LIVE HERE AND THEY ARE NO LONGER THE SAME THING:
//
//   BETA_NOTICE_COPY — the welcome every user meets on their first authed page
//     load (components/BetaModal.jsx). It is now a PLAIN BETA WELCOME: what
//     HO:RA is, where it runs, what to expect, and what it costs. It describes
//     no round and no date window, so it does not expire and does not need
//     editing when a test round opens or closes.
//
//   TRACTION_3_CONFIG — the post-task questionnaire's own round window and the
//     short price string it quotes. Still date-gated by isTractionWindowActive,
//     still nothing to do with the notice.
//
// mobile/src/lib/beta-notice.ts is the same content for the mobile app. The
// two codebases don't share a package, so this is duplicated on purpose and
// guarded by traction.test.mjs, which reads the mobile file — EDIT BOTH.
//
// (This file used to open with "Traction 3 round configuration for the
// webapp." It is named for the round because the round is what it held first;
// the notice copy has since moved in and outlived it.)

/**
 * The price schedule, in the words a user reads.
 *
 * S-05 SAYS CLIENTS NEVER RE-DERIVE PRICE, AND THIS IS NOT THAT: nothing here
 * is computed, and no screen that quotes an actual number uses this string —
 * every real quote comes from POST /tasks/estimate and every settlement from
 * GET /tasks/:id/worklogs, both priced in Go. This is the marketing sentence
 * shown BEFORE a task exists to quote, and it is one string so that changing
 * the schedule is one edit per client rather than a hunt through page files.
 *
 * Kept in lockstep with server/billing.go's BillingConfig by eye, and with
 * mobile's copy of it by test:
 *
 *   BaseFeeDefaultCents       1200   $12
 *   IncludedMinutes             15   first 15 minutes
 *   PerMinuteRateCents          50   $0.50/min
 *   BaseFeeCompanionshipCents 2500   $25 companionship
 *   SurgeRateCentsPerMin       100   $1.00/min
 *   SurgeStartHour              21   from 9 PM
 */
export const PRICING_LINE =
  '$12 base fee includes the first 15 minutes, then $0.50/min ' +
  '($25 base for companionship; $1.00/min for tasks starting after 9 PM)'

/**
 * How purchases are settled — and WHICH of the two sentences below is true
 * depends on a server flag, never on a guess.
 *
 * `paymentsEnforced` is the `payments_enforced` field of GET
 * /payments/methods, which both clients already fetch for the "add a card
 * first" prompt (hooks/usePaymentGate.js). Pass `null`/`undefined` when it has
 * not loaded.
 *
 * FAIL-CLOSED, AND THIS IS THE WHOLE POINT OF THE FUNCTION. "Settle directly
 * with your supporter" is a sentence about taking payment OUTSIDE the app. It
 * is true and necessary while the platform charges nobody; it is false the
 * moment payments are on, and shown then it reads to an App Store reviewer as
 * a storefront steering users off Apple's rails. So the off-platform wording
 * is returned ONLY on a definite `false`. Unknown returns null — the caller
 * omits the line rather than guessing, because a missing sentence costs a beta
 * user one question and the wrong sentence costs a release.
 */
export function betaSettlementLine(paymentsEnforced) {
  if (paymentsEnforced === true) {
    return 'Payments and purchase reimbursements are handled securely in the app.'
  }
  if (paymentsEnforced === false) {
    return 'During beta, settle purchases directly with your supporter.'
  }
  return null
}

/** Service area, as users are told it. */
export const SERVICE_AREA = 'New York City'

// The beta notice, word for word. A welcome, not a round announcement: nothing
// below carries a date, a coverage window, a per-day task limit or a purchase
// cap, so it stays true between rounds and after them.
//
// The $30 purchase cap that used to be the last bullet is GONE, and not only
// from the copy: the backend stopped enforcing it when the whole budget began
// being reserved on the card at post (server/billing.go — the note on
// OverageToleranceCents), leaving the advisory HighBudgetWarningCents in its
// place. The clients were still promising a ceiling nothing enforced.
export const BETA_NOTICE_COPY = {
  heading: "You're in. Welcome to HO:RA Beta.",
  intro: [
    `HO:RA is a minute-billing platform for urban support — real people helping with real tasks in ${SERVICE_AREA}.`,
    'You post what you need, a nearby supporter accepts it, and you pay for the time they actually work.',
  ],
  points: [
    PRICING_LINE,
    "You're charged for time on the clock only — a supporter who steps away clocks out, and waiting time is free",
    'Every task is manually reviewed by the HO:RA team before a supporter is dispatched',
    'Reimbursement only: you cover actual item costs (e.g. a $5 coffee), nothing else',
    'This is an early-stage beta — things may change, and some tasks may not be fulfilled',
  ],
  finePrint:
    'By continuing, you agree to use this platform responsibly and understand this is an early-stage test.',
  acknowledgement:
    'I understand this is a beta — things may change and some tasks may not be fulfilled',
  cta: 'Enter HO:RA',
}

// ── Traction 3: the questionnaire's round, and nothing else ─────────────────

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
  /**
   * The SHORT price string, for the questionnaire's two would-you-use-it
   * questions, where the full PRICING_LINE above would bury the question it is
   * attached to. Byte-identical to mobile's copy by test.
   */
  pricingSummary: '$12 base (first 15 min included), then $0.50/min',
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
 * Is the Traction 3 round running right now? The single date check behind the
 * post-task questionnaire: when it goes false that surface returns to the
 * classic review on its own, with no flag to remember to flip.
 *
 * NOTHING IN THE BETA NOTICE READS THIS ANY MORE. The notice is a plain
 * welcome now — it has no window to be inside or outside of.
 */
export function isTractionWindowActive(now = new Date()) {
  if (QA_FORCE_TRACTION_WINDOW) return true
  const t = now.getTime()
  return t >= Date.parse(TRACTION_3_CONFIG.startsAt) && t < Date.parse(TRACTION_3_CONFIG.endsAt)
}
