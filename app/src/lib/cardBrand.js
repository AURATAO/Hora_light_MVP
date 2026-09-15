/**
 * Brand slugs as Stripe spells them → what a person calls the card.
 *
 * A leaf module with no imports, for the same reason formatCents.js is one:
 * the payment copy is pinned by a plain `node --test` file, and importing this
 * through src/api/payments.js would drag the API client and its extensionless
 * Vite-only imports along with it.
 *
 * ONE definition. It previously lived in src/api/payments.js, which re-exports
 * it — the saved-cards list and the hold confirmations must call the same card
 * by the same name, and two maps would have drifted the first time one of them
 * gained a brand.
 */
const BRAND_LABELS = {
  visa: 'Visa',
  mastercard: 'Mastercard',
  amex: 'American Express',
  discover: 'Discover',
  diners: 'Diners Club',
  jcb: 'JCB',
  unionpay: 'UnionPay',
}

/** An unrecognised brand degrades to "Card" — the last four are the half a
 *  cardholder actually recognises, and a raw Stripe slug is not copy. */
export function brandLabel(brand) {
  return BRAND_LABELS[brand] || 'Card'
}
