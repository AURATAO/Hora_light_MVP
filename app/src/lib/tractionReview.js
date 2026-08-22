// The Traction 3 questionnaire's content and rules, with no React in sight, so
// the wording, the slugs, the submit gate and the payload shape can be tested
// as plain values (see traction.test.mjs).
//
// Mirrors mobile/src/components/TractionReviewSheet.tsx word for word — that
// file is the source of truth. Both platforms must ask the same questions and
// send the same slugs, or the round's answers can't be read as one dataset.
// EDIT BOTH when the copy changes.
// Explicit extension: this module is also imported by plain node in
// traction.test.mjs, where extensionless relative imports don't resolve.
import { TRACTION_3_CONFIG } from './traction.js'

// Labels live here and only here — the payload carries slugs, so re-wording a
// question never touches stored data (server/main.go validEaseRatings et al.).
export const EASE_OPTIONS = [
  { value: 'very_easy',      label: 'Very easy' },
  { value: 'easy',           label: 'Easy' },
  { value: 'neutral',        label: 'Neither easy nor difficult' },
  { value: 'difficult',      label: 'Difficult' },
  { value: 'very_difficult', label: 'Very difficult' },
]

export const USE_AGAIN_OPTIONS = [
  { value: 'yes',         label: 'Yes' },
  { value: 'maybe_task',  label: 'Maybe, depending on the task' },
  { value: 'maybe_cost',  label: 'Maybe, depending on the final cost' },
  { value: 'no',          label: 'No' },
]

const rate = TRACTION_3_CONFIG.perMinuteRate

// Both roles answer the same two multiple-choice questions with the same slugs;
// only the wording differs, because "the flow" means something different from
// each side of a task.
export const TRACTION_QUESTIONS = {
  requester: {
    ease: 'How easy was it to create, follow and complete this mission through HO:RA?',
    stars: 'Rate your supporter',
    useAgain: `HO:RA — ${rate} per minute. Would you use it for a local errand?`,
    open: 'What is the one thing we should improve before the public launch?',
  },
  supporter: {
    ease: 'How clear and easy was the complete mission flow, from acceptance to clock-out?',
    useAgain: `At ${rate}/min, would you take on tasks like this again as a supporter?`,
    open:
      'What is the one operational or app-related improvement that would help you ' +
      'complete future missions more efficiently?',
  },
}

/**
 * Everything except the open question is required, which is what keeps the CTA
 * disabled rather than validating on submit. Only the requester form has stars.
 */
export function isTractionReviewComplete(role, { ease, stars, useAgain }) {
  if (!ease || !useAgain) return false
  return role !== 'requester' || stars > 0
}

/**
 * The POST /tasks/:id/review body. `stars` is omitted entirely for a supporter:
 * their questionnaire rates nobody, and the server stores stars and ratee null
 * for that side regardless of what arrives.
 */
export function buildTractionReviewPayload(role, { ease, stars, useAgain, openFeedback }) {
  const body = {
    ease_rating: ease,
    would_use_again: useAgain,
  }
  if (role === 'requester') body.stars = stars
  const trimmed = (openFeedback || '').trim()
  if (trimmed) body.open_feedback = trimmed
  return body
}
