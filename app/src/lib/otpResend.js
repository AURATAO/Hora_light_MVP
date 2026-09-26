/**
 * OTP resend cooldown — the one number both login screens agree on.
 *
 * Supabase rate-limits OTP sends per address; a "Resend" that fires on every
 * tap gets the address throttled and the user told nothing useful. Thirty
 * seconds is long enough for the first email to arrive and short enough that
 * a genuinely lost one is not a wait. Mirrors mobile/src/lib/otp-resend.ts.
 */
export const RESEND_COOLDOWN_MS = 30_000

/** Whole seconds until a resend is allowed; 0 once it is. */
export function resendSecondsLeft(readyAt, now = Date.now()) {
  if (!readyAt) return 0
  return Math.max(0, Math.ceil((readyAt - now) / 1000))
}

/** The button's label: a countdown while waiting, the action once ready. */
export function resendLabel(readyAt, now = Date.now()) {
  const left = resendSecondsLeft(readyAt, now)
  return left > 0 ? `Resend in ${left}s` : 'Resend code'
}
