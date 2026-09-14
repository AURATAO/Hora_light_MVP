import { useCallback, useEffect, useState } from 'react'
import { loadStripe } from '@stripe/stripe-js'
import {
  confirmTaskPayment,
  FALLBACK_PUBLISHABLE_KEY,
  listPaymentMethods,
  publishableKeyFrom,
} from '../api/payments'

/**
 * "Can this requester post right now, and what do they need to do if not."
 *
 * The gate is advisory. POST /tasks answers 402 whatever this hook believes,
 * so nothing here is a security boundary — it exists so a requester learns
 * they need a card BEFORE filling in a whole form, and so that beta users see
 * nothing at all while PAYMENTS_ENFORCED is off.
 *
 * A backend with no Stripe configured answers 503; that is treated as "not
 * enforced", which is exactly right — such a backend cannot be enforcing.
 */
export function usePaymentGate() {
  const [state, setState] = useState({
    loading: true,
    enforced: false,
    hasCard: false,
    publishableKey: '',
  })

  const refresh = useCallback(async () => {
    try {
      const res = await listPaymentMethods()
      setState({
        loading: false,
        enforced: !!res?.payments_enforced,
        hasCard: !!res?.has_card,
        publishableKey: publishableKeyFrom(res),
      })
    } catch {
      setState({ loading: false, enforced: false, hasCard: false, publishableKey: '' })
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  return { ...state, refresh }
}

/**
 * The 3DS second half.
 *
 * An off-session hold the issuer wants the cardholder present for comes back
 * from POST /tasks as a 402 carrying the intent's client secret. The task
 * already exists, parked and invisible; this runs the bank's challenge and
 * then asks the backend to read the outcome.
 *
 * The backend re-reads the intent from Stripe rather than believing this call,
 * so a failure to reach it leaves the task unposted rather than posted unpaid.
 *
 * Returns nothing on success and throws with a showable `.message` otherwise.
 */
export async function completeCardAuthentication({ publishable_key, client_secret, task_id }) {
  const publishableKey = publishable_key || FALLBACK_PUBLISHABLE_KEY
  if (!publishableKey || !client_secret || !task_id) {
    throw new Error("We couldn't complete that payment. Please try posting again.")
  }
  const stripe = await loadStripe(publishableKey)
  if (!stripe) {
    throw new Error("We couldn't reach our payment provider. Please try again.")
  }

  const { error } = await stripe.handleNextAction({ clientSecret: client_secret })
  if (error) {
    // The bank refused, or the requester closed the challenge. Either way the
    // task stays unposted; the backend discards it on the confirm below.
    await confirmTaskPayment(task_id).catch(() => {})
    throw new Error(error.message || 'That payment was not approved. Try another card.')
  }

  await confirmTaskPayment(task_id)
}

/**
 * Reduce a failed POST /tasks into something to show, and what to do about it.
 *
 * `kind` is what the caller branches on:
 *   'authenticate' — run completeCardAuthentication with `payment`
 *   'card'         — send the requester to Profile to add or change a card
 *   'other'        — an ordinary error; just show the message
 */
export function readPaymentError(err) {
  const body = err?.body || {}
  if (err?.status !== 402) {
    return { kind: 'other', message: err?.message || 'Failed to create task' }
  }
  if (body.error === 'payment_authentication_required') {
    return { kind: 'authenticate', message: body.message || '', payment: body }
  }
  return {
    kind: 'card',
    message:
      body.message ||
      (body.error === 'payment_method_required'
        ? 'Add a card before posting a task.'
        : "We couldn't place a hold on your card. Try another card."),
  }
}
