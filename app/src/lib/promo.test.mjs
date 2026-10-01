import test from 'node:test'
import assert from 'node:assert/strict'
import { readPromoFailure, totalAfterPromo } from './promo.js'

const refusal = (body, status = 400) => Object.assign(new Error('HTTP ' + status), { status, body })

test('a promo refusal is relayed in the server\'s sentence', () => {
  assert.equal(
    readPromoFailure(refusal({ error: 'promo_already_used', message: "You've already used WELCOME10." })),
    "You've already used WELCOME10.",
  )
  assert.equal(readPromoFailure(refusal({ error: 'promo_invalid' })), "That promo code can't be used. Post without one.")
})

test('anything else is not a promo failure', () => {
  assert.equal(readPromoFailure(refusal({ error: 'title required' })), null)
  assert.equal(readPromoFailure(refusal({ error: 'promo_invalid', message: 'x' }, 500)), null)
  assert.equal(readPromoFailure(refusal('<html>')), null)
  assert.equal(readPromoFailure(null), null)
})

test('the total is the discounted one only when the server says so', () => {
  assert.equal(totalAfterPromo({ promo_discount_cents: 1000, total_after_promo_cents: 950 }, 1950), 950)
  assert.equal(totalAfterPromo({ promo_discount_cents: 1950, total_after_promo_cents: 0 }, 1950), 0)
  assert.equal(totalAfterPromo({ promo_discount_cents: 1000 }, 1950), 1950)
  assert.equal(totalAfterPromo({}, 1950), 1950)
  assert.equal(totalAfterPromo(null, 1950), 1950)
})
