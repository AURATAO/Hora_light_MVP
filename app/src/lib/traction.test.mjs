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

import {
  BETA_NOTICE_COPY,
  PRICING_LINE,
  SERVICE_AREA,
  TRACTION_3_CONFIG,
  betaSettlementLine,
  isTractionWindowActive,
} from './traction.js'
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
  // The string that actually describes what a task costs. Drift here means the
  // two platforms quote different prices in the same questionnaire.
  assert.ok(
    betaNotice.includes(`pricingSummary: "${TRACTION_3_CONFIG.pricingSummary}"`),
    'pricingSummary differs from mobile — the platforms would quote different prices'
  )

  for (const opt of [...EASE_OPTIONS, ...USE_AGAIN_OPTIONS]) {
    assert.ok(sheet.includes(`"${opt.value}", label: "${opt.label}"`), `option drift: ${opt.value}`)
  }

  // Question wording, with the rate interpolated on both sides.
  const pricing = TRACTION_3_CONFIG.pricingSummary
  const mobileText = sheet
    .replace(/\$\{pricing\}/g, pricing)
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

// ── The beta notice ────────────────────────────────────────────────────────
//
// The notice is a plain welcome now. It carried the Traction 3 round for two
// months — dates, coverage hours, a per-day task limit, a purchase cap — and
// every one of those was a thing that silently went stale when the round ended
// and nobody edited it. What replaces them is content with no expiry date.

test('the pricing line is the whole schedule, in one sentence', () => {
  assert.equal(
    PRICING_LINE,
    '$12 base fee includes the first 15 minutes, then $0.50/min ' +
      '($25 base for companionship; $1.00/min for tasks starting 9 PM–9 AM)'
  )
  // Every number in it, against server/billing.go's BillingConfig. A schedule
  // change that misses this string quotes the old price to the person being
  // charged — which is the drift S-05 exists to prevent, on the one surface
  // where the figure is copy rather than a server quote.
  assert.ok(PRICING_LINE.includes('$12'), 'BaseFeeDefaultCents 1200')
  assert.ok(PRICING_LINE.includes('first 15 minutes'), 'IncludedMinutes 15')
  assert.ok(PRICING_LINE.includes('$0.50/min'), 'PerMinuteRateCents 50')
  assert.ok(PRICING_LINE.includes('$25 base for companionship'), 'BaseFeeCompanionshipCents 2500')
  assert.ok(PRICING_LINE.includes('$1.00/min'), 'SurgeRateCentsPerMin 100')
  assert.ok(PRICING_LINE.includes('9 PM–9 AM'), 'SurgeStartHour 21 / SurgeEndHour 9 — the window wraps midnight')
})

test('the $30 purchase cap is gone from every word of the notice', () => {
  // It was the last bullet for two months and the backend stopped enforcing it
  // long before that — the whole budget is held on the card at post now, and
  // what replaced the cap is an advisory warning at a much higher number.
  const everything = [
    ...BETA_NOTICE_COPY.intro,
    ...BETA_NOTICE_COPY.points,
    BETA_NOTICE_COPY.heading,
    BETA_NOTICE_COPY.finePrint,
    BETA_NOTICE_COPY.acknowledgement,
  ].join(' ')
  assert.ok(!/\$30\b/.test(everything), 'the $30 cap is still being promised')
  assert.ok(!/purchase cap/i.test(everything))
  assert.ok(!/maximum.{0,20}budget/i.test(everything))
})

test('no Traction-3 window copy survives in the notice', () => {
  const everything = [
    ...BETA_NOTICE_COPY.intro,
    ...BETA_NOTICE_COPY.points,
    BETA_NOTICE_COPY.heading,
    BETA_NOTICE_COPY.finePrint,
  ].join(' ')
  // The round's own strings, and the shapes of the claims that went stale with
  // it. A notice that names a date needs editing when the date passes; the
  // whole point of the refresh is that this one does not.
  assert.ok(!everything.includes(TRACTION_3_CONFIG.window), 'the round window is still quoted')
  assert.ok(!/Aug|Sept|202\d/.test(everything), 'a date survives in the notice')
  assert.ok(!/coverage hours/i.test(everything))
  assert.ok(!/tasks per day/i.test(everything))
  assert.ok(!/this round/i.test(everything))
  assert.ok(!/11am|3pm|2pm|10pm/i.test(everything), 'a coverage window survives')
})

test('the notice says what HO:RA is, where it runs, and what it costs', () => {
  assert.equal(SERVICE_AREA, 'New York City')
  const intro = BETA_NOTICE_COPY.intro.join(' ')
  assert.ok(intro.includes('minute-billing'), 'what it is')
  assert.ok(intro.includes(SERVICE_AREA), 'where it runs')
  assert.ok(BETA_NOTICE_COPY.points.includes(PRICING_LINE), 'what it costs')
  // And the beta expectation, which is the one thing the acknowledgement is
  // actually an acknowledgement OF.
  assert.ok(/early-stage beta/i.test(BETA_NOTICE_COPY.points.join(' ')))
})

// ── The settlement line, and the sentence that must not leak ───────────────

test('the settlement line follows the payments flag', () => {
  assert.equal(
    betaSettlementLine(false),
    'During beta, settle purchases directly with your supporter.'
  )
  assert.equal(
    betaSettlementLine(true),
    'Payments and purchase reimbursements are handled securely in the app.'
  )
})

test('the off-platform wording NEVER appears when payments are enforced', () => {
  // The App Store rule this exists for: a sentence telling users to pay
  // outside the app, shown by an app that takes payment inside it, reads as
  // steering. The guard is not "we pass the right argument at the call sites"
  // — it is that this function cannot produce that sentence for any input
  // except a definite false.
  const offPlatform = betaSettlementLine(false)
  assert.ok(/directly with your supporter/.test(offPlatform))

  for (const enforcedish of [true, 1, 'true', 'yes', {}, [], 'enforced']) {
    const line = betaSettlementLine(enforcedish)
    assert.notEqual(line, offPlatform, `${JSON.stringify(enforcedish)} produced the off-platform line`)
  }
})

test('an unknown flag prints NOTHING rather than guessing', () => {
  // Fail-closed. A missing sentence costs a beta user one question; the wrong
  // sentence costs a release. Every not-yet-known shape resolves to null, so a
  // caller that forgets to distinguish loading from false still cannot show
  // the off-platform wording by accident.
  for (const unknown of [null, undefined, 0, '', Number.NaN]) {
    assert.equal(betaSettlementLine(unknown), null, `${JSON.stringify(unknown)} should be unknown`)
  }
})

test('neither settlement sentence is in the static notice copy', () => {
  // It is flag-dependent, so it must be rendered from betaSettlementLine at
  // the call site — a copy pasted into `points` would be unconditional, which
  // is exactly the bug this whole mechanism prevents.
  const everything = [...BETA_NOTICE_COPY.intro, ...BETA_NOTICE_COPY.points].join(' ')
  assert.ok(!everything.includes('directly with your supporter'))
  assert.ok(!everything.includes('handled securely in the app'))
})

test('the notice copy matches mobile, word for word', t => {
  const path = fileURLToPath(new URL('../../../mobile/src/lib/beta-notice.ts', import.meta.url))
  let src
  try {
    src = readFileSync(path, 'utf8')
  } catch {
    t.skip('mobile/ not present')
    return
  }

  // Every user-visible string in the notice, byte for byte. The two platforms
  // show the same terms to the same people; a difference here is two different
  // sets of terms accepted under one checkbox.
  for (const line of [
    BETA_NOTICE_COPY.heading,
    ...BETA_NOTICE_COPY.intro.filter(p => !p.includes(SERVICE_AREA)),
    ...BETA_NOTICE_COPY.points.filter(p => p !== PRICING_LINE),
    BETA_NOTICE_COPY.finePrint,
    BETA_NOTICE_COPY.acknowledgement,
    BETA_NOTICE_COPY.cta,
  ]) {
    assert.ok(src.includes(JSON.stringify(line)), `notice copy drift: ${line}`)
  }
  // The interpolated ones, checked through their pieces.
  assert.ok(src.includes(`SERVICE_AREA = ${JSON.stringify(SERVICE_AREA)}`), 'service area drift')
  assert.ok(src.includes('${SERVICE_AREA}'), 'mobile hardcodes the area instead of interpolating')

  // The price sentence is assembled from two concatenated pieces on both
  // sides (it is too long for one line), so it is compared after joining.
  const joined = src.replace(/"\s*\+\s*\n?\s*"/g, '')
  assert.ok(joined.includes(JSON.stringify(PRICING_LINE)), 'pricing line differs from mobile')

  // Both settlement sentences, so the flag means the same thing on both.
  assert.ok(src.includes(JSON.stringify(betaSettlementLine(true))), 'enforced wording drift')
  assert.ok(src.includes(JSON.stringify(betaSettlementLine(false))), 'beta wording drift')

  // And mobile's notice is free of the round, exactly like web's.
  const mobileNoticeBlock = src.slice(
    src.indexOf('export const BETA_NOTICE_COPY'),
    src.indexOf('Traction 3: the questionnaire')
  )
  assert.ok(!/purchaseCapDollars|coverageHours/.test(mobileNoticeBlock), 'round fields survive in mobile')
  assert.ok(!mobileNoticeBlock.includes(TRACTION_3_CONFIG.window), 'mobile notice still quotes the round')
})

test('the round config no longer carries notice fields', () => {
  // coverageHours, purchaseCapDollars and area belonged to the notice and were
  // living in the round's config, which is how the notice ended up expiring
  // with the round. Only the questionnaire's own values are left.
  for (const gone of ['coverageHours', 'purchaseCapDollars', 'area']) {
    assert.equal(gone in TRACTION_3_CONFIG, false, `${gone} is still on the round config`)
  }
  assert.deepEqual(
    Object.keys(TRACTION_3_CONFIG).sort(),
    ['endsAt', 'perMinuteRate', 'pricingSummary', 'round', 'startsAt', 'window']
  )
})
