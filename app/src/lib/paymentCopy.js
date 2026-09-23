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
  if (!payment) return null
  // A promo that covered the whole hold reserves $0.00 — and that IS the
  // message, said with the discount that made it so, not silence.
  if (!payment.authorized_cents && !(payment.promo_discount_cents || 0)) return null
  return {
    primary: holdBreakdown(payment),
    secondary: "Charged only for what's used. Rest released automatically.",
  }
}

/**
 * The split comes from the server and is present only when it reconciles with
 * the total; nothing is added up here (S-05).
 *
 *   promo             "$19.50 − $10.00 promo = $9.50 reserved"
 *   base on its own   "$32.50 reserved — $25.00 base + $7.50 time [+ $30.00 budget]"
 *                     (how a companionship task shows its $25 base)
 *   older backend     "$49.50 reserved — $19.50 time + $30.00 budget"
 *   no split          "$19.50 reserved"
 *
 * No budget means no budget clause rather than "+ $0.00 budget", which is
 * noise pretending to be detail; same for a task inside its included minutes.
 */
function holdBreakdown(payment) {
  const total = formatCents(payment.authorized_cents)
  const promo = payment.promo_discount_cents || 0
  const pre = payment.pre_discount_cents || 0
  if (promo > 0 && pre > 0) {
    return `${formatCents(pre)} − ${formatCents(promo)} promo = ${total} reserved`
  }
  const budget = payment.shopping_budget_cents || 0
  const base = payment.base_fee_cents || 0
  const minutes = payment.minutes_cost_cents || 0
  if (base > 0) {
    const parts = [`${formatCents(base)} base`]
    if (minutes > 0) parts.push(`${formatCents(minutes)} time`)
    if (budget > 0) parts.push(`${formatCents(budget)} budget`)
    return `${total} reserved — ${parts.join(' + ')}`
  }
  const time = payment.time_cost_cents || 0
  if (budget > 0 && time > 0) {
    return `${total} reserved — ${formatCents(time)} time + ${formatCents(budget)} budget`
  }
  return `${total} reserved`
}

/**
 * The post form's promo line, from the server quote: what the estimate was,
 * what the code takes off, and what will actually be reserved. Null unless a
 * discount applies.
 */
export function promoReservedLine(quote) {
  const discount = quote?.promo_discount_cents || 0
  if (!quote || discount <= 0 || quote.total_cents === undefined || quote.hold_cents === undefined) {
    return null
  }
  return `${formatCents(quote.total_cents)} − ${formatCents(discount)} promo = ${formatCents(quote.hold_cents)} reserved`
}

/**
 * On the live task's hold card: "Includes a $10.00 promo (WELCOME10)." Null
 * when the task was posted without one.
 */
export function promoHoldNote(payment) {
  const discount = payment?.promo_discount_cents || 0
  if (!discount) return null
  const code = payment?.promo_code ? ` (${payment.promo_code})` : ''
  return `Includes a ${formatCents(discount)} promo${code}.`
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

/**
 * The time a task is priced against, as one coherent line.
 *
 *   "Estimate: 30 min · up to 45 min with auto-extend"
 *
 * WHY THIS EXISTS. The supporter's card said "Paid time 41 of 45 min" while
 * the requester's said "Estimated cost (based on 30 min)" — two numbers,
 * neither explaining the other, on a screen where one of them is deciding
 * whether to keep working. 45 is not a second estimate; it is the estimate
 * plus what the requester already agreed to on top of it.
 *
 * Both roles render this, so neither has to infer the relationship. Null when
 * there is no ceiling to describe.
 */
export function timeBasisNote(cap) {
  const estimate = cap?.estimate_minutes || 0
  const ceiling = cap?.cap_minutes || 0
  if (!estimate || !ceiling) return null

  const base = `Estimate: ${estimate} min`
  if (ceiling <= estimate) return base

  // Why the ceiling is higher than the estimate — the requester consented to
  // auto-extend at post, approved an extension mid-task, or both.
  const autoExtend = (cap.auto_extend_minutes || 0) > 0
  const approved = (cap.approved_extra_minutes || 0) > 0
  const because = approved
    ? autoExtend
      ? 'with auto-extend and approved extensions'
      : 'with approved extensions'
    : 'with auto-extend'
  return `${base} · up to ${ceiling} min ${because}`
}

/**
 * The supporter-and-requester line for Layer 2, the early warning.
 *
 * WHY IT IS NOT JUST "about N minutes left". The warning now fires at the
 * ESTIMATE rather than at the ceiling (server/billing.go
 * timeCapWarningMinutes), so on a 30-minute task with auto-extend it lands at
 * 25 logged minutes with 20 minutes of ceiling still above it. "About 20 min
 * left" is true of the ceiling and useless as a warning: what the supporter
 * needs is that they are at the number the requester planned around, and that
 * the 15 minutes above it are a fuse rather than a second estimate.
 *
 * Null when there is nothing to warn about. `reached` is not this function's
 * business — billing has stopped by then and both clients say so in their own
 * words.
 */
export function capWarningNote(capState) {
  if (!capState || capState.reached || !capState.warning) return null
  const agreed = capState.cap?.agreed_minutes || capState.cap?.estimate_minutes || 0
  const autoExtend = capState.cap?.auto_extend_minutes || 0
  if (agreed > 0 && autoExtend > 0) {
    return `Approaching the ${agreed} min agreed — up to ${autoExtend} more minutes are covered by auto-extend.`
  }
  const remaining = capState.remaining_minutes || 0
  return `About ${remaining} min left on the time that was agreed.`
}

/**
 * "You'll be charged $12.00 (base fee); …" — what cancelling right now costs,
 * in the server's own numbers.
 *
 * ALL THREE FIGURES COME OFF THE `cancellation` BLOCK (S-05). The dialog this
 * feeds used to describe the RULE instead ("if work has already been recorded,
 * you are billed for that time only"), which is a sentence a requester has to
 * apply to their own situation while deciding whether to spend money.
 *
 * `withinGrace` is passed in rather than read off the block because the dialog
 * can sit open across the boundary: the block says what was true when it was
 * fetched, and the live countdown says what is true now. A requester who reads
 * "free" and taps twenty seconds later must not be surprised by a charge.
 *
 * Null when nobody has committed — cancelling an unaccepted task has never
 * cost anything and never will.
 */
export function cancelChargeLine(cancellation, { withinGrace } = {}) {
  if (!cancellation?.committed) return null
  if (withinGrace ?? cancellation.within_grace) {
    return 'Your supporter has committed, but you\u2019re still inside the free window — cancelling now costs nothing.'
  }
  const base = formatCents(cancellation.base_fee_cents || 0)
  const minutes = cancellation.billed_minutes || 0
  if (cancellation.time_cost_cents > 0) {
    return `Your supporter has committed. You\u2019ll be charged ${formatCents(cancellation.charge_cents)} — ${base} base fee plus ${formatCents(cancellation.time_cost_cents)} for the ${minutes} min worked.`
  }
  return `Your supporter has committed. You\u2019ll be charged ${base} (base fee).`
}

/**
 * "$64.75 releases." — the rest of the hold, named before they commit.
 *
 * Falls back to the task's own payment block when the cancellation block is
 * missing, so a requester on a task that could not be priced still learns what
 * is reserved. Null when there is no hold at all, which is every task posted
 * with PAYMENTS_ENFORCED off — the dialog then says nothing about a release
 * rather than "$0.00 releases".
 */
export function cancelReleaseLine(cancellation, payment, { withinGrace } = {}) {
  const authorized = payment?.authorized_cents || 0
  if (!authorized) return null
  if (!cancellation?.committed || (withinGrace ?? cancellation.within_grace)) {
    return `Your reserved ${formatCents(authorized)} will be released immediately.`
  }
  const release = cancellation.release_cents || 0
  if (release <= 0) return null
  return `${formatCents(release)} of your reserved ${formatCents(authorized)} releases immediately.`
}

/**
 * "1:23" — the free window, counted down against the SERVER's deadline.
 *
 * A deadline rather than a duration, so the clock drifts by however long one
 * request took instead of by however long the dialog has been open. Null once
 * it has run out, which is what flips the dialog back to quoting a charge.
 */
export function cancelGraceCountdown(graceEndsAt, nowMs = Date.now()) {
  if (!graceEndsAt) return null
  const left = Date.parse(graceEndsAt) - nowMs
  if (!Number.isFinite(left) || left <= 0) return null
  const total = Math.ceil(left / 1000)
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}


/**
 * "$8.00 more budget" / "+30 min" — what a mid-task request asked for.
 *
 * Reads the server's own fields and formats nothing it was not given. The
 * amount is the ADDITIONAL sum, which is what both parties were asked to
 * approve; the running total is the settlement's job, two lines up.
 */
export function extensionAskLabel(request) {
  if (!request) return ''
  if (request.kind === 'budget') {
    return `${formatCents(request.requested_cents || 0)} more budget`
  }
  return `+${request.requested_minutes || 0} min`
}

/**
 * When it was settled, or when it was asked if it never was.
 *
 * A request that outlived its task has no resolved_at, and dating the row by
 * the ask is the only honest thing left to show.
 */
export function extensionDecidedAt(request) {
  const when = request?.resolved_at || request?.requested_at
  if (!when) return ''
  const parsed = Date.parse(when)
  if (!Number.isFinite(parsed)) return ''
  return new Date(parsed).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/**
 * The RESOLUTION, as the thing that leads the supporter's card.
 *
 * WHY THIS IS A HIERARCHY PROBLEM AND NOT A COPY ONE. After a deny or an
 * auto-deny the card re-surfaced the ask options — more budget, more time — at
 * the same visual weight as the instruction the supporter was supposed to
 * follow. Three equal-looking things, one of which was the answer and two of
 * which were "ask again", which read as the system nudging them to re-ask
 * somebody who had just said no (build 11).
 *
 * Asking again stays allowed, by design: per-kind pending re-opens the moment
 * a request resolves, and a supporter who genuinely needs more should not have
 * to argue with the UI. It is the WEIGHTING that was wrong. So the resolution
 * is the headline, and the re-ask is a subdued secondary below it.
 *
 * Two lines, so the clients can style them apart: the verdict, and what to do
 * about it.
 */
export function extensionResolutionTitle(request) {
  if (!request) return null
  if (request.status === 'expired') return 'No response'
  if (request.status === 'denied') return 'Denied'
  return null
}

/**
 * What to actually do now, which is the half that matters.
 *
 * For a BUDGET request that is the supporter's own pre-chosen fallback, read
 * back to them in their own words — they picked it while they still had the
 * context, precisely so that nobody has to decide anything under time
 * pressure.
 *
 * For a TIME request there is no fallback field and there never was one,
 * because the fallback is the billing: time past the ceiling is not charged,
 * and the supporter keeps working or wraps up as they judge safest. That is a
 * real answer and it belongs here rather than being left blank.
 */
export function extensionResolutionDetail(request) {
  if (!extensionResolutionTitle(request)) return null
  if (request.fallback_instruction) {
    return request.status === 'expired'
      ? `Proceed with your fallback: ${request.fallback_instruction}`
      : request.fallback_instruction
  }
  return "Time past the agreed cap isn\u2019t billed. Wrap up whenever you judge it right \u2014 you can still complete the task at any point."
}

/** "Ask for more budget again" / "Ask for more time again" — the subdued
 *  re-ask, labelled for the kind that was just refused so it is obviously the
 *  same request rather than a new idea. */
export function extensionReaskLabel(request) {
  return request?.kind === 'time' ? 'Ask for more time again' : 'Ask for more budget again'
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
