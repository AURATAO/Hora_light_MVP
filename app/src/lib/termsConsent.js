// The sign-in consent checkbox, recorded server-side (profiles.terms_accepted_at).
//
// Both sign-in paths are gated on the checkbox, but Google sign-in completes
// server-side (/auth/login → /auth/callback) with no client hook after it, so
// the answer is parked here when the box is ticked and sent once by the first
// authenticated page (ProtectLayout). The server keeps the FIRST acceptance.
// Best-effort throughout: storage can be unavailable, and a failed write must
// never keep anybody out of the app.

const KEY = 'hora_terms_accepted_pending'

export function markTermsConsent(agreed) {
  try {
    if (agreed) window.localStorage.setItem(KEY, '1')
    else window.localStorage.removeItem(KEY)
  } catch {
    /* storage unavailable — nothing to record */
  }
}

export async function flushTermsConsent(api) {
  let pending = false
  try {
    pending = window.localStorage.getItem(KEY) === '1'
  } catch {
    return
  }
  if (!pending) return
  try {
    await api('/profile', { method: 'PATCH', body: { terms_accepted: true } })
    window.localStorage.removeItem(KEY)
  } catch {
    /* retried on the next authenticated load */
  }
}
