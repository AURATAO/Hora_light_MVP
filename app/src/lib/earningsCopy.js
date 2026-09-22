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
