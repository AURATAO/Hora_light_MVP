import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { useToast } from '../providers/ToastProvider'
import SupporterStatusBanner from '../components/SupporterStatusBanner'

export default function BecomeSupporter() {
  const { user } = useAuth()
  const nav = useNavigate()
  const toast = useToast()

  const [firstName, setFirstName] = useState('')
  const [lastName, setLastName] = useState('')
  const [phone, setPhone] = useState('')
  const [city, setCity] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  // Where this account already is in the pipeline, from the same /profile
  // read that prefills the form.
  const [status, setStatus] = useState('')

  useEffect(() => {
    api('/profile')
      .then(p => {
        const parts = (p?.name ?? '').split(' ')
        setFirstName(parts[0] ?? '')
        setLastName(parts.slice(1).join(' '))
        setPhone(p?.phone ?? '')
        setCity(p?.city ?? '')
        setStatus(p?.supporter_status ?? '')
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  async function handleSubmit() {
    setSubmitted(true)
    if (!firstName.trim() || !lastName.trim() || !phone.trim() || !city.trim()) return
    setSubmitting(true)
    try {
      await api('/profile', {
        method: 'PATCH',
        body: { name: `${firstName.trim()} ${lastName.trim()}`, phone: phone.trim(), city: city.trim() },
      })
      await api('/supporter/apply', {
        method: 'POST',
        body: { first_name: firstName.trim(), last_name: lastName.trim() },
      })
      // Stay here and show the status the application now has. This used to
      // toast and leave for /my, where an applicant sees nothing about it.
      toast('Application submitted')
      setStatus('applied')
      setSubmitting(false)
    } catch (e) {
      toast(e.message || 'Something went wrong')
      setSubmitting(false)
    }
  }

  // Already applied or approved: the status, not a second form. Submitting
  // again reset supporter_applied_at and put a reviewed application back at
  // the end of the queue. REJECTED is deliberately not guarded — re-applying
  // clears the rejection (D-08), and a second chance is a form, not a notice.
  const inPipeline = status === 'applied' || status === 'approved'

  const inputClass =
    'w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white placeholder-white/30 outline-none focus:border-secondary/50 transition-colors'

  return (
    <div className="min-h-screen bg-primary text-accent py-12 px-4">
      <div className="mx-auto max-w-sm space-y-4">

        <Link to="/my" className="inline-block text-sm opacity-60 hover:opacity-100 transition-opacity">
          ← Back
        </Link>

        <div className="rounded-2xl border border-white/10 bg-[#2D343F] p-6 space-y-5">
          <div className="space-y-1 text-center">
            <h1 className="font-heading text-xl text-white">Apply to Become a Supporter</h1>
            <p className="text-sm text-white/50">
              We'll review your application and send you a background check link within 1–2 business days.
            </p>
          </div>

          <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 px-4 py-3 text-sm text-amber-300 space-y-1">
            <p className="font-semibold">Current eligibility requirements</p>
            <ul className="list-disc list-inside space-y-0.5 text-amber-300/80">
              <li>Must be located in the United States</li>
              <li>Must have a valid US government-issued ID</li>
            </ul>
          </div>

          {loading ? (
            <div className="text-sm text-white/40 text-center py-4">Loading…</div>
          ) : inPipeline ? (
            status === 'applied' ? (
              <SupporterStatusBanner status="applied" />
            ) : (
              <div className="text-center space-y-3">
                <p className="text-sm text-white/70">You&apos;re already a verified supporter.</p>
                <button
                  onClick={() => nav('/my')}
                  className="rounded-xl px-6 py-2.5 text-sm font-secondary font-semibold text-white hover:brightness-110"
                  style={{ backgroundColor: '#9aab3a' }}
                >
                  Go to tasks
                </button>
              </div>
            )
          ) : (
            <>
              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">First name</span>
                <input
                  type="text"
                  value={firstName}
                  onChange={e => setFirstName(e.target.value)}
                  placeholder="First name"
                  className={inputClass}
                />
                {submitted && !firstName.trim() && (
                  <p className="text-xs text-red-400">First name is required</p>
                )}
              </label>

              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">Last name</span>
                <input
                  type="text"
                  value={lastName}
                  onChange={e => setLastName(e.target.value)}
                  placeholder="Last name"
                  className={inputClass}
                />
                {submitted && !lastName.trim() && (
                  <p className="text-xs text-red-400">Last name is required</p>
                )}
              </label>

              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">Phone</span>
                <input
                  type="tel"
                  value={phone}
                  onChange={e => setPhone(e.target.value)}
                  placeholder="+1 212 555 0100"
                  className={inputClass}
                />
                {submitted && !phone.trim() && (
                  <p className="text-xs text-red-400">Phone is required</p>
                )}
              </label>

              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">City</span>
                <input
                  type="text"
                  value={city}
                  onChange={e => setCity(e.target.value)}
                  placeholder="e.g. New York"
                  className={inputClass}
                />
                {submitted && !city.trim() && (
                  <p className="text-xs text-red-400">City is required</p>
                )}
              </label>

              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">Email</span>
                <input
                  type="email"
                  value={user?.email ?? ''}
                  readOnly
                  className="w-full rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm text-white/40 outline-none cursor-not-allowed"
                />
              </label>

              <button
                onClick={handleSubmit}
                disabled={submitting}
                className="w-full rounded-xl py-3 text-sm font-secondary font-semibold text-white hover:brightness-110 transition-all disabled:opacity-60"
                style={{ backgroundColor: '#9aab3a' }}
              >
                {submitting ? 'Submitting…' : 'Submit Application →'}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
