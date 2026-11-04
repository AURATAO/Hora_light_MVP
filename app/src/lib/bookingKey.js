// The new-task form's booking key (server/task_booking_key.go). One per
// form, sent with every POST /tasks from it, so a resubmit after an
// uncertain Stripe failure (a timeout) finds the task the first attempt made
// and repeats its hold call: one task, one hold — never a second.

/** A fresh key for a new form. */
export function newBookingKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
}

/** The hold's outcome is unknown; resubmitting the same form is safe. */
export function isUncertainPost(err) {
  return err?.status === 502 && err?.body?.error === 'payments_uncertain'
}

export const UNCERTAIN_POST_MESSAGE = 'Something went wrong, please try again.'

/**
 * The key the form's next submit sends. Kept across every failure, so a
 * retry is the same booking; replaced once the post is settled — posted, or
 * handed to a bank challenge (that task is answered by its own confirm, and
 * a resubmit would otherwise find it waiting on a challenge it can't run).
 */
export function keyAfterPost(key, outcome) {
  return outcome === 'posted' || outcome === 'authenticate' ? newBookingKey() : key
}
