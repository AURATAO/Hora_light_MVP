import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  ONBOARDING_COPY,
  createLoginLink,
  createOnboardingLink,
  getEarnings,
} from '../api/payments'
import { formatCents } from '../lib/formatCents'
import { useToast } from '../providers/ToastProvider'

/**
 * Earnings — the supporter's half of the money.
 *
 * Three states, and the whole component is a state machine over the one word
 * the backend sends:
 *
 *   not_started  no connected account. One button, one sentence.
 *   in_progress  Stripe wants more. Same button, different words, plus how
 *                much is left.
 *   complete     lifetime earned, recent transfers, and a way into the Stripe
 *                Express dashboard.
 *
 * WHAT THIS DELIBERATELY IS NOT. There is no wallet, no balance, and no
 * withdraw button. Stripe pays out on its own daily schedule straight to the
 * supporter's bank, and building a manual-withdraw flow on top of that would
 * mean holding somebody's money and inventing a second, worse payout system
 * next to the one that already works. "Manage payouts" hands them to Stripe's
 * dashboard, which is the complete and authoritative record.
 *
 * RENDERS NOTHING FOR NON-SUPPORTERS, and nothing at all when the backend has
 * no Stripe configured (503) or does not have these routes yet (404) — same
 * as PaymentMethods. A payouts card on a requester's profile is an invitation
 * to a flow that does not apply to them.
 */
export default function Earnings({ isSupporter }) {
  const toast = useToast()
  const [params, setParams] = useSearchParams()

  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)
  // Hidden rather than errored: an older backend, or one with no Stripe.
  const [unavailable, setUnavailable] = useState(false)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setData(await getEarnings())
      setUnavailable(false)
    } catch (e) {
      if (e?.status === 404 || e?.status === 503) setUnavailable(true)
      else toast(e?.body?.message || 'Couldn’t load your earnings')
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    if (isSupporter) load()
    else setLoading(false)
  }, [isSupporter, load])

  /**
   * Coming back from Stripe.
   *
   * `?onboarding=return` means only that the flow was entered and exited — NOT
   * that it was completed, and not that anything was approved. Stripe passes
   * no state through this URL by design. So the only correct response is to
   * re-read the account and render whatever it actually says, which `load()`
   * already does on mount; this just clears the query param so a refresh does
   * not look like a second return.
   *
   * `?onboarding=refresh` means the link went stale — expired, already used,
   * or a back button. Stripe's guidance is to mint a fresh one and send them
   * straight back in, which is what makes an interrupted setup resumable
   * rather than a dead end.
   */
  useEffect(() => {
    const flow = params.get('onboarding')
    if (!flow) return
    setParams(prev => {
      const next = new URLSearchParams(prev)
      next.delete('onboarding')
      return next
    }, { replace: true })
    if (flow === 'refresh') startOnboarding()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params])

  async function startOnboarding() {
    if (busy) return
    setBusy(true)
    try {
      const { url } = await createOnboardingLink()
      // A full navigation, not a new tab: the link is single-use, and a tab
      // the supporter can come back to after using it lands on an error page.
      window.location.href = url
    } catch (e) {
      toast(e?.body?.message || 'Couldn’t start payout setup. Try again in a moment.')
      setBusy(false)
    }
  }

  async function openDashboard() {
    if (busy) return
    setBusy(true)
    try {
      const { url } = await createLoginLink()
      window.open(url, '_blank', 'noopener')
    } catch (e) {
      toast(e?.body?.message || 'Couldn’t open your payouts dashboard.')
    } finally {
      setBusy(false)
    }
  }

  if (!isSupporter || unavailable) return null

  return (
    <div className="rounded-2xl border border-white/10 bg-[#2D343F] p-6 space-y-4">
      <h2 className="font-heading text-lg text-white">Earnings</h2>

      {loading ? (
        <div className="text-sm text-white/40">Loading…</div>
      ) : !data ? null : (
        <>
          <OnboardingCard
            state={data.onboarding?.state}
            requirementsDue={data.onboarding?.requirements_due || []}
            busy={busy}
            onStart={startOnboarding}
            onManage={openDashboard}
          />

          {data.onboarding?.state === 'complete' && (
            <>
              <div className="rounded-lg bg-white/5 px-4 py-3">
                <div className="text-xs text-white/50">Earned all time</div>
                <div className="text-2xl text-white font-semibold">
                  {formatCents(data.lifetime_earned_cents || 0)}
                </div>
              </div>
              <TransferList transfers={data.transfers || []} />
            </>
          )}
        </>
      )}
    </div>
  )
}

function OnboardingCard({ state, requirementsDue, busy, onStart, onManage }) {
  const copy = ONBOARDING_COPY[state] || ONBOARDING_COPY.not_started
  const done = state === 'complete'

  return (
    <div className="space-y-2">
      <div className="flex items-start gap-2">
        <span
          className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${done ? 'bg-emerald-400' : 'bg-amber-400'}`}
          aria-hidden
        />
        <div className="flex-1">
          <div className="text-sm text-white">{copy.title}</div>
          <p className="text-xs text-white/50 mt-0.5">{copy.body}</p>
          {/* The COUNT, never the field names. Stripe spells them
              "individual.verification.document", which tells a supporter
              nothing and looks like an error. The hosted form is what
              explains them. */}
          {!done && requirementsDue.length > 0 && (
            <p className="text-xs text-white/40 mt-0.5">
              {requirementsDue.length} {requirementsDue.length === 1 ? 'detail' : 'details'} still needed.
            </p>
          )}
        </div>
      </div>

      <button
        onClick={done ? onManage : onStart}
        disabled={busy}
        className="w-full rounded-xl py-2.5 text-sm font-secondary font-semibold text-white
                   hover:brightness-110 transition-all disabled:opacity-50"
        style={{ backgroundColor: done ? 'transparent' : '#3A5A2D' }}
      >
        <span className={done ? 'underline underline-offset-4 text-white/70' : ''}>
          {busy ? 'Opening…' : copy.cta}
        </span>
      </button>
    </div>
  )
}

/**
 * Recent transfers. A strip, not a ledger — "Manage payouts" goes to Stripe's
 * dashboard, which has the complete record, and paginating here would be
 * building a worse copy of it.
 */
function TransferList({ transfers }) {
  if (transfers.length === 0) {
    return (
      <p className="text-xs text-white/40">
        Nothing yet. Payments appear here after you complete a task.
      </p>
    )
  }

  return (
    <div className="space-y-2">
      <div className="text-xs text-white/50">Recent</div>
      {transfers.map(t => (
        <div key={`${t.task_id}-${t.created_at}`} className="flex justify-between gap-3 text-sm">
          <div className="min-w-0">
            <div className="truncate text-white/80">{t.task_title || 'Task'}</div>
            {/* The split, said out loud. A supporter who sees one total for a
                shopping task cannot tell what they MADE from what they are
                being handed back, and those are very different numbers. */}
            <div className="text-xs text-white/40">
              {formatCents(t.time_cents)} time
              {t.receipt_cents > 0 && <> + {formatCents(t.receipt_cents)} reimbursement</>}
            </div>
          </div>
          <div className="text-right shrink-0">
            <div className="text-white">{formatCents(t.amount_cents)}</div>
            {t.status !== 'paid' && (
              <div className="text-xs text-white/40">
                {t.status === 'failed' ? 'We’re sorting this out' : 'On its way'}
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}
