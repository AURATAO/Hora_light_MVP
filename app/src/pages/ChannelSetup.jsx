// Book with HO:RA — the one page a first-time chat booker visits.
//
// Reached from the link HO:RA texts after the person replies YES to a priced
// task in Messages. Name and Terms were given in the chat; what is left is the
// two things a chat cannot do: prove the email (so there is an account they can
// also open in the app) and take a card.
//
// D-16 R-4. This used to be HTML embedded in the Go binary
// (server/channel_setup.html + .js, still there, serving links already sent).
// It is the only page of this product that lived outside app/, and it looked
// it. Same flow, same endpoints, same proof model — Tailwind and this app's
// Supabase and Stripe clients instead of its own.
//
// PROOF MODEL, unchanged (server/channel_setup.go):
//   the setup token  proves this person reads messages sent to that number
//   the email code   proves the email is theirs → a normal HO:RA account
//   both together    POST /channel/setup/link attaches the number to the
//                    account; the server takes the identity from the session
//                    and never from the request body (S-11).
//
// THE TOKEN IS IN THE FRAGMENT (#t=…) and must stay there: a fragment is never
// sent to a server, so it cannot land in a request log, a proxy, or the
// link-preview fetch iMessage and WhatsApp make of every URL (S-12). It is
// taken out of the address bar as soon as it is read.
//
// ALREADY SIGNED IN. A session is "who" and the token is "which number", and
// the server accepts exactly that pair — so a person already signed in here
// (they opened the app before, or they are coming back to a second link) is
// shown the account and asked to confirm, rather than made to type an email
// and a code again. Asked, not assumed: a browser can be signed in as someone
// else entirely, and linking a phone number to the wrong account is not
// something to do silently. "Use a different email" is always there.
import { useEffect, useMemo, useRef, useState } from 'react'
import { loadStripe } from '@stripe/stripe-js'
import { Elements, PaymentElement, useElements, useStripe } from '@stripe/react-stripe-js'
import { supabase } from '../lib/supabaseClient'
import {
  getSetupConfig,
  checkSetupToken,
  linkSetupNumber,
  createSetupIntent,
  finishSetup,
} from '../api/channelSetup'

// loadStripe injects a script tag, so it is memoised per key. The key comes
// from the BACKEND (the SetupIntent response), never from a VITE_ variable,
// so rotating it — or switching test → live — needs no web deploy.
const stripePromises = new Map()
function stripeFor(publishableKey) {
  if (!stripePromises.has(publishableKey)) {
    stripePromises.set(publishableKey, loadStripe(publishableKey))
  }
  return stripePromises.get(publishableKey)
}

const ELEMENTS_APPEARANCE = {
  theme: 'stripe',
  variables: {
    colorPrimary: '#3A5A2D',
    colorText: '#222831',
    colorDanger: '#A3332A',
    fontFamily: 'Inter, system-ui, -apple-system, "Segoe UI", sans-serif',
    borderRadius: '10px',
    spacingUnit: '4px',
  },
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

// Read the token out of the fragment, remember it for this tab only, and take
// it out of the address bar (screenshots, history, sharing).
function readToken() {
  let t = ''
  try {
    t = new URLSearchParams(window.location.hash.replace(/^#/, '')).get('t') || ''
  } catch { t = '' }
  try {
    if (t) sessionStorage.setItem('hora_setup_t', t)
    else t = sessionStorage.getItem('hora_setup_t') || ''
  } catch { /* storage blocked: keep it in memory only */ }
  if (window.location.search || window.location.hash) {
    window.history.replaceState(null, '', window.location.pathname)
  }
  return t
}

function forgetToken() {
  try { sessionStorage.removeItem('hora_setup_t') } catch { /* ignore */ }
}

function otpMessage(err) {
  const m = err?.message || ''
  if (/rate|seconds|too many/i.test(m)) return 'Too many codes requested. Wait a minute, then try again.'
  return "We couldn't send the code. Check the email address and try again."
}

// ── chrome ────────────────────────────────────────────────────────────────

function Steps({ step }) {
  return (
    <div className="mb-6 flex gap-1.5" aria-hidden="true">
      {[1, 2, 3].map((i) => (
        <span
          key={i}
          className={`h-[3px] flex-1 rounded-sm ${i <= step ? 'bg-[#3A5A2D]' : 'bg-[#E5E7EB]'}`}
        />
      ))}
    </div>
  )
}

function ErrorNote({ children }) {
  if (!children) return null
  return (
    <div role="alert" className="mb-4 rounded-lg bg-[#FBECEB] px-3 py-2.5 text-sm text-[#A3332A]">
      {children}
    </div>
  )
}

const BTN = 'w-full rounded-[10px] bg-[#222831] px-4 py-3.5 font-semibold text-white disabled:opacity-45'
const LINK_BTN = 'mt-1.5 w-full rounded-[10px] px-4 py-2.5 text-[#5F6670] font-medium'
const INPUT =
  'w-full rounded-[10px] border border-[#E5E7EB] bg-white px-3.5 py-3 text-base text-[#222831] outline-none focus:border-[#3A5A2D] focus:ring-2 focus:ring-[#3A5A2D]/20'

// ── the card step ─────────────────────────────────────────────────────────

function CardForm({ email, onDone, onSkip }) {
  const stripe = useStripe()
  const elements = useElements()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function submit(e) {
    e.preventDefault()
    if (!stripe || !elements) return
    setBusy(true)
    setError('')
    // redirect: 'if_required' keeps the common card case in-page; a 3DS
    // challenge opens in Stripe's own modal. An Italian or other EEA card
    // will usually take that path, and it is the same code either way.
    const { error: stripeError } = await stripe.confirmSetup({
      elements,
      redirect: 'if_required',
      confirmParams: email ? { payment_method_data: { billing_details: { email } } } : undefined,
    })
    if (stripeError) {
      // Stripe's own message: it is written for the cardholder and names the
      // field that was wrong.
      setError(stripeError.message || "That card couldn't be saved. Try another card.")
      setBusy(false)
      return
    }
    setBusy(false)
    onDone()
  }

  return (
    <form onSubmit={submit} noValidate>
      <div className="mb-4 rounded-[10px] border border-[#E5E7EB] bg-white p-3.5">
        <PaymentElement options={{ layout: 'tabs', wallets: { applePay: 'auto', googlePay: 'auto' } }} />
      </div>
      <ErrorNote>{error}</ErrorNote>
      <button type="submit" className={BTN} disabled={busy || !stripe}>
        {busy ? 'Saving…' : 'Save card and book'}
      </button>
      <button type="button" className={LINK_BTN} onClick={onSkip} disabled={busy}>
        Skip for now
      </button>
    </form>
  )
}

// ── the page ──────────────────────────────────────────────────────────────

export default function ChannelSetup() {
  // loading | expired | email | code | confirm | card | done
  const [view, setView] = useState('loading')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const [phoneHint, setPhoneHint] = useState('')
  const [booking, setBooking] = useState(null)
  const [cfg, setCfg] = useState(null)
  const [sessionEmail, setSessionEmail] = useState('') // an existing signed-in account
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [intent, setIntent] = useState(null) // { client_secret, publishable_key }
  const [result, setResult] = useState(null) // { title, lead, ok }

  const tokenRef = useRef('')
  const emailRef = useRef(null)
  const codeRef = useRef(null)

  const step = view === 'card' ? 3 : view === 'code' ? 2 : 1

  useEffect(() => {
    let alive = true
    ;(async () => {
      const token = readToken()
      tokenRef.current = token
      if (!token) { setView('expired'); return }
      const [config, check, { data: { session } }] = await Promise.all([
        getSetupConfig().catch(() => ({ ok: false, data: {} })),
        checkSetupToken(token).catch(() => ({ ok: false, data: {} })),
        supabase.auth.getSession().catch(() => ({ data: { session: null } })),
      ])
      if (!alive) return
      if (!check.ok) { setView('expired'); return }
      setCfg(config.data || {})
      setPhoneHint(check.data.phone_hint || 'your phone')
      setBooking(check.data.booking || null)
      const who = session?.user?.email || ''
      if (who) {
        setSessionEmail(who)
        setView('confirm')
      } else {
        setView('email')
      }
    })()
    return () => { alive = false }
  }, [])

  useEffect(() => {
    if (view === 'email') emailRef.current?.focus()
    if (view === 'code') codeRef.current?.focus()
  }, [view])

  // ── steps ──────────────────────────────────────────────────────────────

  async function sendCode(e) {
    e?.preventDefault()
    const addr = email.trim().toLowerCase()
    if (!EMAIL_RE.test(addr)) { setError('Enter a valid email address.'); return }
    setError('')
    setBusy(true)
    const { error: err } = await supabase.auth.signInWithOtp({
      email: addr,
      options: { shouldCreateUser: true },
    })
    setBusy(false)
    if (err) { setError(otpMessage(err)); return }
    setEmail(addr)
    setCode('')
    setView('code')
  }

  async function verifyCode(e) {
    e?.preventDefault()
    const digits = code.replace(/\D/g, '')
    if (digits.length < 6) { setError('Enter the 6-digit code from the email.'); return }
    setError('')
    setBusy(true)
    const { data, error: err } = await supabase.auth.verifyOtp({
      email, token: digits, type: 'email',
    })
    if (err || !data?.session) {
      setBusy(false)
      setError("That code didn't work. Check the latest email, or go back and resend.")
      return
    }
    await linkThenCard(email)
  }

  // Link the number to the signed-in account, then go to the card step. The
  // only place the token is spent; it is forgotten the moment it is.
  async function linkThenCard(forEmail) {
    setError('')
    setBusy(true)
    const res = await linkSetupNumber(tokenRef.current).catch(() => ({ ok: false, status: 0, data: {} }))
    if (res.status === 410) { setBusy(false); forgetToken(); setView('expired'); return }
    if (res.status === 409) {
      setBusy(false)
      setError(
        'This phone number is already connected to a different HO:RA account. ' +
        'Sign in with that account’s email, or write to info@my-hora.com.',
      )
      setView('email')
      return
    }
    if (!res.ok) {
      setBusy(false)
      setError("We couldn't connect your number. Please try again.")
      return
    }
    forgetToken()
    if (res.data.phone_hint) setPhoneHint(res.data.phone_hint)

    // The card. A 503 means payments are not configured here (staging): book
    // straight away, and if the server does need a card, finish() says so.
    const si = await createSetupIntent().catch(() => ({ ok: false, data: {} }))
    if (!si.ok || !si.data.client_secret || !si.data.publishable_key) {
      await book(forEmail)
      return
    }
    setIntent({ client_secret: si.data.client_secret, publishable_key: si.data.publishable_key })
    setBusy(false)
    setView('card')
  }

  async function book() {
    setError('')
    setBusy(true)
    const res = await finishSetup().catch(() => ({ ok: false, data: {} }))
    setBusy(false)
    if (!res.ok) { setError("We couldn't finish booking. Please try again."); return }
    const outcome = res.data.outcome || ''
    if (outcome === 'booked') {
      setResult({ ok: true, title: 'Booked', lead: "Go back to Messages. We'll text you when a supporter accepts." })
    } else if (outcome === 'needs_card') {
      setResult({ ok: false, title: 'Almost there', lead: 'We need a card to book. Check Messages for a link to add one.' })
    } else if (outcome === 'declined' || outcome === 'outstanding_balance' || outcome === 'needs_authentication') {
      setResult({ ok: false, title: 'Almost there', lead: "We couldn't place the booking yet. Check Messages for what to do next." })
    } else {
      setResult({ ok: false, title: "You're all set", lead: 'Go back to Messages and tell HO:RA what you need.' })
    }
    setView('done')
  }

  async function useAnotherEmail() {
    // Signing out here is deliberate: the next step signs a (possibly
    // different) person in, and two sessions cannot be held at once.
    setError('')
    setSessionEmail('')
    setCode('')
    try { await supabase.auth.signOut() } catch { /* already gone */ }
    setView('email')
  }

  const elementsOptions = useMemo(
    () => (intent ? { clientSecret: intent.client_secret, appearance: ELEMENTS_APPEARANCE } : null),
    [intent],
  )

  return (
    <main className="min-h-screen bg-[#FAFAF7] text-[#222831]">
      <div className="mx-auto w-full max-w-[440px] px-5 pb-10 pt-7">
        <div className="mb-7 text-[22px] font-semibold tracking-[0.02em] text-[#3A5A2D]">
          HO<b className="text-[#E1B145]">:</b>RA
        </div>

        {view !== 'expired' && view !== 'loading' && <Steps step={step} />}
        <ErrorNote>{error}</ErrorNote>

        {view === 'loading' && <p className="text-[#5F6670]">Checking your link…</p>}

        {view === 'expired' && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">This link has expired</h1>
            <p className="text-[#5F6670]">
              Booking links work once, for 30 minutes. Text HO:RA again and we'll send you a fresh one.
            </p>
          </>
        )}

        {(view === 'email' || view === 'code' || view === 'confirm') && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">
              {view === 'code' ? 'Enter the code' : 'One step to book'}
            </h1>

            {booking && view !== 'code' && (
              <div className="mb-5 rounded-[10px] border border-[#E5E7EB] bg-white px-3.5 py-3">
                <b className="block font-semibold">{booking.title}</b>
                <span className="text-sm text-[#5F6670]">
                  Estimate {booking.estimate} · you pay for the time actually used
                </span>
              </div>
            )}

            {view === 'confirm' && (
              <>
                <p className="mb-6 text-[#5F6670]">
                  You're signed in as <b className="text-[#222831]">{sessionEmail}</b>. We'll connect
                  texts from {phoneHint} to this account.
                </p>
                <button className={BTN} disabled={busy} onClick={() => linkThenCard(sessionEmail)}>
                  {busy ? 'One moment…' : 'Connect and continue'}
                </button>
                <button type="button" className={LINK_BTN} disabled={busy} onClick={useAnotherEmail}>
                  Use a different email
                </button>
              </>
            )}

            {view === 'email' && (
              <>
                <p className="mb-6 text-[#5F6670]">
                  For texts from {phoneHint}. Your email first: we'll send a 6-digit code to confirm
                  it's yours.
                </p>
                <form onSubmit={sendCode} noValidate>
                  <div className="mb-4">
                    <label htmlFor="setup-email" className="mb-1.5 block text-sm font-medium">Email</label>
                    <input
                      id="setup-email"
                      ref={emailRef}
                      type="email"
                      autoComplete="email"
                      autoCapitalize="off"
                      className={INPUT}
                      value={email}
                      onChange={(e) => setEmail(e.target.value)}
                      required
                    />
                  </div>
                  <button type="submit" className={BTN} disabled={busy}>
                    {busy ? 'Sending…' : 'Send code'}
                  </button>
                </form>
              </>
            )}

            {view === 'code' && (
              <>
                <p className="mb-6 text-[#5F6670]">
                  We sent a 6-digit code to <b className="text-[#222831]">{email}</b>.
                </p>
                <form onSubmit={verifyCode} noValidate>
                  <div className="mb-4">
                    <label htmlFor="setup-code" className="mb-1.5 block text-sm font-medium">Code</label>
                    <input
                      id="setup-code"
                      ref={codeRef}
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      maxLength={10}
                      className={INPUT}
                      value={code}
                      onChange={(e) => setCode(e.target.value)}
                      required
                    />
                  </div>
                  <button type="submit" className={BTN} disabled={busy}>
                    {busy ? 'Checking…' : 'Confirm'}
                  </button>
                  <button
                    type="button"
                    className={LINK_BTN}
                    disabled={busy}
                    onClick={() => { setCode(''); setError(''); setView('email') }}
                  >
                    Use a different email
                  </button>
                </form>
              </>
            )}
          </>
        )}

        {view === 'card' && elementsOptions && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">Add a card</h1>
            <p className="mb-6 text-[#5F6670]">
              Nothing is charged now. We reserve the estimate when you book, and you pay for the time
              actually used.
            </p>
            <Elements stripe={stripeFor(intent.publishable_key)} options={elementsOptions}>
              <CardForm email={sessionEmail || email} onDone={book} onSkip={book} />
            </Elements>
          </>
        )}

        {view === 'done' && result && (
          <>
            <div
              aria-hidden="true"
              className="mb-4 grid h-[52px] w-[52px] place-items-center rounded-full bg-[#3A5A2D] text-2xl text-white"
            >
              {result.ok ? '✓' : '→'}
            </div>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">{result.title}</h1>
            <p className="text-[#5F6670]">{result.lead}</p>
          </>
        )}

        {(view === 'email' || view === 'code' || view === 'confirm' || view === 'card') && cfg && (
          <p className="mt-5 text-[13px] text-[#5F6670]">
            By booking you agree to our{' '}
            <a className="text-[#222831] underline" href={cfg.terms_url || 'https://www.my-hora.com/terms'} target="_blank" rel="noreferrer">Terms</a>
            {' '}and{' '}
            <a className="text-[#222831] underline" href={cfg.privacy_url || 'https://www.my-hora.com/privacy'} target="_blank" rel="noreferrer">Privacy Policy</a>.
          </p>
        )}
      </div>
    </main>
  )
}
