import test from 'node:test'
import assert from 'node:assert/strict'
import { isUncertainPost, keyAfterPost, newBookingKey } from './bookingKey.js'

const failure = (status, body) => Object.assign(new Error('HTTP ' + status), { status, body })

test('a new form gets a new key, in the shape the server accepts', () => {
  const a = newBookingKey()
  const b = newBookingKey()
  assert.notEqual(a, b)
  for (const k of [a, b]) assert.match(k, /^[A-Za-z0-9_-]{16,64}$/)
})

test('a retry of the same form keeps its key', () => {
  const k = newBookingKey()
  assert.equal(keyAfterPost(k, 'uncertain'), k)
  assert.equal(keyAfterPost(k, 'card'), k)
  assert.equal(keyAfterPost(k, 'other'), k)
})

test('a settled post starts the next one fresh', () => {
  const k = newBookingKey()
  assert.notEqual(keyAfterPost(k, 'posted'), k)
  assert.notEqual(keyAfterPost(k, 'authenticate'), k)
})

test('only the uncertain-hold answer reads as uncertain', () => {
  assert.equal(isUncertainPost(failure(502, { error: 'payments_uncertain', message: 'x' })), true)
  assert.equal(isUncertainPost(failure(502, '<html>')), false)
  assert.equal(isUncertainPost(failure(402, { error: 'payment_required' })), false)
  assert.equal(isUncertainPost(null), false)
})
