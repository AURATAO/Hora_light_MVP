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

// Re-exported from its own leaf module so the hold confirmations can import it
// without pulling the API client in. Same map, one definition.
export { brandLabel } from '../lib/cardBrand.js'

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

// ── Phase 3: getting paid ──────────────────────────────────────────────────
//
// The supporter's half of the money. Same rule as everything above: these all
// go through the Go backend, and nothing here ever touches a bank account or
// an identity document — those are collected by Stripe, on Stripe's own
// domain, through a URL the backend mints. This app never sees either.

/**
 * Start (or resume) payout onboarding.
 *
 * Returns `{ url }` — a SINGLE-USE Stripe-hosted link that grants access to
 * the supporter's own personal information. Navigate to it immediately; never
 * store it, share it, or put it in a link someone could come back to.
 */
export function createOnboardingLink() {
  return api('/payments/connect/onboarding-link', { method: 'POST' })
}

/** Where onboarding has got to: state, payouts_enabled, requirements_due. */
export function getConnectStatus() {
  return api('/payments/connect/status')
}

/**
 * A one-time URL into the Stripe Express dashboard, where the supporter sees
 * their own balance, payout schedule and bank account. Refused (404) for
 * somebody who has not onboarded.
 */
export function createLoginLink() {
  return api('/payments/connect/login-link', { method: 'POST' })
}

/** Onboarding state + lifetime earned + recent transfers, in one call. */
/**
 * `limit`/`offset` page the transfer list. Omitted, the server answers with
 * its default page and the total — which is what the earnings strip needs to
 * render three rows and say how many more there are.
 */
export function getEarnings({ limit, offset } = {}) {
  const params = new URLSearchParams()
  if (limit !== undefined) params.set('limit', String(limit))
  if (offset !== undefined) params.set('offset', String(offset))
  const query = params.toString()
  return api(`/payments/earnings${query ? `?${query}` : ''}`)
}

/**
 * Whether a failed accept was the payouts gate rather than a real error.
 *
 * The backend answers 403 `payouts_onboarding_required` when PAYMENTS_ENFORCED
 * is on and the supporter has not finished onboarding. It is NOT a 402: nothing
 * is owed and no payment is required — they are simply not set up to receive
 * one, and the only useful response is the onboarding CTA.
 */
export function isPayoutsOnboardingRequired(e) {
  return e?.status === 403 && e?.body?.error === 'payouts_onboarding_required'
}

/** Copy for each onboarding state. One place, so both the Earnings card and
 *  the accept-gate prompt say the same thing about the same state. */
export const ONBOARDING_COPY = {
  not_started: {
    title: 'Set up payouts to start earning',
    body: 'Add your bank details through Stripe. It takes a couple of minutes and you only do it once.',
    cta: 'Set up payouts',
  },
  in_progress: {
    title: 'Finish setting up payouts',
    body: 'Stripe still needs a few details before we can pay you.',
    cta: 'Continue setup',
  },
  // Form in, nothing due, Stripe not yet done making the account
  // transferable. No setup left; the only action is to check again.
  verifying: {
    title: 'Verification in progress',
    body: 'You can accept tasks once Stripe finishes.',
    cta: 'Check again',
  },
  complete: {
    title: 'Payouts are set up',
    body: 'Payments land in your bank automatically.',
    cta: 'Manage payouts',
  },
}
