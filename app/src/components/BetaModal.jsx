import { useEffect, useState } from 'react'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'

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
            You're in. Welcome to HO:RA Beta.
          </h1>

          <div className="space-y-3 text-base text-white/70 font-secondary leading-relaxed mb-6">
            <p>
              HO:RA is a minute-billing platform for urban support —
              real people helping with real tasks in Midtown West, NYC.
            </p>
            <p>
              This is a closed beta pilot (April 2026). Here's what to know:
            </p>
            <ul className="space-y-2.5 pl-1">
              {[
                'Service is 100% free during beta — no platform fees',
                'Coverage hours: 11am–2pm and 7pm–10pm',
                'Up to 3 tasks per day (subject to runner availability)',
                'Every task is manually reviewed by the HO:RA team before a runner is dispatched',
                'Reimbursement only: you cover actual item costs (e.g. a $5 coffee), nothing else',
                'Purchase cap: $30 max per task',
              ].map((item) => (
                <li key={item} className="flex gap-3">
                  <span className="text-secondary mt-0.5 shrink-0">•</span>
                  <span>{item}</span>
                </li>
              ))}
            </ul>
            <p className="text-white/50 text-sm pt-1">
              By continuing, you agree to use this platform responsibly and
              understand this is an early-stage test.
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
              I understand this is a beta — things may change and some tasks
              may not be fulfilled
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
            {saving ? 'Saving…' : 'Enter HO:RA'}
          </button>
        </div>
      </div>
    </div>
  )
}
