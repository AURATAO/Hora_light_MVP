/** Whether a category is companionship. Both spellings are live: the home
 *  grid and the AI parser say "companionship", Post Task's own selector posts
 *  "companion", and the server bills either at the companionship base fee
 *  (server/billing.go isCompanionship). */
export function isCompanionCategory(category) {
  return category === 'companionship' || category === 'companion'
}

/**
 * Whether the companionship policy still has to be accepted before a task in
 * `category` may be submitted.
 *
 * KEYED ON THE CATEGORY, not on how the form came to hold it. The gate used
 * to be opened by the two paths that knew about it — the ?category= link and
 * the Companion button — so a third path, the AI box prefilling
 * "companionship", posted with no acknowledgement at all.
 *
 * `preAcknowledged` is for editing a task that was already posted as
 * companionship: it was accepted then, and re-asking on every edit is noise.
 */
export function needsCompanionPolicy(category, { acknowledged = false, preAcknowledged = false } = {}) {
  return isCompanionCategory(category) && !acknowledged && !preAcknowledged
}
