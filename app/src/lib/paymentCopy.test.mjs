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
