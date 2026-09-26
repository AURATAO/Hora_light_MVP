/**
 * Earnings copy — the supporter's side of the money, in words.
 *
 * Two rules, same as paymentCopy.js and for the same reason (a tester who
 * was told nothing assumed the worst):
 *
 *   1. A real number, always. "$0.00 paid out" is a fact; "isn't counted
 *      here" is a hedge, and hedges read as bad news.
 *   2. One word per row, and "paid" is reserved for money in the BANK. A
 *      transfer Stripe has accepted but not yet swept is "on its way"; a
 *      failed one says so, with the sentence that matters.
 */
import { formatCents } from './formatCents.js'

/** Under "Earned all time": where the money is, as two numbers. Empty when
 *  nothing has been earned — there is no split of zero worth printing. */
export function earningsWhereaboutsLine({ lifetime_earned_cents, in_transit_cents, paid_out_cents }) {
  if (!lifetime_earned_cents) return ''
  return `${formatCents(in_transit_cents || 0)} on its way to your bank · ${formatCents(paid_out_cents || 0)} paid out`
}

export const TRANSFER_STATUS_COPY = {
  paid: { label: 'Paid', note: '' },
  on_its_way: { label: 'On its way', note: '' },
  failed: { label: 'Failed', note: "We're on it — you won't lose this payment." },
}

/** The row's status line. Reads display_status (the backend's word); an
 *  older backend without it degrades to the row status, where "paid" cannot
 *  distinguish bank from balance, so it is shown as on its way. */
export function transferStatusCopy(transfer) {
  const key = transfer?.display_status
    || (transfer?.status === 'failed' ? 'failed' : 'on_its_way')
  return TRANSFER_STATUS_COPY[key] || TRANSFER_STATUS_COPY.on_its_way
}

/** "20%" for 2000 basis points, "2.5%" for 250 — never a rounded rate. */
export function platformFeePercent(bps) {
  const n = Number(bps) || 0
  if (n % 100 === 0) return `${n / 100}%`
  return `${(n / 100).toFixed(2).replace(/\.?0+$/, '')}%`
}

/**
 * The one-line explainer under Earnings. Reads the rate the backend sends so
 * the sentence and the arithmetic cannot disagree (S-05); an older backend
 * without it gets the shipped rate.
 */
export function platformFeeExplainer(bps = 2000) {
  return `HO:RA takes ${platformFeePercent(bps)} of service fees; purchase reimbursements are always paid back in full.`
}

/**
 * What a payment was made of, as a supporter reads it — the settlement's
 * `earned` block and an Earnings transfer row use the same words:
 *
 *   $19.60 service (after 20% platform fee) + $12.40 reimbursement
 *   $19.60 service (after 20% platform fee)
 *   $27.00 service                              ← a transfer sent before the fee
 *   $12.40 reimbursement
 *
 * `time` is the service figure AFTER the fee (the backend's time_cents); the
 * fee clause appears only when a fee was actually taken, so a pre-fee row is
 * never described as discounted. Never a silent deduction.
 */
export function earningsBreakdownLine({ time, reimbursement, feeCents, feeBps }) {
  const parts = []
  if (time > 0 || feeCents > 0) {
    const fee = feeCents > 0 ? ` (after ${platformFeePercent(feeBps || 2000)} platform fee)` : ''
    parts.push(`${formatCents(time || 0)} service${fee}`)
  }
  if (reimbursement > 0) parts.push(`${formatCents(reimbursement)} reimbursement`)
  return parts.join(' + ')
}

/** The settlement card's `earned` block. */
export function earnedBreakdownLine(earned) {
  if (!earned) return ''
  return earningsBreakdownLine({
    time: earned.time_cents,
    reimbursement: earned.reimbursement_cents,
    feeCents: earned.platform_fee_cents,
    feeBps: earned.platform_fee_bps,
  })
}

/** One Earnings transfer row. */
export function transferBreakdownLine(transfer) {
  if (!transfer) return ''
  return earningsBreakdownLine({
    time: transfer.time_cents,
    reimbursement: transfer.receipt_cents,
    feeCents: transfer.platform_fee_cents,
    feeBps: transfer.platform_fee_bps,
  })
}
