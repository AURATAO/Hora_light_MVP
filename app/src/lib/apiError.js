// What a failed API call says to the person looking at it.
//
// The Go backend answers a failure with { error, message? }: `message` is a
// sentence written for the user ("You have an outstanding balance of $12.00
// …"), `error` is either a short phrase ("already clocked in") or a code for
// the client to branch on ("payouts_onboarding_required"). api() used to
// throw Error("HTTP 403") for all of them, and every `toast(e.message)` in the
// app showed exactly that.

const GENERIC_SERVER = 'Something went wrong on our side. Please try again.'

/** A code is one token: no spaces. "outstanding_balance" is for branching on,
 *  never for showing. */
function isCode(text) {
  return !/\s/.test(text)
}

/**
 * The message for a failed response, in order of preference:
 *   1. the server's `message` sentence;
 *   2. its `error`, when that is a phrase rather than a code — capitalised,
 *      and never for a 5xx ("db error" explains nothing to a user);
 *   3. a generic line that still carries the status for a bug report.
 */
export function apiErrorMessage(status, body) {
  const b = body && typeof body === 'object' ? body : {}
  if (typeof b.message === 'string' && b.message.trim()) return b.message.trim()
  if (status >= 500) return GENERIC_SERVER
  if (typeof b.error === 'string' && b.error.trim() && !isCode(b.error.trim())) {
    const phrase = b.error.trim()
    return phrase.charAt(0).toUpperCase() + phrase.slice(1)
  }
  return `That didn't work (HTTP ${status}). Please try again.`
}

/** The server's `error` code or phrase, for callers that branch on it. */
export function apiErrorCode(body) {
  return body && typeof body === 'object' && typeof body.error === 'string' ? body.error : undefined
}
