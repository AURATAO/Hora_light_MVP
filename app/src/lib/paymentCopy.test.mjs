import test from 'node:test'
import assert from 'node:assert/strict'
import {
  formatCardLabel,
  holdPlacedMessage,
  holdSummary,
  holdWillBeReleasedMessage,
  holdReleasedMessage,
} from './paymentCopy.js'

/**
 * The copy is the feature here, so the copy is what is pinned.
 *
 * A live tester posted a task, was told nothing about their card, assumed the
 * post had failed and cancelled it. The money was correct throughout; the
 * silence was the defect. These tests hold the two properties that silence
 * violated: a real number is always named, and nothing is said at all when
 * there is no number to name.
 */

test('a card is named the way a cardholder reads it', () => {
  assert.equal(formatCardLabel({ card_brand: 'visa', card_last4: '4242' }), 'Visa ••4242')
  assert.equal(formatCardLabel({ card_brand: 'mastercard', card_last4: '5100' }), 'Mastercard ••5100')
  assert.equal(formatCardLabel({ card_brand: 'amex', card_last4: '0005' }), 'American Express ••0005')
  // An unrecognised brand degrades to "Card" rather than dropping the whole
  // clause — the last four are the half the cardholder recognises, and a raw
  // Stripe slug is not copy. Same fallback the saved-cards list uses.
  assert.equal(formatCardLabel({ card_brand: 'cartes_bancaires', card_last4: '1111' }), 'Card ••1111')
  assert.equal(formatCardLabel({ card_last4: '4242' }), 'Card ••4242')
})

test('no card, no clause — never "your card (unknown)"', () => {
  for (const payment of [null, undefined, {}, { card_brand: 'visa' }]) {
    assert.equal(formatCardLabel(payment), '')
  }
  // The hold is still announced, just without naming a card.
  const msg = holdPlacedMessage({ authorized_cents: 7675 })
  assert.match(msg, /\$76\.75 on your card\./)
  assert.doesNotMatch(msg, /\(/)
})

test('the post-success message names the amount and the card', () => {
  const msg = holdPlacedMessage({ authorized_cents: 7675, card_brand: 'visa', card_last4: '4242' })
  assert.match(msg, /reserved \$76\.75 on your card \(Visa ••4242\)/)
  assert.match(msg, /only be charged for actual time and purchases/)
  assert.match(msg, /released automatically/)
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

test('no message ever hedges about the amount', () => {
  const messages = [
    holdPlacedMessage({ authorized_cents: 7675, card_brand: 'visa', card_last4: '4242' }),
    holdSummary({ authorized_cents: 7675, card_brand: 'visa', card_last4: '4242' }),
    holdWillBeReleasedMessage({ authorized_cents: 7675 }),
    holdReleasedMessage({ captured_cents: 2450, released_cents: 5225 }),
    holdReleasedMessage({ captured_cents: 0, released_cents: 7675 }),
  ]
  for (const msg of messages) {
    assert.ok(msg, 'expected a message')
    assert.match(msg, /\$\d/, `no amount in: ${msg}`)
    for (const hedge of [/\bmay be charged\b/i, /\bmight\b/i, /\bapproximately\b/i, /\bup to\b/i]) {
      assert.doesNotMatch(msg, hedge, `hedged wording in: ${msg}`)
    }
  }
})
