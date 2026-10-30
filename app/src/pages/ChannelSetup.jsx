// Book with HO:RA — the one page a first-time chat booker visits.
//
// Reached from the link HO:RA texts after the person replies YES to a priced
// task in Messages. Name and Terms were given in the chat; what is left is the
// two things a chat cannot do: prove the email (so there is an account they can
// also open in the app) and take a card.
//
// D-16 R-4. This used to be HTML embedded in the Go binary
// (server/channel_setup.html + .js, still there for links already sent and for
// staging). Same flow, same endpoints, same proof model — Tailwind and this
// app's Supabase and Stripe clients instead of its own.
//
// PROOF MODEL, unchanged (server/channel_setup.go, S-11 condition 2):
//   the setup token  proves this person reads messages sent to that number
//   the email code   proves the email is theirs, TYPED HERE, THIS VISIT
//   both together    POST /channel/setup/link attaches the number to the
//                    account; the server takes the identity from the session
//                    and never from the request body.
//
// WHY A SIGNED-IN SESSION IS NOT ENOUGH, though the server would accept it.
// A first draft of this page offered "you're signed in as X — connect and
// continue" to save a signed-in person typing an email and a code. That is a
// one-tap account takeover and it was removed (review r1): anyone can text
// HO:RA, reply YES, and get a valid link of their own; send that URL to
// someone who happens to be signed in here and one tap binds the SENDER's
// phone number to the RECIPIENT's account — after which every YES the sender
// texts books a task against the recipient's card, and the binding cannot be
// undone short of deleting the account (S-11 condition 4). The fresh code is
// what makes the person holding the phone and the person holding the mailbox
// the same person. A session is only ever used to PREFILL the email field.
//
// THE TOKEN IS IN THE FRAGMENT (#t=…) and must stay there: a fragment is never
// sent to a server, so it cannot land in a request log, a proxy, or the
// link-preview fetch iMessage and WhatsApp make of every URL (S-12). It is
// read during the first render — before paint, before any effect — and taken
// out of the address bar in the same breath.
//
// EVERY DEAD END HAS A WAY OUT, and each retry retries the step that failed —
// never one that cannot succeed twice. There are three, because there are
// three things that can be spent: a link the server rejects is "expired" (text
// us again) and one we could not reach is "offline" (try again, the token is
// kept); once the CODE is spent the retry is the linking, not the code; once
// the NUMBER is linked the retry is the booking, not the link. A button that
// re-submits a used one-time code is how a person ends up asking for a third
// email they do not need (review r2).
//
// WHOSE SESSION. The access token this visit minted is held here and passed
// to every authenticated call; nothing re-reads it from shared storage and no
// cookie is sent. `dualAuth` prefers the `hora_session` cookie over the
// Authorization header, and that cookie is `SameSite=None` in production — so
// a browser still holding one for an older account would otherwise decide
// which account the phone number binds to, while this page said otherwise
// (api/channelSetup.js).
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Elements, PaymentElement, useElements, useStripe } from '@stripe/react-stripe-js'
import { supabase } from '../lib/supabaseClient'
import {
  getSetupConfig,
  checkSetupToken,
  linkSetupNumber,
  createSetupIntent,
  listSavedCards,
  finishSetup,
} from '../api/channelSetup'

// Stripe.js is loaded only when the card step is reached, from the '/pure'
// entry point: the plain entry injects the script on import, which would run
// third-party code on this page while the token is still in the URL.
// Memoised per key because each call injects a script tag. The key comes from
// the BACKEND (the SetupIntent response), never from a VITE_ variable, so
// rotating it — or switching test → live — needs no web deploy.
const stripePromises = new Map()
async function stripeFor(publishableKey) {
  if (!stripePromises.has(publishableKey)) {
    // Evicted if it rejects: a cached rejection makes "try again" fail
    // forever, which is the one thing a retry must not do.
    const p = import('@stripe/stripe-js/pure')
      .then(({ loadStripe }) => loadStripe(publishableKey))
      .catch((err) => { stripePromises.delete(publishableKey); throw err })
    stripePromises.set(publishableKey, p)
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
// it out of the address bar (screenshots, history, sharing). Called from a
// useState initializer: this runs during the first render, so the URL is clean
// before anything paints or any other script gets a turn.
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
    // Keep the existing history state: React Router keeps its own keys in
    // there and nulling them breaks its back/forward bookkeeping.
    window.history.replaceState(window.history.state, '', window.location.pathname)
  }
  return t
}

function forgetToken() {
  try { sessionStorage.removeItem('hora_setup_t') } catch { /* ignore */ }
}

// Only the two answers setupTokenAuth actually gives for a bad link mean the
// link is bad: 410 (unknown, used or expired) and 400 (no token in the body).
// Everything else — a 403 from a misconfigured origin, a 404 from the wrong
// base URL, a 429, a 5xx, a request that never completed, a 200 of HTML from
// an SPA rewrite — is our problem, and telling someone to ask for a new link
// sends them round a loop that fails identically.
function linkIsBad(res) {
  return res.status === 410 || res.status === 400
}

// The access token minted at the code step is not refreshed by this page, and
// Supabase's default lifetime is an hour — long enough that somebody who
// walks away mid-card comes back to a 401. It is its own screen, because
// "sign in again" is the only thing that fixes it and no amount of retrying
// does (review r3).
function isUnauthorized(res) {
  return res.status === 401 || res.status === 403
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
        <span key={i} className={`h-[3px] flex-1 rounded-sm ${i <= step ? 'bg-[#3A5A2D]' : 'bg-[#E5E7EB]'}`} />
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

// "visa" → "Visa", "amex" → "Amex"; Stripe's brand ids are lower-case.
function brandName(b) {
  const s = String(b || 'card')
  return s === 'card' ? 'card' : s.charAt(0).toUpperCase() + s.slice(1)
}

const BTN = 'w-full rounded-[10px] bg-[#222831] px-4 py-3.5 font-semibold text-white disabled:opacity-45'
const LINK_BTN = 'mt-1.5 w-full rounded-[10px] px-4 py-2.5 font-medium text-[#5F6670] disabled:opacity-45'
const INPUT =
  'w-full rounded-[10px] border border-[#E5E7EB] bg-white px-3.5 py-3 text-base text-[#222831] outline-none focus:border-[#3A5A2D] focus:ring-2 focus:ring-[#3A5A2D]/20'

// ── the card step ─────────────────────────────────────────────────────────

function CardForm({ email, busy, onDone, onSkip, skipLabel }) {
  const stripe = useStripe()
  const elements = useElements()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  // One disabled flag for both buttons, and it includes the parent's booking:
  // confirmSetup succeeding is not the end of this step, so re-enabling here
  // would let a second tap confirm an already-confirmed SetupIntent or start a
  // second booking.
  const locked = saving || busy || !stripe

  async function submit(e) {
    e.preventDefault()
    if (!stripe || !elements) return
    setSaving(true)
    setError('')
    // redirect: 'if_required' keeps the common card case in-page; a 3DS
    // challenge opens in Stripe's own modal. An EEA card usually takes that
    // path, and it is the same code either way.
    let stripeError = null
    try {
      // Thrown, not returned, for an integration error — a payment method
      // that can only be completed by redirect, with no return_url. Caught,
      // because an uncaught one leaves both buttons locked for good, Skip
      // included, and there is no other way off this screen.
      ;({ error: stripeError } = await stripe.confirmSetup({
        elements,
        redirect: 'if_required',
        confirmParams: email ? { payment_method_data: { billing_details: { email } } } : undefined,
      }))
    } catch (err) {
      console.error('[channel-setup] confirmSetup threw:', err)
      setError("That payment method can't be saved here. Try a card instead.")
      setSaving(false)
      return
    }
    if (stripeError) {
      // Stripe's own message: it is written for the cardholder and names the
      // field that was wrong.
      setError(stripeError.message || "That card couldn't be saved. Try another card.")
      setSaving(false)
      return
    }
    await onDone()
    setSaving(false)
  }

  return (
    <form onSubmit={submit} noValidate>
      <div className="mb-4 rounded-[10px] border border-[#E5E7EB] bg-white p-3.5">
        <PaymentElement options={{ layout: 'tabs', wallets: { applePay: 'auto', googlePay: 'auto' } }} />
      </div>
      <ErrorNote>{error}</ErrorNote>
      <button type="submit" className={BTN} disabled={locked}>
        {saving || busy ? 'Saving…' : 'Save card and book'}
      </button>
      {skipLabel && (
        <button type="button" className={LINK_BTN} onClick={onSkip} disabled={locked}>
          {skipLabel}
        </button>
      )}
    </form>
  )
}

// ── the page ──────────────────────────────────────────────────────────────

export default function ChannelSetup() {
  // loading | offline | expired | email | code | linking | card | retry | done
  const [view, setView] = useState('loading')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const [phoneHint, setPhoneHint] = useState('')
  const [booking, setBooking] = useState(null)
  const [cfg, setCfg] = useState(null)
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [intent, setIntent] = useState(null) // { client_secret, publishable_key }
  const [stripeObj, setStripeObj] = useState(null)
  const [result, setResult] = useState(null) // { ok, title, lead }
  // A card already on file (a returning account), and whether posting needs
  // one at all. Unknown → assume it does: offering "skip" to someone with no
  // card only ends on "we need a card" (Aura 2026-10-30: it read as if the
  // booking had gone through without one).
  const [saved, setSaved] = useState(null) // { brand, last4 }
  const [enforced, setEnforced] = useState(true)

  // The token, read before the first paint. A ref, not state: it is not
  // rendered, and it must not change identity across renders.
  const tokenRef = useRef(null)
  if (tokenRef.current === null) tokenRef.current = readToken()
  // The access token this visit minted (verifyOtp). Held here, passed to every
  // authenticated call, never re-read from shared storage.
  const accessRef = useRef('')
  // What the 'retry' screen retries: 'card' (the card step never opened) or
  // 'book' (it did, or was skipped, and the booking failed).
  const [retryKind, setRetryKind] = useState('book')
  // The number is attached. Two things depend on it: a fresh sign-in after an
  // expired token skips the linking and goes straight to the card, and a 410
  // stops meaning "ask for a new link".
  const linkedRef = useRef(false)
  // We have sent at least one link request this visit, so a 410 may mean "the
  // first one landed and we lost the answer" rather than "stale link".
  const attemptedLinkRef = useRef(false)
  const navigate = useNavigate()
  const emailRef = useRef(null)
  const codeRef = useRef(null)

  const step = view === 'done' || view === 'card' || view === 'saved' || view === 'retry' ? 3 : view === 'code' || view === 'linking' ? 2 : 1

  async function load() {
    const token = tokenRef.current
    if (!token) { setView('expired'); return }
    setView('loading')
    const [config, check] = await Promise.all([
      getSetupConfig(),
      checkSetupToken(token),
    ])
    if (linkIsBad(check)) { setView('expired'); return }
    // A 2xx is not enough either: a captive portal or an SPA rewrite answers
    // 200 with something that is not this endpoint's body.
    if (!check.ok || check.data.valid !== true) { setView('offline'); return }
    setCfg(config.data || {})
    setPhoneHint(check.data.phone_hint || 'your phone')
    setBooking(check.data.booking || null)
    setView('email')
    // Prefill, separately and after: a session only ever fills the email in,
    // and getSession can block on the auth lock and a token refresh — which
    // must not hold the whole page on "checking your link" while a 30-minute
    // link runs down.
    supabase.auth.getSession()
      .then((r) => {
        const who = r?.data?.session?.user?.email
        if (who) setEmail((cur) => cur || who)
      })
      .catch((err) => console.error('[channel-setup] prefill from session failed:', err))
  }

  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // history.replaceState does not reach react-router's cached location, which
  // would keep #t=… in memory for the life of the page. Done in an effect
  // because navigate() cannot be called during a render.
  useEffect(() => {
    if (window.location.hash || window.location.search) return
    navigate({ pathname: '/channel/setup', search: '', hash: '' }, { replace: true })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

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
    let err = null
    try {
      // Wrapped because supabase-js awaits every onAuthStateChange subscriber
      // inside these calls: a listener that throws would otherwise leave this
      // button disabled for good, with no way off the screen.
      ;({ error: err } = await supabase.auth.signInWithOtp({
        email: addr,
        options: { shouldCreateUser: true },
      }))
    } catch (thrown) {
      console.error('[channel-setup] signInWithOtp threw:', thrown)
      setBusy(false)
      setError("We couldn't send the code. Please try again.")
      return
    }
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
    let data = null
    let err = null
    try {
      ;({ data, error: err } = await supabase.auth.verifyOtp({ email, token: digits, type: 'email' }))
    } catch (thrown) {
      console.error('[channel-setup] verifyOtp threw:', thrown)
      setBusy(false)
      setError("We couldn't check that code. Please try again.")
      return
    }
    if (err || !data?.session?.access_token) {
      setBusy(false)
      setError("That code didn't work. Check the latest email, or go back and resend.")
      return
    }
    // The code is spent from here on, so this view can never be returned to:
    // its button would re-submit a used code and send the person to ask for
    // an email they do not need.
    accessRef.current = data.session.access_token
    // Signing in again after an expired token: the number is already attached
    // and the setup token is gone, so the linking is behind us.
    if (linkedRef.current) {
      setView('retry')
      await openCard()
      return
    }
    setView('linking')
    await linkThenCard()
  }

  // The token this page is holding is no longer accepted. Proving the mailbox
  // again is the only fix; the email is known, so it is one code away. Where
  // that lands afterwards depends on whether the number is already attached
  // (verifyCode decides), so nothing else is reset here.
  function signInAgain() {
    accessRef.current = ''
    setCode('')
    setBusy(false)
    setError(
      linkedRef.current
        ? 'Your sign-in expired before we could finish. Your number is connected — confirm your email once more and we will place the booking.'
        : 'Your sign-in expired. Send yourself a new code and we will carry on.',
    )
    setView('email')
  }

  // Spend the setup token: attach the number to the account whose mailbox was
  // just proved. The only place it is spent, and the last place it is used.
  // Reachable only from the 'linking' view, whose own button calls this again
  // — so a failure that did NOT spend the token retries the link, and never
  // the one-time code behind it.
  async function linkThenCard() {
    setError('')
    setBusy(true)
    const retrying = attemptedLinkRef.current
    attemptedLinkRef.current = true
    const res = await linkSetupNumber(tokenRef.current, accessRef.current)
    if (isUnauthorized(res)) { signInAgain(); return }
    if (res.status === 410) {
      setBusy(false)
      forgetToken()
      tokenRef.current = ''
      // A 410 on a RETRY can also mean the first request landed and we lost
      // the answer: the number would then be connected, and "ask for a fresh
      // link" would be the wrong instruction.
      setView(retrying ? 'maybe_linked' : 'expired')
      return
    }
    if (res.status === 409) {
      // The token is NOT spent — the number belongs to another account, and
      // proving THAT account's mailbox is the way through. Back to the email
      // step, where a fresh code is typed for it.
      setBusy(false)
      setError(
        'This phone number is already connected to a different HO:RA account. ' +
        'Use that account\u2019s email, or write to info@my-hora.com.',
      )
      accessRef.current = ''
      setCode('')
      setView('email')
      return
    }
    if (!res.ok || res.data.linked !== true) {
      setBusy(false)
      setError("We couldn't connect your number. Check your connection and try again.")
      return
    }
    linkedRef.current = true
    forgetToken()
    tokenRef.current = ''
    if (res.data.phone_hint) setPhoneHint(res.data.phone_hint)
    await openCard()
  }

  // The card step. A 503 means payments are not configured here (staging):
  // book straight away, and if the server does need a card, finish() says so.
  // Any OTHER failure is not a reason to skip a card silently — it is a retry
  // of the card step, which is what the 'retry' screen then offers.
  async function openCard(opts) {
    setError('')
    setBusy(true)
    if (!opts?.fresh) {
      // A returning account may already have a card: say which one, and
      // book with it, instead of a card form with a "skip" that silently
      // uses it. Any failure here falls through to the card form.
      const pm = await listSavedCards(accessRef.current)
      if (isUnauthorized(pm)) { signInAgain(); return }
      if (pm.status === 503) { await book(); return }
      if (pm.ok) {
        setEnforced(pm.data.payments_enforced !== false)
        const cards = Array.isArray(pm.data.cards) ? pm.data.cards : []
        const card = cards.find((c) => c.is_default) || cards[0]
        if (card && card.last4) {
          setSaved({ brand: card.brand || 'card', last4: card.last4 })
          setBusy(false)
          setView('saved')
          return
        }
        setSaved(null)
      }
    }
    const si = await createSetupIntent(accessRef.current)
    if (isUnauthorized(si)) { signInAgain(); return }
    if (si.status === 503) { await book(); return }
    if (!si.ok || !si.data.client_secret || !si.data.publishable_key) {
      setBusy(false)
      setError("Your number is connected, but we couldn't open the card form.")
      setRetryKind('card')
      setView('retry')
      return
    }
    const stripe = await stripeFor(si.data.publishable_key).catch((err) => {
      console.error('[channel-setup] Stripe.js did not load:', err)
      return null
    })
    if (!stripe) {
      setBusy(false)
      setError("Your number is connected, but we couldn't load the card form. Check your connection.")
      setRetryKind('card')
      setView('retry')
      return
    }
    setStripeObj(stripe)
    setIntent({ client_secret: si.data.client_secret, publishable_key: si.data.publishable_key })
    setBusy(false)
    setView('card')
  }

  // Book what the chat already priced. Reachable only after the link
  // succeeded, so a failure here never sends anyone back to a spent token.
  async function book() {
    setError('')
    setBusy(true)
    const res = await finishSetup(accessRef.current)
    if (isUnauthorized(res)) { signInAgain(); return }
    setBusy(false)
    if (!res.ok || typeof res.data.outcome !== 'string') {
      setError("We couldn't finish booking. Please try again.")
      setRetryKind('book')
      setView('retry')
      return
    }
    const outcome = res.data.outcome
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

  const elementsOptions = useMemo(
    () => (intent ? { clientSecret: intent.client_secret, appearance: ELEMENTS_APPEARANCE } : null),
    [intent],
  )

  // What the old page had in its <head> and an SPA route does not: a title
  // and noindex. The link is private and one-use; it must not be indexed if
  // it is ever pasted somewhere a crawler reads.
  useEffect(() => {
    const prevTitle = document.title
    document.title = 'Book with HO:RA'
    const meta = document.createElement('meta')
    meta.name = 'robots'
    meta.content = 'noindex, nofollow'
    document.head.appendChild(meta)
    return () => { document.title = prevTitle; meta.remove() }
  }, [])

  const showTerms = view === 'email' || view === 'code' || view === 'linking' || view === 'card' || view === 'saved'

  return (
    <main className="min-h-screen bg-[#FAFAF7] text-[#222831]">
      <div className="mx-auto w-full max-w-[440px] px-5 pb-10 pt-7">
        <div className="mb-7 text-[22px] font-semibold tracking-[0.02em] text-[#3A5A2D]">
          HO<b className="text-[#E1B145]">:</b>RA
        </div>

        {view !== 'expired' && view !== 'offline' && view !== 'maybe_linked' && view !== 'loading' && <Steps step={step} />}
        <ErrorNote>{error}</ErrorNote>

        {view === 'loading' && <p className="text-[#5F6670]">Checking your link…</p>}

        {view === 'offline' && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">We couldn't reach HO:RA</h1>
            <p className="mb-6 text-[#5F6670]">
              Your link has not been used — we just couldn't get an answer. Check your connection and
              try again.
            </p>
            <button className={BTN} onClick={load}>Try again</button>
            <p className="mt-4 text-[13px] text-[#5F6670]">
              If it keeps failing, text HO:RA and we'll sort the booking out there.
            </p>
          </>
        )}

        {view === 'expired' && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">This link has expired</h1>
            <p className="text-[#5F6670]">
              Booking links work once, for 30 minutes. Text HO:RA again and we'll send you a fresh one.
            </p>
          </>
        )}

        {(view === 'email' || view === 'code') && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">
              {view === 'code' ? 'Enter the code' : 'One step to book'}
            </h1>

            {booking && view === 'email' && (
              <div className="mb-5 rounded-[10px] border border-[#E5E7EB] bg-white px-3.5 py-3">
                <b className="block font-semibold">{booking.title}</b>
                <span className="text-sm text-[#5F6670]">
                  Estimate {booking.estimate} · you pay for the time actually used
                </span>
              </div>
            )}

            {view === 'email' ? (
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
            ) : (
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

        {view === 'card' && elementsOptions && stripeObj && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">{saved ? 'Use a different card' : 'Add a card'}</h1>
            <p className="mb-6 text-[#5F6670]">
              Nothing is charged now. We reserve the estimate when you book, and you pay for the time
              actually used.
            </p>
            <Elements stripe={stripeObj} options={elementsOptions}>
              <CardForm
                email={email}
                busy={busy}
                onDone={book}
                onSkip={book}
                skipLabel={saved ? `Use my ${brandName(saved.brand)} ending in ${saved.last4}` : enforced ? null : 'Skip for now'}
              />
            </Elements>
          </>
        )}

        {view === 'saved' && saved && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">Book with your card</h1>
            <p className="mb-6 text-[#5F6670]">
              {`We'll use your ${brandName(saved.brand)} ending in ${saved.last4}. Nothing is charged now. We reserve the estimate when you book, and you pay for the time actually used.`}
            </p>
            <ErrorNote>{error}</ErrorNote>
            <button className={BTN} disabled={busy} onClick={book}>
              {busy ? 'Booking…' : `Book with •••• ${saved.last4}`}
            </button>
            <button className={LINK_BTN} disabled={busy} onClick={() => openCard({ fresh: true })}>
              Use a different card
            </button>
          </>
        )}

        {view === 'linking' && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">
              {error ? 'Almost connected' : 'Connecting your number…'}
            </h1>
            <p className="mb-6 text-[#5F6670]">
              {error
                ? `Your email is confirmed. We still have to connect texts from ${phoneHint} to it — no need to type the code again.`
                : `Confirming ${email} and connecting texts from ${phoneHint}.`}
            </p>
            {error && (
              <button className={BTN} disabled={busy} onClick={linkThenCard}>
                {busy ? 'One moment…' : 'Try again'}
              </button>
            )}
          </>
        )}

        {view === 'maybe_linked' && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">Check Messages</h1>
            <p className="text-[#5F6670]">
              This link has been used — most likely by the attempt we just made, which means texts
              from {phoneHint} already reach your HO:RA account. Go back to Messages and text HO:RA:
              it will tell you where the booking stands, and asking for a new link is not what you
              need.
            </p>
          </>
        )}

        {view === 'retry' && (
          <>
            <h1 className="mb-2 text-2xl font-semibold leading-tight">Your number is connected</h1>
            <p className="mb-6 text-[#5F6670]">
              Texts from {phoneHint} now reach your HO:RA account.{' '}
              {retryKind === 'card'
                ? 'One thing left: the card, and then the booking you said yes to.'
                : 'One thing left: placing the booking you said yes to.'}
            </p>
            <button
              className={BTN}
              disabled={busy}
              onClick={retryKind === 'card' ? openCard : book}
            >
              {busy ? 'One moment…' : 'Try again'}
            </button>
            {retryKind === 'card' && (saved || !enforced) && (
              <button className={LINK_BTN} disabled={busy} onClick={book}>
                {saved ? `Book with my card ending in ${saved.last4}` : 'Book without a card'}
              </button>
            )}
            <p className="mt-4 text-[13px] text-[#5F6670]">
              If this keeps failing, go back to Messages and text HO:RA — nothing is lost, and your
              number stays connected.
            </p>
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

        {showTerms && (
          <p className="mt-5 text-[13px] text-[#5F6670]">
            By booking you agree to our{' '}
            <a className="text-[#222831] underline" href={cfg?.terms_url || 'https://www.my-hora.com/terms'} target="_blank" rel="noreferrer">Terms</a>
            {' '}and{' '}
            <a className="text-[#222831] underline" href={cfg?.privacy_url || 'https://www.my-hora.com/privacy'} target="_blank" rel="noreferrer">Privacy Policy</a>.
          </p>
        )}
      </div>
    </main>
  )
}
