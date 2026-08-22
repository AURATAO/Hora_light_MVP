// Runs on node's built-in test runner, no dependencies:  npm test
//
// Covers the two things a bad port would get wrong silently: the date gate
// (wrong timezone → the round opens or closes a day off) and the payload the
// questionnaire sends (wrong slug → a CHECK constraint rejects it, wrong shape
// → the wrong columns are written).
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { TRACTION_3_CONFIG, isTractionWindowActive } from './traction.js'
import {
  EASE_OPTIONS,
  USE_AGAIN_OPTIONS,
  TRACTION_QUESTIONS,
  buildTractionReviewPayload,
  isTractionReviewComplete,
} from './tractionReview.js'

const at = iso => new Date(iso)

test('window opens Aug 18 and closes after Aug 28, on NYC day boundaries', () => {
  // 03:59Z is 23:59 EDT on Aug 17 — still closed.
  assert.equal(isTractionWindowActive(at('2026-08-18T03:59:59Z')), false)
  assert.equal(isTractionWindowActive(at('2026-08-18T04:00:00Z')), true)
  assert.equal(isTractionWindowActive(at('2026-08-18T00:00:00-04:00')), true)
  // Aug 28 counts in full: endsAt is the first instant after it.
  assert.equal(isTractionWindowActive(at('2026-08-28T23:59:59-04:00')), true)
  assert.equal(isTractionWindowActive(at('2026-08-29T00:00:00-04:00')), false)
  assert.equal(isTractionWindowActive(at('2026-08-29T03:59:59Z')), true)
  assert.equal(isTractionWindowActive(at('2026-08-29T04:00:00Z')), false)
  // Long before and long after.
  assert.equal(isTractionWindowActive(at('2026-07-01T12:00:00Z')), false)
  assert.equal(isTractionWindowActive(at('2026-09-15T12:00:00Z')), false)
})

test('answer slugs are exactly the ones the server and the CHECK constraints accept', () => {
  assert.deepEqual(
    EASE_OPTIONS.map(o => o.value),
    ['very_easy', 'easy', 'neutral', 'difficult', 'very_difficult']
  )
  assert.deepEqual(
    USE_AGAIN_OPTIONS.map(o => o.value),
    ['yes', 'maybe_task', 'maybe_cost', 'no']
  )
})

test('requester submit is gated on ease + stars + would-use-again', () => {
  const full = { ease: 'easy', stars: 4, useAgain: 'yes', openFeedback: '' }
  assert.equal(isTractionReviewComplete('requester', full), true)
  assert.equal(isTractionReviewComplete('requester', { ...full, ease: '' }), false)
  assert.equal(isTractionReviewComplete('requester', { ...full, stars: 0 }), false)
  assert.equal(isTractionReviewComplete('requester', { ...full, useAgain: '' }), false)
  // The open question is optional.
  assert.equal(isTractionReviewComplete('requester', { ...full, openFeedback: '' }), true)
})

test('supporter submit is gated on ease + would-use-again, never on stars', () => {
  const full = { ease: 'neutral', stars: 0, useAgain: 'maybe_task', openFeedback: '' }
  assert.equal(isTractionReviewComplete('supporter', full), true)
  assert.equal(isTractionReviewComplete('supporter', { ...full, ease: '' }), false)
  assert.equal(isTractionReviewComplete('supporter', { ...full, useAgain: '' }), false)
})

test('requester payload carries stars, supporter payload carries none', () => {
  const answers = { ease: 'very_easy', stars: 5, useAgain: 'yes', openFeedback: ' faster matching ' }
  assert.deepEqual(buildTractionReviewPayload('requester', answers), {
    ease_rating: 'very_easy',
    would_use_again: 'yes',
    stars: 5,
    open_feedback: 'faster matching',
  })
  const supporter = buildTractionReviewPayload('supporter', answers)
  assert.equal('stars' in supporter, false)
  assert.deepEqual(supporter, {
    ease_rating: 'very_easy',
    would_use_again: 'yes',
    open_feedback: 'faster matching',
  })
})

test('blank open feedback is omitted rather than sent empty', () => {
  const body = buildTractionReviewPayload('supporter', {
    ease: 'easy', stars: 0, useAgain: 'no', openFeedback: '   ',
  })
  assert.equal('open_feedback' in body, false)
})

// The round's dates and questions are duplicated between web and mobile on
// purpose (no shared package). This is the guard on that duplication: it reads
// the mobile files and fails if the two ever disagree. Skipped, not failed,
// when mobile isn't checked out beside app/.
test('web copy still matches mobile, the source of truth', t => {
  const root = fileURLToPath(new URL('../../../mobile/src/', import.meta.url))
  let sheet, betaNotice
  try {
    sheet = readFileSync(root + 'components/TractionReviewSheet.tsx', 'utf8')
    betaNotice = readFileSync(root + 'lib/beta-notice.ts', 'utf8')
  } catch {
    t.skip('mobile/ not present')
    return
  }

  for (const key of ['startsAt', 'endsAt']) {
    assert.ok(
      betaNotice.includes(`${key}: "${TRACTION_3_CONFIG[key]}"`),
      `${key} differs from mobile — the round would open or close on different days`
    )
  }
  assert.ok(betaNotice.includes(`perMinuteRate: "${TRACTION_3_CONFIG.perMinuteRate}"`))

  for (const opt of [...EASE_OPTIONS, ...USE_AGAIN_OPTIONS]) {
    assert.ok(sheet.includes(`"${opt.value}", label: "${opt.label}"`), `option drift: ${opt.value}`)
  }

  // Question wording, with the rate interpolated on both sides.
  const rate = TRACTION_3_CONFIG.perMinuteRate
  const mobileText = sheet
    .replace(/\$\{rate\}/g, rate)
    .replace(/\s*\n\s*/g, ' ')          // mobile wraps some strings across lines
    .replace(/" \+ "/g, '')             // ...and concatenates the pieces
  for (const role of ['requester', 'supporter']) {
    for (const q of ['ease', 'useAgain', 'open']) {
      assert.ok(
        mobileText.includes(TRACTION_QUESTIONS[role][q]),
        `question drift: ${role}.${q}`
      )
    }
  }
  assert.ok(sheet.includes(TRACTION_QUESTIONS.requester.stars))
})
