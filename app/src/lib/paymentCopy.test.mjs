import test from 'node:test'
import assert from 'node:assert/strict'
import {
  formatCardLabel,
  highBudgetWarning,
  holdPlacedMessage,
  holdReleasedMessage,
  holdSummary,
  holdWillBeReleasedMessage,
  outstandingBalanceMessage,
  surgeRateNote,
  timeBasisNote,
  capWarningNote,
  cancelChargeLine,
  cancelReleaseLine,
  cancelGraceCountdown,
  extensionResolutionTitle,
  extensionResolutionDetail,
  extensionReaskLabel,
} from './paymentCopy.js'

/**
 * The copy is the feature here, so the copy is what is pinned.
 *
 * A live tester posted a task, was told nothing about their card, assumed the
 * post had failed and cancelled it. The money was correct throughout; the
 * silence was the defect. Two rules came out of it and every test below is one
 * of them:
 *
 *   1. A real number, always. Never a hedge, never "$0.00" standing in for
 *      "we don't know" — where there is no number, there is no message.
 *   2. Never the word "refund". A released authorization is not one; nothing
 *      was taken.
 */

test('a card is named the way a cardholder reads it', () => {
  assert.equal(formatCardLabel({ card_brand: 'visa', card_last4: '4242' }), 'Visa ••4242')
  assert.equal(formatCardLabel({ card_brand: 'mastercard', card_last4: '5100' }), 'Mastercard ••5100')
  assert.equal(formatCardLabel({ card_brand: 'amex', card_last4: '0005' }), 'American Express ••0005')
  // An unrecognised brand degrades to "Card" rather than dropping the whole
  // clause — the last four are the half a cardholder recognises, and a raw
  // Stripe slug is not copy. Same fallback the saved-cards list uses.
  assert.equal(formatCardLabel({ card_brand: 'cartes_bancaires', card_last4: '1111' }), 'Card ••1111')
  assert.equal(formatCardLabel({ card_last4: '4242' }), 'Card ••4242')
})

test('no card, no clause — never "your card (unknown)"', () => {
  for (const payment of [null, undefined, {}, { card_brand: 'visa' }]) {
    assert.equal(formatCardLabel(payment), '')
  }
})

test('the post-success line is the amount and what it is made of', () => {
  const msg = holdPlacedMessage({
    authorized_cents: 4950,
    time_cost_cents: 1950,
    shopping_budget_cents: 3000,
  })
  assert.equal(msg.primary, '$49.50 reserved — $19.50 time + $30.00 budget')
  assert.equal(msg.secondary, "Charged only for what's used. Rest released automatically.")
})

test('no shopping means a single number, not a breakdown with a zero in it', () => {
  const msg = holdPlacedMessage({ authorized_cents: 1950, time_cost_cents: 1950 })
  assert.equal(msg.primary, '$19.50 reserved')
  assert.doesNotMatch(msg.primary, /\$0\.00/)
  assert.doesNotMatch(msg.primary, /budget/)
})

test('a hold the server could not break down still states the total', () => {
  // The server omits the split when it does not reconcile with the authorized
  // amount. A breakdown that fails to add up to the number beside it is worse
  // than no breakdown at all.
  assert.equal(holdPlacedMessage({ authorized_cents: 4950 }).primary, '$49.50 reserved')
})

test('the post-success confirmation does not name a card', () => {
  // Which card it landed on matters when you are looking at a live task and
  // wondering what is held. At the moment of posting, the number is the
  // message — the card stays on the task-detail line.
  const msg = holdPlacedMessage({
    authorized_cents: 4950,
    time_cost_cents: 1950,
    shopping_budget_cents: 3000,
    card_brand: 'visa',
    card_last4: '4242',
  })
  assert.doesNotMatch(`${msg.primary} ${msg.secondary}`, /visa|4242/i)
})

test('the task-detail line DOES name the card', () => {
  assert.equal(
    holdSummary({ authorized_cents: 4950, card_brand: 'visa', card_last4: '4242' }),
    '$49.50 reserved · Visa ••4242'
  )
})

test('no hold means nothing is said about money', () => {
  // Every task in the running beta (PAYMENTS_ENFORCED off) looks like this.
  // Returning null is what stops a surface rendering "$0.00 reserved", which
  // would be a confident lie about somebody's card.
  for (const payment of [null, undefined, {}, { authorized_cents: 0 }]) {
    assert.equal(holdPlacedMessage(payment), null)
    assert.equal(holdSummary(payment), null)
    assert.equal(holdWillBeReleasedMessage(payment), null)
  }
  assert.equal(holdReleasedMessage({ captured_cents: 0, released_cents: 0 }), null)
  assert.equal(holdReleasedMessage({}), null)
  assert.equal(holdReleasedMessage(null), null)
})

test('the evening rate explains itself, in the server’s numbers', () => {
  assert.equal(
    surgeRateNote({ surge_rate: true, per_minute_rate_cents: 100, included_minutes: 15 }),
    'Evening rate: $1.00/min after the first 15 minutes.'
  )
  assert.equal(surgeRateNote({ surge_rate: false, per_minute_rate_cents: 50 }), null)
  assert.equal(surgeRateNote(null), null)
})

test('a large budget warns, at the server’s threshold', () => {
  const quote = { high_budget_warning_cents: 50000 }
  assert.equal(highBudgetWarning(49999, quote), null)
  assert.match(highBudgetWarning(50000, quote), /High budget/)
  assert.match(highBudgetWarning(250000, quote), /reserved on your card/)
  // No threshold from the server means no warning invented locally — the
  // clients must not carry their own copy of it (S-05).
  assert.equal(highBudgetWarning(250000, {}), null)
  assert.equal(highBudgetWarning(0, quote), null)
})

test('an outstanding balance names the amount and where it came from', () => {
  const msg = outstandingBalanceMessage({
    total_cents: 1250,
    task_title: 'Pick up a parcel',
    task_count: 1,
  })
  assert.match(msg, /outstanding balance of \$12\.50/)
  assert.match(msg, /Pick up a parcel/)
  assert.match(msg, /settle it to keep posting/)

  assert.match(
    outstandingBalanceMessage({ total_cents: 3000, task_title: 'Pick up a parcel', task_count: 3 }),
    /and 2 more/
  )

  for (const none of [null, undefined, {}, { total_cents: 0 }]) {
    assert.equal(outstandingBalanceMessage(none), null)
  }
})

test('the estimate and the ceiling read as one line, not two numbers', () => {
  // Consent given at post: the ceiling is the estimate plus the auto-extend
  // window, and the line says so rather than leaving 45 unexplained.
  assert.equal(
    timeBasisNote({ estimate_minutes: 30, auto_extend_minutes: 15, approved_extra_minutes: 0, cap_minutes: 45 }),
    'Estimate: 30 min · up to 45 min with auto-extend'
  )
  // Consent refused: there is no headroom, so there is no second number.
  assert.equal(
    timeBasisNote({ estimate_minutes: 30, auto_extend_minutes: 0, approved_extra_minutes: 0, cap_minutes: 30 }),
    'Estimate: 30 min'
  )
  // An approved extension on top of consent.
  assert.equal(
    timeBasisNote({ estimate_minutes: 30, auto_extend_minutes: 15, approved_extra_minutes: 30, cap_minutes: 75 }),
    'Estimate: 30 min · up to 75 min with auto-extend and approved extensions'
  )
  // An approved extension without consent.
  assert.equal(
    timeBasisNote({ estimate_minutes: 30, auto_extend_minutes: 0, approved_extra_minutes: 30, cap_minutes: 60 }),
    'Estimate: 30 min · up to 60 min with approved extensions'
  )
  // Nothing to describe.
  for (const none of [null, undefined, {}, { estimate_minutes: 0, cap_minutes: 45 }]) {
    assert.equal(timeBasisNote(none), null)
  }
})

test('the early warning names the estimate, not the ceiling above it', () => {
  // THE BUILD 11 FINDING, in copy. With auto-extend on, the warning fires at
  // 25 minutes on a 30-minute task with 20 minutes of ceiling left. "About 20
  // min left" is true of the ceiling and useless as a warning.
  assert.equal(
    capWarningNote({
      warning: true,
      reached: false,
      remaining_minutes: 20,
      cap: { estimate_minutes: 30, agreed_minutes: 30, auto_extend_minutes: 15, cap_minutes: 45 },
    }),
    'Approaching the 30 min agreed — up to 15 more minutes are covered by auto-extend.'
  )
  // Auto-extend off: the ceiling IS the estimate, and the remaining count is
  // the honest thing to say.
  assert.equal(
    capWarningNote({
      warning: true,
      reached: false,
      remaining_minutes: 5,
      cap: { estimate_minutes: 30, agreed_minutes: 30, auto_extend_minutes: 0, cap_minutes: 30 },
    }),
    'About 5 min left on the time that was agreed.'
  )
  // An approved extension is a new estimate, so it is the number named.
  assert.equal(
    capWarningNote({
      warning: true,
      reached: false,
      remaining_minutes: 20,
      cap: { estimate_minutes: 30, agreed_minutes: 45, auto_extend_minutes: 15, cap_minutes: 60 },
    }),
    'Approaching the 45 min agreed — up to 15 more minutes are covered by auto-extend.'
  )
  // Past the ceiling, and before the warning: not this line's business either
  // way. Billing has stopped in the first case and nothing has happened in the
  // second.
  assert.equal(
    capWarningNote({ warning: false, reached: true, remaining_minutes: 0, cap: { agreed_minutes: 30 } }),
    null
  )
  assert.equal(
    capWarningNote({ warning: false, reached: false, remaining_minutes: 30, cap: { agreed_minutes: 30 } }),
    null
  )
  for (const none of [null, undefined, {}]) {
    assert.equal(capWarningNote(none), null)
  }
})

test('the cancel dialog quotes the amount, not the rule', () => {
  // THE BUILD 11 FINDING behind this: the dialog described the policy ("if
  // work has already been recorded, you are billed for that time only") to a
  // requester who then had to apply it to their own task while deciding
  // whether to spend money. And on an accepted task there was no dialog at
  // all, because there was no way to cancel one.
  assert.equal(
    cancelChargeLine({
      committed: true,
      within_grace: false,
      charge_cents: 2450,
      base_fee_cents: 1200,
      time_cost_cents: 1250,
      billed_minutes: 40,
    }),
    'Your supporter has committed. You\u2019ll be charged $24.50 — $12.00 base fee plus $12.50 for the 40 min worked.'
  )
  // Accepted, nothing logged: the base fee alone, and named as a base fee
  // rather than as an unexplained $12.
  assert.equal(
    cancelChargeLine({
      committed: true,
      within_grace: false,
      charge_cents: 1200,
      base_fee_cents: 1200,
      time_cost_cents: 0,
      billed_minutes: 0,
    }),
    'Your supporter has committed. You\u2019ll be charged $12.00 (base fee).'
  )
  // Inside the grace window.
  assert.match(
    cancelChargeLine({ committed: true, within_grace: true, charge_cents: 0 }),
    /costs nothing/
  )
  // THE BOUNDARY CASE THE DIALOG CAN SIT ACROSS. The block was fetched inside
  // the window; the live countdown has since run out. The caller's verdict
  // wins, because it is the one that matches what the server will do when the
  // button is actually pressed.
  assert.match(
    cancelChargeLine(
      { committed: true, within_grace: true, charge_cents: 1200, base_fee_cents: 1200, time_cost_cents: 0 },
      { withinGrace: false }
    ),
    /charged \$12\.00 \(base fee\)/
  )
  // Nobody committed: cancelling has never cost anything.
  assert.equal(cancelChargeLine({ committed: false }), null)
  for (const none of [null, undefined, {}]) {
    assert.equal(cancelChargeLine(none), null)
  }
})

test('the release is named before they commit, and only when there is one', () => {
  assert.equal(
    cancelReleaseLine(
      { committed: true, within_grace: false, charge_cents: 1200, release_cents: 6475 },
      { authorized_cents: 7675 }
    ),
    '$64.75 of your reserved $76.75 releases immediately.'
  )
  // Free cancel: the whole hold goes back, and says so in the simpler words.
  assert.equal(
    cancelReleaseLine({ committed: false }, { authorized_cents: 7675 }),
    'Your reserved $76.75 will be released immediately.'
  )
  // No hold — every task posted with PAYMENTS_ENFORCED off. Silence beats
  // "$0.00 releases".
  assert.equal(cancelReleaseLine({ committed: true, release_cents: 0 }, null), null)
  assert.equal(cancelReleaseLine({ committed: true, release_cents: 0 }, { authorized_cents: 0 }), null)
})

test('the grace countdown runs off the server deadline and stops at zero', () => {
  const now = Date.parse('2026-09-21T12:00:00Z')
  const at = (secs) => new Date(now + secs * 1000).toISOString()
  assert.equal(cancelGraceCountdown(at(83), now), '1:23')
  assert.equal(cancelGraceCountdown(at(120), now), '2:00')
  assert.equal(cancelGraceCountdown(at(9), now), '0:09')
  // Expired, and the instant it expires. Null is what flips the dialog back to
  // quoting a charge, so it has to be null and not "0:00".
  assert.equal(cancelGraceCountdown(at(0), now), null)
  assert.equal(cancelGraceCountdown(at(-5), now), null)
  assert.equal(cancelGraceCountdown(null, now), null)
  assert.equal(cancelGraceCountdown(undefined, now), null)
})

test('a resolved request leads with the answer, not with asking again', () => {
  // THE BUILD 11 FINDING: after a deny or auto-deny the card re-surfaced the
  // ask options at the same weight as the fallback instruction. Three
  // equal-looking things, one of them the answer and two of them "ask again",
  // which read as the system urging a re-ask of somebody who had just said no.
  //
  // The verdict is the quiet half; the INSTRUCTION is the loud one, because
  // what the supporter needs is what to do next.
  const expiredBudget = {
    kind: 'budget',
    status: 'expired',
    fallback_instruction: 'Buy the alternative you chose: the 500g jar',
  }
  assert.equal(extensionResolutionTitle(expiredBudget), 'No response')
  assert.equal(
    extensionResolutionDetail(expiredBudget),
    'Proceed with your fallback: Buy the alternative you chose: the 500g jar'
  )

  // A denial is the supporter's own fallback too, without the "no response"
  // framing — somebody DID answer.
  const deniedBudget = { ...expiredBudget, status: 'denied' }
  assert.equal(extensionResolutionTitle(deniedBudget), 'Denied')
  assert.equal(extensionResolutionDetail(deniedBudget), 'Buy the alternative you chose: the 500g jar')

  // A TIME request has no fallback field and never had one, because the
  // fallback IS the billing. That is a real answer and it belongs here rather
  // than leaving the primary line blank.
  const expiredTime = { kind: 'time', status: 'expired' }
  assert.equal(extensionResolutionTitle(expiredTime), 'No response')
  assert.match(extensionResolutionDetail(expiredTime), /isn\u2019t billed/)
  assert.match(extensionResolutionDetail(expiredTime), /complete the task at any point/)

  // Not a resolution: nothing to lead with.
  for (const live of [{ status: 'pending' }, { status: 'approved' }, null, undefined]) {
    assert.equal(extensionResolutionTitle(live), null)
    assert.equal(extensionResolutionDetail(live), null)
  }
})

test('the re-ask is labelled for the kind that was just refused', () => {
  // Obviously the SAME request rather than a new idea — and subdued, which is
  // the clients' job. Requesting again stays permitted by design: per-kind
  // pending re-opens the moment a request resolves.
  assert.equal(extensionReaskLabel({ kind: 'time' }), 'Ask for more time again')
  assert.equal(extensionReaskLabel({ kind: 'budget' }), 'Ask for more budget again')
  assert.equal(extensionReaskLabel(null), 'Ask for more budget again')
})

test('the cancel dialog promises a specific amount back, before anything happens', () => {
  assert.equal(
    holdWillBeReleasedMessage({ authorized_cents: 7675 }),
    'Your reserved $76.75 will be released immediately.'
  )
})

test('a cancel that took nothing says the whole hold went back', () => {
  const msg = holdReleasedMessage({ captured_cents: 0, released_cents: 7675 })
  assert.match(msg, /^Reserved \$76\.75 released\./)
  assert.match(msg, /1–7 days/)
  // "Released" and "refunded" are different things and the copy must not blur
  // them: nothing was ever taken, so there is nothing to refund.
  assert.doesNotMatch(msg, /refund/i)
})

test('a partial settlement states both halves, from the server’s own numbers', () => {
  const msg = holdReleasedMessage({ captured_cents: 2450, released_cents: 5225 })
  assert.match(msg, /Charged \$24\.50 for completed time/)
  assert.match(msg, /remaining \$52\.25 hold has been released/)
  assert.match(msg, /1–7 days/)
})

test('a settlement that consumed the whole hold still reads correctly', () => {
  // captured == authorized, so released is 0. "the remaining $0.00 hold has
  // been released" would be nonsense; it degrades to a clause that is true.
  const msg = holdReleasedMessage({ captured_cents: 7675, released_cents: 0 })
  assert.match(msg, /Charged \$76\.75 for completed time/)
  assert.doesNotMatch(msg, /\$0\.00/)
})

test('no message ever hedges about the amount, and none says "refund"', () => {
  const placed = holdPlacedMessage({
    authorized_cents: 4950,
    time_cost_cents: 1950,
    shopping_budget_cents: 3000,
  })
  const messages = [
    placed.primary,
    holdSummary({ authorized_cents: 7675, card_brand: 'visa', card_last4: '4242' }),
    holdWillBeReleasedMessage({ authorized_cents: 7675 }),
    holdReleasedMessage({ captured_cents: 2450, released_cents: 5225 }),
    holdReleasedMessage({ captured_cents: 0, released_cents: 7675 }),
    outstandingBalanceMessage({ total_cents: 1250, task_title: 'A task', task_count: 1 }),
    surgeRateNote({ surge_rate: true, per_minute_rate_cents: 100, included_minutes: 15 }),
  ]
  for (const msg of messages) {
    assert.ok(msg, 'expected a message')
    assert.match(msg, /\$\d/, `no amount in: ${msg}`)
    assert.doesNotMatch(msg, /refund/i, `says "refund" in: ${msg}`)
    for (const hedge of [/\bmay be charged\b/i, /\bmight\b/i, /\bapproximately\b/i, /\bup to\b/i]) {
      assert.doesNotMatch(msg, hedge, `hedged wording in: ${msg}`)
    }
  }
})
