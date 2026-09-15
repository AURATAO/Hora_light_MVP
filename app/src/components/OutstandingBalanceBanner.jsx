import { useCallback, useEffect, useState } from 'react'
import { api } from '../api/client'
import { outstandingBalanceMessage } from '../lib/paymentCopy'
import { runCardChallenge } from '../hooks/usePaymentGate'
import { useToast } from '../providers/ToastProvider'

/**
 * The wall a requester hits when a completion could not be charged.
 *
 * WHY A PERSISTENT BANNER AND NOT A ONE-OFF NOTICE. The balance blocks posting
 * (POST /tasks answers 403), and a block whose reason was announced once, in a
 * toast, days ago, is indistinguishable from a broken app. It stays until the
 * money is settled.
 *
 * Renders NOTHING for everybody who owes nothing, which is very nearly
 * everybody. One GET on mount; no polling — a balance can only appear when a
 * task completes, which is not something that happens while you stare at a
 * screen, and the 403 on the next post is the backstop.
 *
 * Deliberately requester-only in effect: the endpoint answers per session, and
 * a supporter never has a balance because payouts are never gated on this. The
 * platform carries the float — see skills/payments-runbook.md.
 */
export default function OutstandingBalanceBanner() {
  const [outstanding, setOutstanding] = useState(null)
  const [settling, setSettling] = useState(false)
  const toast = useToast()

  const refresh = useCallback(async () => {
    try {
      const res = await api('/payments/outstanding-balance')
      setOutstanding(res?.outstanding ?? null)
    } catch {
      // Payments unconfigured (503), signed out, offline. Silence is right:
      // this banner is an exception state, and failing to confirm somebody is
      // fine must not itself become a message.
      setOutstanding(null)
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  async function settle() {
    setSettling(true)
    try {
      await api('/payments/settle-balance', { method: 'POST' })
      toast('Balance settled — thank you.')
      await refresh()
    } catch (e) {
      const body = e?.body || {}
      // The issuer wants the cardholder present. Not a failure — it is the
      // one outcome a retry can actually fix, and it is why this is a button
      // rather than a background job. Same 3DS flow posting uses.
      if (body.error === 'payment_authentication_required' && body.client_secret) {
        try {
          // Just the challenge. The "confirm" is the settle call below, re-run
          // once the bank is satisfied — there is no task to post here.
          await runCardChallenge({
            publishable_key: body.publishable_key,
            client_secret: body.client_secret,
          })
        } catch {
          // Fall through to the retry below either way: the server re-reads
          // the intent's real state, so a dismissed challenge simply leaves
          // the balance standing.
        }
        try {
          await api('/payments/settle-balance', { method: 'POST' })
          toast('Balance settled — thank you.')
        } catch (retryErr) {
          toast(retryErr?.body?.message || 'That card was declined. Try another card.')
        }
        await refresh()
        return
      }
      toast(body.message || "Couldn't settle that just now. Try again.")
      await refresh()
    } finally {
      setSettling(false)
    }
  }

  const message = outstandingBalanceMessage(outstanding)
  if (!message) return null

  return (
    <div className="border border-red-400/40 bg-red-400/10 text-red-200 rounded-md px-4 py-3 text-sm flex flex-wrap items-center justify-between gap-3">
      <span>{message}</span>
      <button
        type="button"
        onClick={settle}
        disabled={settling}
        className="rounded-md bg-red-400/90 px-3 py-1.5 text-black font-medium hover:bg-red-400 disabled:opacity-60"
      >
        {settling ? 'Settling…' : 'Settle now'}
      </button>
    </div>
  )
}
