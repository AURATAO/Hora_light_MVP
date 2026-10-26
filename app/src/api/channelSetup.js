// The four backend calls the chat-booking setup page makes
// (server/channel_setup.go). D-16 R-4: the page itself used to be HTML the Go
// binary served; it now lives in this app, so these go cross-origin to the API
// like every other call here.
//
// Auth differs per call and that is the whole security model (S-11):
//
//   config, check   PUBLIC. `check` is authenticated by the SETUP TOKEN in the
//                   body — the proof that this person can read messages sent to
//                   that number — and by nothing else.
//   link            BOTH: the Supabase session (who) AND the token (which
//                   number). The server reads the identity from the session,
//                   never from the body.
//   finish          the session alone; it books what the chat already priced.
//
// The token is never put in a query string or a header: it stays in the POST
// body, and it reaches this app only through the URL fragment (ChannelSetup.jsx).
import { supabase } from '../lib/supabaseClient'

const API = (
  import.meta.env?.VITE_API_BASE_URL ??
  import.meta.env?.VITE_API_BASE ??
  ''
).replace(/\/+$/, '')

async function call(path, { method = 'GET', body, auth = false } = {}) {
  const headers = { 'Content-Type': 'application/json' }
  if (auth) {
    const { data: { session } } = await supabase.auth.getSession()
    if (session?.access_token) headers.Authorization = `Bearer ${session.access_token}`
  }
  const res = await fetch(`${API}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
    credentials: 'include',
    cache: 'no-store',
  })
  const text = await res.text().catch(() => '')
  let data = null
  try { data = text ? JSON.parse(text) : null } catch { data = null }
  return { ok: res.ok, status: res.status, data: data || {} }
}

// GET /channel/setup/config — the public values the page needs (terms and
// privacy URLs). The Supabase credentials in this response are for the
// server-served page; this app has its own client.
export function getSetupConfig() {
  return call('/channel/setup/config')
}

// POST /channel/setup/check — is this link still good, whose number is it (last
// four only), and what is about to be booked.
export function checkSetupToken(token) {
  return call('/channel/setup/check', { method: 'POST', body: { token } })
}

// POST /channel/setup/link — attach the number to the signed-in account.
// 410 = link expired or used; 409 = the number belongs to another account.
export function linkSetupNumber(token) {
  return call('/channel/setup/link', { method: 'POST', body: { token }, auth: true })
}

// POST /payments/setup-intent — a SetupIntent to save a card against.
// 503 when payments are not configured (staging): the page books anyway.
export function createSetupIntent() {
  return call('/payments/setup-intent', { method: 'POST', auth: true })
}

// POST /channel/setup/finish — book what they said YES to in the chat. The
// confirmation goes to Messages, not here.
export function finishSetup() {
  return call('/channel/setup/finish', { method: 'POST', auth: true })
}
