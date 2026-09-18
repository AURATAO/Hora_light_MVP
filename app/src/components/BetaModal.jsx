import { useEffect, useState } from 'react'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { usePaymentGate } from '../hooks/usePaymentGate'
import { BETA_NOTICE_COPY, betaSettlementLine } from '../lib/traction'

/**
 * The beta welcome, shown once per account until it is accepted.
 *
 * Every word comes from lib/traction.js — this file carried its own copy until
 * the refresh, and that copy had drifted a long way: it still announced "a
 * closed beta pilot (April 2026)" in Midtown West with two coverage windows, a
 * three-tasks-a-day limit and a $30 purchase cap, none of which had been true
 * for months and none of which mobile's notice said. One source per client,
 * and a test that compares the two clients (lib/traction.test.mjs).
 */
/**
 * The one bullet that is not a constant: whether purchases settle in the app
 * or directly with the supporter.
 *
 * ITS OWN COMPONENT SO THE FETCH HAPPENS ONLY WHEN THE NOTICE IS ON SCREEN.
 * BetaModal is mounted by ProtectLayout on EVERY authed page, and a
 * usePaymentGate call in its body ran on all of them — a Stripe-backed request
 * per page load for every user, forever, to render a line almost none of them
 * would see. Worse, it raced the gate NewTask runs for its own banner: two
 * concurrent GET /payments/payment-methods share one Stripe idempotency key
 * for "create this user's customer", and the loser comes back 409, which the
 * handler answers as a 500. The local sweep caught it as two 500s on a task
 * screen. Rendered behind the `state === 'show'` early return, this fires once
 * per person, on the one screen that shows the line.
 *
 * `loading` is kept distinct from `false`: while the flag is unknown
 * betaSettlementLine returns null and the bullet is simply absent, which is
 * the safe direction (see its doc comment).
 */
function SettlementPoint() {
  const { enforced, loading } = usePaymentGate()
  const line = betaSettlementLine(loading ? null : enforced)
  if (!line) return null
  return (
    <li className="flex gap-3">
      <span className="text-secondary mt-0.5 shrink-0">•</span>
      <span>{line}</span>
    </li>
  )
}

export default function BetaModal() {
  // 'checking' | 'show' | 'done'
  const [state, setState] = useState('checking')
  const [checked, setChecked] = useState(false)
  const [saving, setSaving] = useState(false)
  const { user } = useAuth()

  useEffect(() => {
    if (!user?.id) return
    let alive = true
    async function check() {
      try {
        const profile = await api('/profile')
        if (!alive) return
        if (profile?.beta_accepted === true) {
          setState('done')
        } else {
          setState('show')
        }
      } catch {
        if (alive) setState('show')
      }
    }
    check()
    return () => { alive = false }
  }, [user?.id])

  async function accept() {
    setSaving(true)
    try {
      await api('/profile', { method: 'PATCH', body: { beta_accepted: true } })
    } catch {
      // best-effort — close modal regardless
    }
    setState('done')
    setSaving(false)
  }

  if (state !== 'show') return null

  return (
    <div className="fixed inset-0 z-9999 flex items-end sm:items-center justify-center bg-black/80 backdrop-blur-sm px-4 pb-4 sm:pb-0">
      <div className="w-full max-w-lg rounded-2xl border border-white/10 bg-surface shadow-2xl flex flex-col max-h-[90vh]">

        {/* Scrollable content */}
        <div className="overflow-y-auto flex-1 p-6 sm:p-8">
          <h1 className="font-heading text-2xl sm:text-3xl text-white mb-4">
            {BETA_NOTICE_COPY.heading}
          </h1>

          <div className="space-y-3 text-base text-white/70 font-secondary leading-relaxed mb-6">
            {BETA_NOTICE_COPY.intro.map(paragraph => (
              <p key={paragraph}>{paragraph}</p>
            ))}
            <ul className="space-y-2.5 pl-1">
              {BETA_NOTICE_COPY.points.map((item) => (
                <li key={item} className="flex gap-3">
                  <span className="text-secondary mt-0.5 shrink-0">•</span>
                  <span>{item}</span>
                </li>
              ))}
              <SettlementPoint />
            </ul>
            <p className="text-white/50 text-sm pt-1">
              {BETA_NOTICE_COPY.finePrint}
            </p>
          </div>

          <label className="flex items-start gap-3 cursor-pointer group">
            <input
              type="checkbox"
              checked={checked}
              onChange={e => setChecked(e.target.checked)}
              className="mt-1 accent-secondary shrink-0 w-4 h-4"
            />
            <span className="text-base text-white/60 group-hover:text-white/80 transition-colors">
              {BETA_NOTICE_COPY.acknowledgement}
            </span>
          </label>
        </div>

        {/* Sticky footer with CTA */}
        <div className="p-6 sm:p-8 pt-4 border-t border-white/10">
          <button
            onClick={accept}
            disabled={!checked || saving}
            className="w-full rounded-xl bg-brand py-3.5 text-base font-secondary font-semibold text-white
                       hover:brightness-110 transition-all disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {saving ? 'Saving…' : BETA_NOTICE_COPY.cta}
          </button>
        </div>
      </div>
    </div>
  )
}
