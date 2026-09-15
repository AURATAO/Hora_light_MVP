import { formatCents } from './formatCents.js'
import { brandLabel } from './cardBrand.js'

/**
 * Every sentence the app says about a card hold, in one place.
 *
 * WHY THIS IS A MODULE AND NOT THREE INLINE STRINGS. The gap this closes was
 * not a bug in the money — the hold, the capture and the release were all
 * correct and verified. It was that an off-session pre-auth is SILENT: a live
 * tester posted a task, saw nothing about their card, concluded the post had
 * failed, and cancelled it. Three surfaces have to tell one consistent story
 * (post success, the open task, the cancel), and three copies of that story
 * drift the moment one of them is edited.
 *
 * TWO RULES, both learned from that incident:
 *
 * 1. ALWAYS A REAL NUMBER. Never "your card may be charged" — vagueness about
 *    somebody's money is what made them cancel. If the amount is unknown these
 *    functions return null and the caller renders nothing at all, which is
 *    honest; they never fall back to a hedge or to $0.00.
 *
 * 2. NO ARITHMETIC HERE. Every figure is computed in Go and rendered verbatim
 *    (S-05). "Charged $X, released $Y" uses the server's own captured_cents
 *    and released_cents rather than subtracting one from the other — a client
 *    that does its own subtraction can disagree with what Stripe actually did.
 */

/**
 * "Visa ••4242", or an empty string when the card is unknown — which is the
 * case for a hold placed before the card was recorded, and for any
 * authorization Stripe returned no charge detail for. Callers drop the clause
 * rather than writing "your card (unknown)".
 */
export function formatCardLabel(payment) {
  const last4 = payment?.card_last4
  if (!last4) return ''
  // Cardholders read "Visa ••4242" on every other surface in their life, so
  // that is what this says — and it says it with the SAME brand map the saved
  // cards list uses.
  return `${brandLabel(payment?.card_brand)} ••${last4}`
}

/**
 * The post-success confirmation, as two lines.
 *
 *   primary    "$49.50 reserved — $19.50 time + $30.00 budget"
 *   secondary  "Charged only for what's used. Rest released automatically."
 *
 * Short on purpose. The long version of this sentence was a paragraph, and a
 * paragraph on a success screen is a paragraph nobody reads — which defeats
 * the whole point, since the reason this exists is that a requester saw
 * nothing and assumed the post had failed.
 *
 * No card here. Which card it landed on matters when you are looking at a live
 * task and wondering what is held; at the moment of posting, the number is the
 * message. The card stays on the task-detail line, where it belongs.
 *
 * Null when there is no hold — every task posted with PAYMENTS_ENFORCED off —
 * and the caller then says nothing about money at all.
 */
export function holdPlacedMessage(payment) {
  if (!payment || !payment.authorized_cents) return null
  return {
    primary: holdBreakdown(payment),
    secondary: "Charged only for what's used. Rest released automatically.",
  }
}

/**
 * "$49.50 reserved — $19.50 time + $30.00 budget", or just "$19.50 reserved"
 * when there is no shopping.
 *
 * The split comes from the server and is present only when it reconciles with
 * the total; nothing is added up here (S-05). A task with no budget gets the
 * single number rather than "— $19.50 time + $0.00 budget", which would be
 * noise pretending to be detail.
 */
function holdBreakdown(payment) {
  const total = formatCents(payment.authorized_cents)
  const budget = payment.shopping_budget_cents || 0
  const time = payment.time_cost_cents || 0
  if (budget > 0 && time > 0) {
    return `${total} reserved — ${formatCents(time)} time + ${formatCents(budget)} budget`
  }
  return `${total} reserved`
}

/**
 * Why a task is being quoted more than usual. Null unless the evening rate
 * applies; the rate and the included block both come from the server quote, so
 * this names no number of its own.
 */
export function surgeRateNote(quote) {
  if (!quote?.surge_rate) return null
  return `Evening rate: ${formatCents(quote.per_minute_rate_cents)}/min after the first ${quote.included_minutes} minutes.`
}

/**
 * The red notice on the post form when a lot of money is about to be reserved.
 * The threshold comes from the server quote — both clients must warn at the
 * same number, and neither should carry a copy of it.
 */
export function highBudgetWarning(budgetCents, quote) {
  const threshold = quote?.high_budget_warning_cents
  if (!threshold || !budgetCents || budgetCents < threshold) return null
  return `High budget — this full amount will be reserved on your card.`
}

/**
 * The persistent banner when a completion could not be charged.
 *
 * Names the amount and the task it came from, because "you have an outstanding
 * balance" with no number is the same vagueness that caused the original
 * incident. Null when nothing is owed, which is almost always.
 */
export function outstandingBalanceMessage(outstanding) {
  if (!outstanding?.total_cents) return null
  const from = outstanding.task_title ? ` from “${outstanding.task_title}”` : ''
  const more =
    outstanding.task_count > 1 ? ` and ${outstanding.task_count - 1} more` : ''
  return `You have an outstanding balance of ${formatCents(outstanding.total_cents)}${from}${more} — settle it to keep posting.`
}

/** The short form for a task-detail row: "$76.75 reserved · Visa ••4242". */
export function holdSummary(payment) {
  if (!payment || !payment.authorized_cents) return null
  const card = formatCardLabel(payment)
  return card
    ? `${formatCents(payment.authorized_cents)} reserved · ${card}`
    : `${formatCents(payment.authorized_cents)} reserved`
}

/**
 * The warning inside the cancel dialog, BEFORE anything happens. Reads off the
 * task's payment block, so it names the hold that is actually standing.
 */
export function holdWillBeReleasedMessage(payment) {
  if (!payment || !payment.authorized_cents) return null
  return `Your reserved ${formatCents(payment.authorized_cents)} will be released immediately.`
}

// Banks post an authorization reversal on their own schedule and the requester
// will keep seeing the hold on their statement until they do. Saying so up
// front is the difference between "released" and a support email three days
// later asking why the money is still showing.
const STATEMENT_NOTE =
  'Depending on your bank, it may take 1–7 days to disappear from your statement.'

/**
 * What the requester is told after a cancel succeeds, from the cancel
 * response. Three shapes, because three things can have happened:
 *
 *   nothing held      → null; the caller says nothing about money
 *   held, nothing     → the whole hold went back
 *   taken             → charged this much, released the rest
 */
export function holdReleasedMessage(result) {
  const released = result?.released_cents || 0
  const captured = result?.captured_cents || 0
  if (!released && !captured) return null

  if (captured > 0) {
    const tail = released > 0
      ? ` the remaining ${formatCents(released)} hold has been released.`
      : ' the rest of the hold has been released.'
    return `Charged ${formatCents(captured)} for completed time;${tail} ${STATEMENT_NOTE}`
  }
  return `Reserved ${formatCents(released)} released. ${STATEMENT_NOTE}`
}
