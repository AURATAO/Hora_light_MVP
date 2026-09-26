import { useEffect, useState, useMemo } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { AuthAPI } from '../api/client.js'
import { useAuth } from '../auth/AuthContext.jsx'
import { useLoader } from '../providers/LoaderProvider.jsx'
import { RESEND_COOLDOWN_MS, resendLabel, resendSecondsLeft } from '../lib/otpResend.js'

export default function Login() {
  const [email, setEmail] = useState('')
  const [step, setStep] = useState('idle')
  const [code, setCode] = useState('')
  const [loading, setLoading] = useState(false)
  const [checking, setChecking] = useState(true)
  // When "Resend code" becomes tappable again (ms epoch); 0 = never sent.
  // Ticked once a second while a countdown is showing, nowhere else.
  const [resendReadyAt, setResendReadyAt] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const { wrap } = useLoader()
  const [agree, setAgree] = useState(false);

  const nav = useNavigate()
  const loc = useLocation()
  const { setUser } = useAuth()

  const from = useMemo(() => {
    const f = loc.state?.from
    if (!f || f === '/login') return '/'
    return f
  }, [loc.state])

  useEffect(() => {
    let alive = true
      ; (async () => {
        await wrap(async () => {
          const me = await AuthAPI.me()
          if (alive && me) {
            nav(from, { replace: true })
            return
          }
        }).finally(() => {
          if (alive) setChecking(false)
        })
      })()
    return () => { alive = false }
  }, [from, nav])

  useEffect(() => {
    if (step !== 'code' || resendSecondsLeft(resendReadyAt, now) === 0) return undefined
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [step, resendReadyAt, now])

  // 檢查中交給全域 Overlay，避免白底
  if (checking) return null

  // Sends (or re-sends) the code to the address in the field. A fresh send
  // always clears whatever was typed in the code box: the old code is dead
  // the moment a new one is issued, and leaving six stale digits in place is
  // how "Invalid code" gets reported against a working address.
  async function sendOtp() {
    if (!email || loading) return
    setLoading(true)
    try {
      await wrap(() => AuthAPI.requestOtp(email))
      setCode('')
      setStep('code')
      setResendReadyAt(Date.now() + RESEND_COOLDOWN_MS)
      setNow(Date.now())
    } catch (e) {
      alert(e.message || 'Failed to send code')
    } finally {
      setLoading(false)
    }
  }

  // Back to the email step with the address still in the field and
  // editable. Everything about the pending code is discarded — the digits,
  // the cooldown — because the next send is for a (possibly) different
  // address and must not inherit a countdown that belonged to the old one.
  function useDifferentEmail() {
    setCode('')
    setResendReadyAt(0)
    setStep('email')
  }

  const resendWait = resendSecondsLeft(resendReadyAt, now)

  async function verifyOtp(e) {
    e.preventDefault()
    if (!email || !code) return
    setLoading(true)
    try {
      await wrap(async () => {
        const data = await AuthAPI.verifyOtp(email, code)
        // Exchange Supabase token for internal hora_session cookie
        await AuthAPI.exchangeToken(data.session.access_token)
        const me = await AuthAPI.me()
        setUser(me)
        nav(from, { replace: true })
      })
    } catch (e) {
      alert(e.message || 'Invalid code')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center px-4">
      <div className="w-full max-w-sm rounded-2xl border border-slate-200 bg-white/80 backdrop-blur p-6 shadow-sm">
        <div className="mb-6 text-center">
          <div className="mx-auto h-2 w-2 pb-6 "><img src="/Loading_logo.png" alt="logo" /></div>
          <h1 className="mt-3 text-xl font-semibold text-slate-900">Welcome to HO:RA</h1>
          {/* <p className="mt-1 text-sm text-slate-500">Choose a sign-in method</p> */}
        </div>



        <div className="flex items-center justify-center gap-4">
          <button
            onClick={() => { if (!agree) return; AuthAPI.loginWithGoogle(from) }}
            disabled={!agree}
            title={!agree ? "Please agree to Privacy Policy and Terms to continue" : "Continue with Google"}
            aria-label="Continue with Google"
            className={`group h-12 w-12 rounded-full border border-slate-200 bg-white shadow-sm transition active:scale-95 flex items-center justify-center
              ${!agree ? "opacity-40 cursor-not-allowed" : "hover:shadow-md"}`}
          >
            <svg viewBox="0 0 48 48" className="h-6 w-6" aria-hidden>
              <path fill="#FFC107" d="M43.6 20.5H42V20H24v8h11.3C33.7 32 29.3 35 24 35c-7.2 0-13-5.8-13-13S16.8 9 24 9c3.3 0 6.3 1.2 8.6 3.2l5.7-5.7C34.6 3.4 29.6 1.5 24 1.5 11.5 1.5 1.5 11.5 1.5 24S11.5 46.5 24 46.5c12 0 22-9 22-22 0-1.5-.2-3-.4-4z" />
              <path fill="#FF3D00" d="M6.3 14.6l6.6 4.9C14.5 15.9 18.9 13 24 13c3.3 0 6.3 1.2 8.6 3.2l5.7-5.7C34.6 7.4 29.6 5.5 24 5.5c-7.8 0-14.4 4.3-17.7 10.6z" />
              <path fill="#4CAF50" d="M24 42.5c5.1 0 9.7-1.7 13.3-4.7l-6.1-5.1C29.1 34 26.7 35 24 35c-5.2 0-9.6-3.5-11.2-8.3l-6.6 5.1C9.4 38.1 16.1 42.5 24 42.5z" />
              <path fill="#1976D2" d="M46 24c0-1.5-.2-3-.4-4H24v8h11.3c-.8 3.7-3.2 6.2-6 7.7l6.1 5.1C38.3 38.7 46 32.5 46 24z" />
            </svg>
          </button>

          <button
            onClick={() => { if (!agree) return; setStep(prev => (prev === 'email' ? 'idle' : 'email')) }}
            disabled={!agree}
            title={!agree ? "Please agree to Privacy Policy and Terms to continue" : "Sign in with email"}
            aria-label="Sign in with email"
            className={`group h-12 w-12 rounded-full border border-slate-200 bg-white shadow-sm transition active:scale-95 flex items-center justify-center
              ${!agree ? "opacity-40 cursor-not-allowed" : "hover:shadow-md"}`}
          >
            <svg viewBox="0 0 24 24" className="h-6 w-6 text-slate-700" aria-hidden>
              <path d="M2 6.5A2.5 2.5 0 0 1 4.5 4h15A2.5 2.5 0 0 1 22 6.5v11A2.5 2.5 0 0 1 19.5 20h-15A2.5 2.5 0 0 1 2 17.5v-11Z" fill="none" stroke="currentColor" strokeWidth="1.5" />
              <path d="M3 7l8.5 6a2 2 0 0 0 2 0L22 7" fill="none" stroke="currentColor" strokeWidth="1.5" />
            </svg>
          </button>
        </div>

        <div className="my-6 h-px bg-slate-200" />

        {step === 'email' && (
          <form
            className="space-y-3"
            onSubmit={e => { e.preventDefault(); sendOtp() }}
          >
            <label className="block text-sm text-slate-600">
              Email
              <input
                type="email"
                autoFocus
                value={email}
                onChange={e => setEmail(e.target.value)}
                placeholder="you@example.com"
                className="mt-1 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-slate-900 placeholder-slate-400 outline-none focus:ring-2 focus:ring-slate-900/10"
              />
            </label>
            <button
              type="submit"
              disabled={loading || !email}
              className="w-full rounded-lg bg-slate-900 text-white py-2.5 disabled:opacity-50"
            >
              {loading ? 'Sending…' : 'Send code'}
            </button>
          </form>
        )}

        {step === 'code' && (
          <form onSubmit={verifyOtp} className="space-y-3">
            {/* The address the code went to, read-only here. It is edited by
                going back — not in place — because a code is bound to the
                address it was sent to, and an address changed underneath a
                pending code can only produce "Invalid code". */}
            <div className="text-sm text-slate-600">
              Code sent to <span className="font-medium text-slate-900 break-all">{email}</span>
            </div>
            <label className="block text-sm text-slate-600">
              6-digit code
              <input
                inputMode="numeric"
                pattern="[0-9]*"
                maxLength={6}
                autoFocus
                value={code}
                onChange={e => setCode(e.target.value.replace(/\D/g, ''))}
                placeholder="123456"
                className="mt-1 w-full tracking-[0.3em] text-center rounded-lg border border-slate-200 px-3 py-2"
              />
            </label>
            <button
              type="submit"
              disabled={!code || code.length < 6 || loading}
              className="w-full rounded-lg bg-slate-900 text-white py-2.5 disabled:opacity-50"
            >
              Verify & Continue
            </button>
            <div className="flex items-center justify-between text-sm">
              {/* The way back for a mistyped address. Returns to the email
                  step with the field pre-filled and editable. */}
              <button
                type="button"
                onClick={useDifferentEmail}
                disabled={loading}
                className="text-slate-600 underline underline-offset-4 hover:text-slate-900 disabled:opacity-50"
              >
                Use a different email
              </button>
              {/* Re-sends to the SAME address, behind a 30-second cooldown so
                  a tap-tap-tap does not get the address rate-limited. */}
              <button
                type="button"
                onClick={sendOtp}
                disabled={loading || resendWait > 0}
                aria-live="polite"
                className="text-slate-600 underline underline-offset-4 hover:text-slate-900 disabled:opacity-50 disabled:no-underline"
              >
                {resendLabel(resendReadyAt, now)}
              </button>
            </div>
          </form>
        )}

        <div className="flex items-center mb-4">
          <input
            type="checkbox"
            checked={agree}
            onChange={(e) => setAgree(e.target.checked)}
            className="mr-2 accent-secondary"
          />
          <span className="text-primary text-sm">
            I have read and agree to the <a href="https://www.my-hora.com/privacy" target="_blank" rel="noopener noreferrer" className="underline text-secondary">Privacy Policy</a> and <a href="https://www.my-hora.com/terms" target="_blank" rel="noopener noreferrer" className="underline text-secondary">Terms of Use</a>.
          </span>
        </div>

      </div>
    </div>
  )
}
