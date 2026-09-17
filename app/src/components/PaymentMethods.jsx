import { useCallback, useEffect, useState } from 'react'
import { loadStripe } from '@stripe/stripe-js'
import { Elements, PaymentElement, useElements, useStripe } from '@stripe/react-stripe-js'
import { CreditCard, Plus, Trash2 } from 'lucide-react'
import {
  brandLabel,
  createSetupIntent,
  deletePaymentMethod,
  formatExpiry,
  isExpired,
  listPaymentMethods,
  publishableKeyFrom,
} from '../api/payments'
import { useToast } from '../providers/ToastProvider'

/**
 * Profile → Payment methods. List, add, remove.
 *
 * The card form is Stripe's own <PaymentElement>, rendered in an iframe served
 * from Stripe's origin: the number never touches this app, which is what keeps
 * the whole web client out of PCI scope and gets 3DS handling for free.
 *
 * loadStripe is called with the publishable key the BACKEND sends, not a
 * VITE_ variable, so rotating keys does not need a web deploy. It is memoised
 * per key because loadStripe injects a script tag — calling it on every render
 * would inject one per render.
 */

const stripePromises = new Map()
function stripeFor(publishableKey) {
  if (!stripePromises.has(publishableKey)) {
    stripePromises.set(publishableKey, loadStripe(publishableKey))
  }
  return stripePromises.get(publishableKey)
}

// Stripe's own dark theme, nudged to this app's surface colours so the iframe
// doesn't read as a white rectangle pasted into a dark card.
const ELEMENTS_APPEARANCE = {
  theme: 'night',
  variables: {
    colorBackground: '#2D343F',
    colorText: '#FFFFFF',
    colorDanger: '#F87171',
    borderRadius: '8px',
  },
}

function CardRow({ card, onRemove, removing }) {
  const expired = isExpired(card.exp_month, card.exp_year)
  return (
    <div className="flex items-center justify-between gap-3 rounded-lg border border-white/10 bg-white/5 px-4 py-3">
      <div className="flex items-center gap-3 min-w-0">
        <CreditCard className="h-5 w-5 shrink-0 opacity-60" aria-hidden="true" />
        <div className="min-w-0">
          <div className="text-sm text-white truncate">
            {brandLabel(card.brand)} ···· {card.last4}
          </div>
          <div className={`text-xs ${expired ? 'text-red-400' : 'text-white/40'}`}>
            {expired ? 'Expired ' : 'Expires '}
            {formatExpiry(card.exp_month, card.exp_year)}
            {card.is_default && !expired ? ' · used for new tasks' : ''}
          </div>
        </div>
      </div>
      <button
        type="button"
        onClick={() => onRemove(card)}
        disabled={removing}
        className="shrink-0 rounded-md p-2 text-white/40 hover:text-red-400 hover:bg-white/5 transition-colors disabled:opacity-40"
        aria-label={`Remove ${brandLabel(card.brand)} ending ${card.last4}`}
      >
        <Trash2 className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  )
}

/**
 * The add-card form. Lives inside <Elements> so it can reach the Stripe
 * instance; confirmSetup does the whole exchange, 3DS included, without the
 * card ever leaving Stripe's iframe.
 */
function AddCardForm({ onSaved, onCancel }) {
  const stripe = useStripe()
  const elements = useElements()
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  async function handleSubmit(e) {
    e.preventDefault()
    if (!stripe || !elements) return
    setSubmitting(true)
    setError('')

    // redirect: 'if_required' keeps the common card case in-page. A payment
    // method that genuinely needs a redirect would need return_url; cards
    // with 3DS are handled in a modal without leaving the page.
    const { error: stripeError } = await stripe.confirmSetup({
      elements,
      redirect: 'if_required',
    })

    if (stripeError) {
      // Stripe's message is the right one here: it is written for the
      // cardholder and names the specific field that was wrong.
      setError(stripeError.message || "That card couldn't be saved. Try another.")
      setSubmitting(false)
      return
    }
    setSubmitting(false)
    onSaved()
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      {/* 'tabs', not 'tab' — the union is 'tabs' | 'accordion' | 'auto' and
          Stripe throws an IntegrationError at elements.create() on anything
          else. Nothing type-checks this file (plain JSX, Vite), so the literal
          has to be right by inspection.

          WALLETS. 'auto' is already the default for both; they are written out
          because the interesting fact about them is invisible otherwise — the
          switch that actually decides whether an Apple Pay button appears is
          NOT in this file. It is domain registration in the Stripe dashboard
          (Payment method domains → add mvp.horaapp.co). Until that is done
          these two lines do nothing at all, and someone hunting for "why is
          there no Apple Pay button" should find that sentence here rather than
          conclude the option is missing and add it a second time.

          Stripe performs the Apple merchant validation for web itself — there
          is no merchant ID or certificate to configure on this side, and the
          merchant.co.horaapp.hora identifier is the NATIVE app's alone.

          The same off-session caveat as mobile applies: this form saves a
          method that is charged later without the customer present. See the
          WALLETS AND OFF-SESSION note in mobile/src/lib/payments.ts — the
          reasoning is identical and is not repeated here. */}
      <PaymentElement
        options={{
          layout: 'tabs',
          wallets: { applePay: 'auto', googlePay: 'auto' },
        }}
      />
      {error && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {error}
        </div>
      )}
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={!stripe || submitting}
          className="flex-1 rounded-lg bg-[#9aab3a] px-4 py-2.5 text-sm font-medium text-[#1B2027] disabled:opacity-50"
        >
          {submitting ? 'Saving…' : 'Save card'}
        </button>
        <button
          type="button"
          onClick={onCancel}
          disabled={submitting}
          className="rounded-lg border border-white/20 px-4 py-2.5 text-sm text-white/70 hover:text-white disabled:opacity-50"
        >
          Cancel
        </button>
      </div>
    </form>
  )
}

export default function PaymentMethods() {
  const toast = useToast()
  const [cards, setCards] = useState([])
  const [loading, setLoading] = useState(true)
  // Set when this backend has no payments surface — either Stripe is not
  // configured (503) or the routes are not deployed yet (404). The whole
  // section hides rather than showing an error a user cannot act on.
  const [unavailable, setUnavailable] = useState(false)
  const [removingId, setRemovingId] = useState('')
  const [setup, setSetup] = useState(null) // { clientSecret, publishableKey }
  const [starting, setStarting] = useState(false)

  const load = useCallback(async () => {
    try {
      const res = await listPaymentMethods()
      setCards(res?.cards || [])
      setUnavailable(false)
    } catch (e) {
      // 404 as well as 503, and deliberately: web and the Go backend deploy
      // separately (Render's auto-deploy is manual), so there is always a
      // window where this build is live against a backend that has no
      // /payments routes. Hiding the section is the correct read of "this
      // backend does not do payments" — an error toast on the settings page
      // for a staged deploy is noise nobody can act on.
      if (e.status === 503 || e.status === 404) setUnavailable(true)
      else if (e.status !== 401) toast("Couldn't load your payment methods")
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    load()
  }, [load])

  async function startAddCard() {
    setStarting(true)
    try {
      const res = await createSetupIntent()
      const publishableKey = publishableKeyFrom(res)
      if (!publishableKey) {
        // The backend has a secret key but no publishable one. Saying so
        // plainly beats a blank Stripe iframe and a support ticket.
        toast('Card payments are not fully configured yet — try again later')
        return
      }
      setSetup({ clientSecret: res.client_secret, publishableKey })
    } catch (e) {
      if (e.status !== 401) toast("Couldn't start adding a card")
    } finally {
      setStarting(false)
    }
  }

  async function handleRemove(card) {
    if (!window.confirm(`Remove your ${brandLabel(card.brand)} ending ${card.last4}?`)) return
    setRemovingId(card.id)
    try {
      await deletePaymentMethod(card.id)
      setCards(prev => prev.filter(c => c.id !== card.id))
    } catch (e) {
      // 409 means the card is holding funds for a task in flight; the backend
      // explains which, and that message is more useful than a generic one.
      toast(e?.body?.message || "Couldn't remove that card")
    } finally {
      setRemovingId('')
    }
  }

  if (unavailable) return null

  return (
    <div className="rounded-2xl border border-white/10 bg-[#2D343F] p-6 space-y-4">
      <div>
        <h2 className="font-heading text-lg text-white">Payment methods</h2>
        <p className="mt-1 text-xs text-white/40">
          Posting a task places a hold on your card. You're only charged for the time actually worked.
        </p>
      </div>

      {loading ? (
        <div className="py-4 text-center text-sm text-white/40">Loading…</div>
      ) : (
        <div className="space-y-2">
          {cards.map(card => (
            <CardRow
              key={card.id}
              card={card}
              onRemove={handleRemove}
              removing={removingId === card.id}
            />
          ))}
          {cards.length === 0 && !setup && (
            <p className="py-2 text-sm text-white/40">No card saved yet.</p>
          )}
        </div>
      )}

      {setup ? (
        <Elements
          stripe={stripeFor(setup.publishableKey)}
          options={{ clientSecret: setup.clientSecret, appearance: ELEMENTS_APPEARANCE }}
        >
          <AddCardForm
            onSaved={() => {
              setSetup(null)
              // Stripe attaches the PaymentMethod to the customer server-side
              // as part of confirmSetup, so re-reading the list is the only
              // way to learn what was actually saved — and it confirms the
              // card is really there rather than trusting the client.
              load()
            }}
            onCancel={() => setSetup(null)}
          />
        </Elements>
      ) : (
        !loading && (
          <button
            type="button"
            onClick={startAddCard}
            disabled={starting}
            className="flex w-full items-center justify-center gap-2 rounded-lg border border-white/20 px-4 py-2.5 text-sm text-white/80 hover:border-white/40 hover:text-white transition-colors disabled:opacity-50"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            {starting ? 'Opening…' : cards.length ? 'Add another card' : 'Add a card'}
          </button>
        )
      )}
    </div>
  )
}
