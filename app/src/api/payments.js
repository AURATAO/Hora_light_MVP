// The card-on-file API, web side.
//
// Four calls, all against the Go backend — there is no Supabase table here and
// there never will be (CLAUDE.md Rule 1). Card data itself never reaches this
// file: Stripe Elements collects it in an iframe on Stripe's own origin and
// hands back a confirmed SetupIntent, so the page never sees a PAN and the app
// stays out of PCI scope.

import { api } from './client'

/**
 * Fallback publishable key.
 *
 * The BACKEND is the source of truth — every payments response carries the key
 * it is actually configured with, so rotating it is one Render env change
 * rather than a web deploy. This is used only when talking to a backend that
 * has no STRIPE_PUBLISHABLE_KEY set.
 *
 * Publishable keys are public by design (S-12); the secret key lives only in
 * server/.env.
 */
export const FALLBACK_PUBLISHABLE_KEY = import.meta.env?.VITE_STRIPE_PUBLISHABLE_KEY || ''

/** The key to build a Stripe instance with, backend first. */
export function publishableKeyFrom(response) {
  return response?.publishable_key || FALLBACK_PUBLISHABLE_KEY
}

/**
 * Start saving a card. Returns the SetupIntent client secret plus the
 * publishable key, which the backend sends rather than each client carrying
 * its own copy — a key rotation is then one backend env change instead of a
 * web deploy AND a native rebuild.
 */
export function createSetupIntent() {
  return api('/payments/setup-intent', { method: 'POST' })
}

/** The saved cards, newest first. `has_card` answers the post-task gate. */
export function listPaymentMethods() {
  return api('/payments/payment-methods')
}

/** Remove one card. The backend refuses a card that is holding funds. */
export function deletePaymentMethod(id) {
  return api(`/payments/payment-methods/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

/**
 * Finish a post that a bank wanted the cardholder present for. Called after
 * stripe.handleNextAction resolves; the backend reads the intent's real status
 * from Stripe rather than believing this call, so a client that lies gets a
 * task that stays unposted.
 */
export function confirmTaskPayment(taskId) {
  return api(`/tasks/${encodeURIComponent(taskId)}/payment/confirm`, { method: 'POST' })
}

/** Brand slugs as Stripe spells them → what a person calls the card. */
const BRAND_LABELS = {
  visa: 'Visa',
  mastercard: 'Mastercard',
  amex: 'American Express',
  discover: 'Discover',
  diners: 'Diners Club',
  jcb: 'JCB',
  unionpay: 'UnionPay',
}

export function brandLabel(brand) {
  return BRAND_LABELS[brand] || 'Card'
}

/** "04 / 2029" — zero-padded, because "4 / 2029" doesn't read as an expiry. */
export function formatExpiry(month, year) {
  return `${String(month).padStart(2, '0')} / ${year}`
}

/**
 * True once the card is past its printed expiry. The month itself is still
 * valid — a card marked 04/29 works through the end of April 2029 — so the
 * comparison is against the first day of the FOLLOWING month.
 */
export function isExpired(month, year) {
  return new Date(year, month, 1) <= new Date()
}
