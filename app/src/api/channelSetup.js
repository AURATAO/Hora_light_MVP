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
//   link            BOTH: the access token proved THIS VISIT (who) AND the
//                   setup token (which number). The server reads the identity
//                   from the token, never from the body.
//   setup-intent    the access token from this visit.
//   finish          the same; it books what the chat already priced.
//
// WHY THE TOKEN IS PASSED IN AND NO COOKIE IS SENT. `dualAuth` tries the
// `hora_session` cookie FIRST and the Authorization header second, and in
// production that cookie is `SameSite=None`, so it rides along on these
// cross-origin calls. A browser that still holds a cookie for an OLD account
// would therefore decide who the phone number binds to — while this page says
// it is binding the email somebody just proved (review r2). So every
// authenticated call here carries the access token this visit minted, passed
// in explicitly rather than re-read from shared storage (another tab can
// change that between the code and the booking), and sends `credentials:
// 'omit'` so no cookie can outvote it.
//
// The setup token is never put in a query string or a header: it stays in the
// POST body, and it reaches this app only through the URL fragment
// (ChannelSetup.jsx).

const API = (
  import.meta.env?.VITE_API_BASE_URL ??
  import.meta.env?.VITE_API_BASE ??
  ''
).replace(/\/+$/, '')

if (!API && typeof console !== 'undefined') {
  // Without a base these calls hit this app's own origin, where an SPA
  // rewrite answers 200 with HTML — which the page would otherwise have to
  // detect by reading the body.
  console.warn('[channel-setup] VITE_API_BASE_URL is empty — API calls will go to this origin and fail.')
}

async function call(path, { method = 'GET', body, accessToken } = {}) {
  const headers = { 'Content-Type': 'application/json' }
  if (accessToken) headers.Authorization = `Bearer ${accessToken}`
  let res
  try {
    res = await fetch(`${API}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
      // No cookies, in either direction: see the note above.
      credentials: 'omit',
      cache: 'no-store',
    })
  } catch (err) {
    // Status 0 is this page's "we could not reach HO:RA", which is a different
    // screen from "your link is no good" — so the reason is worth a line in
    // the console when someone is looking at a browser that cannot book.
    console.error(`[channel-setup] ${method} ${path} did not complete:`, err)
    return { ok: false, status: 0, data: {} }
  }
  const text = await res.text().catch(() => '')
  let data = null
  try { data = text ? JSON.parse(text) : null } catch { data = null }
  if (!res.ok) console.error(`[channel-setup] ${method} ${path} → ${res.status}`)
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

// POST /channel/setup/link — attach the number to the account whose email was
// just proved. 410 = link expired or used; 409 = the number belongs to another
// account.
export function linkSetupNumber(token, accessToken) {
  return call('/channel/setup/link', { method: 'POST', body: { token }, accessToken })
}

// POST /payments/setup-intent — a SetupIntent to save a card against.
// 503 when payments are not configured (staging): the page books anyway.
export function createSetupIntent(accessToken) {
  return call('/payments/setup-intent', { method: 'POST', accessToken })
}

// POST /channel/setup/finish — book what they said YES to in the chat. The
// confirmation goes to Messages, not here.
export function finishSetup(accessToken) {
  return call('/channel/setup/finish', { method: 'POST', accessToken })
}
