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
 *   verifying    Form in, nothing due, Stripe not yet done. "Check again".
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
  // Whether the strip has been expanded past its three-row preview, and
  // whether the next page is in flight.
  const [expanded, setExpanded] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)

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

  // "See all" fetches the rest IN PLACE rather than routing to a new screen:
  // this card already lives inside the profile page, the list is short enough
  // to belong there, and a whole route for it would be a page whose only
  // content is a list the user was already looking at.
  const loadAllTransfers = useCallback(async () => {
    setLoadingMore(true)
    try {
      const full = await getEarnings({ limit: 50 })
      setData(full)
      setExpanded(true)
    } catch (e) {
      toast(e?.body?.message || 'Couldn\u2019t load your payments')
    } finally {
      setLoadingMore(false)
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
            onCheckAgain={load}
          />

          {data.onboarding?.state === 'complete' && (
            <>
              <div className="rounded-lg bg-white/5 px-4 py-3">
                <div className="text-xs text-white/50">Earned all time</div>
                <div className="text-2xl text-white font-semibold">
                  {formatCents(data.lifetime_earned_cents || 0)}
                </div>
              </div>
              <TransferList
                transfers={data.transfers || []}
                total={data.total ?? (data.transfers || []).length}
                expanded={expanded}
                loadingMore={loadingMore}
                onSeeAll={loadAllTransfers}
              />
            </>
          )}
        </>
      )}
    </div>
  )
}

function OnboardingCard({ state, requirementsDue, busy, onStart, onManage, onCheckAgain }) {
  const copy = ONBOARDING_COPY[state] || ONBOARDING_COPY.not_started
  const done = state === 'complete'
  const verifying = state === 'verifying'

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
          {!done && !verifying && requirementsDue.length > 0 && (
            <p className="text-xs text-white/40 mt-0.5">
              {requirementsDue.length} {requirementsDue.length === 1 ? 'detail' : 'details'} still needed.
            </p>
          )}
        </div>
      </div>

      <button
        onClick={done ? onManage : verifying ? onCheckAgain : onStart}
        disabled={busy}
        className="w-full rounded-xl py-2.5 text-sm font-secondary font-semibold text-white
                   hover:brightness-110 transition-all disabled:opacity-50"
        style={{ backgroundColor: done || verifying ? 'transparent' : '#3A5A2D' }}
      >
        <span className={done || verifying ? 'underline underline-offset-4 text-white/70' : ''}>
          {busy ? 'Opening…' : copy.cta}
        </span>
      </button>
      {/* A way back into Stripe's form while verifying: what Stripe files as
          "eventually due" (a date of birth, the last four of an SSN) is not
          in the count above, and is sometimes exactly what unblocks the
          account. */}
      {verifying && (
        <button
          onClick={onStart}
          disabled={busy}
          className="w-full py-1 text-xs text-white/50 underline underline-offset-4
                     hover:text-white/70 transition-all disabled:opacity-50"
        >
          Update details on Stripe
        </button>
      )}
    </div>
  )
}

/** How many transfers the strip shows before deferring to "See all". */
const PREVIEW_COUNT = 3

/**
 * Recent transfers: the three most recent, and a way to the rest.
 *
 * WHY IT IS CAPPED NOW. This used to render every transfer the endpoint
 * returned — twenty of them — which grew with how much somebody had worked, so
 * the supporters who had earned the most had the longest scroll between them
 * and the number they came for, which is the lifetime total directly above.
 *
 * "Manage payouts" still goes to Stripe's dashboard and always will: that is
 * the record of what reached their BANK. This is the record of what HO:RA paid
 * them and which task each payment was for, and sending somebody out to an
 * external dashboard to answer "what have I earned here" is an export, not a
 * list. `onSeeAll` loads the next page in place.
 */
function TransferList({ transfers, total, onSeeAll, expanded, loadingMore }) {
  if (transfers.length === 0) {
    return (
      <p className="text-xs text-white/40">
        Nothing yet. Payments appear here after you complete a task.
      </p>
    )
  }

  const shown = expanded ? transfers : transfers.slice(0, PREVIEW_COUNT)
  const hasMore = total > shown.length

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <div className="text-xs text-white/50">Recent</div>
        {/* The NUMBER, on purpose: "See all" alone makes a reader guess
            whether there are four or four hundred, which is the thing they
            are actually asking when they look at a truncated list. */}
        {hasMore && (
          <button
            type="button"
            onClick={onSeeAll}
            disabled={loadingMore}
            className="text-xs text-white/60 underline hover:text-white disabled:opacity-50"
          >
            {loadingMore ? 'Loading…' : `See all (${total})`}
          </button>
        )}
      </div>
      {shown.map(t => (
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
