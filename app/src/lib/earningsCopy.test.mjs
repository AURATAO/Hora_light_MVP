import test from 'node:test'
import assert from 'node:assert/strict'
import { earningsWhereaboutsLine, transferStatusCopy } from './earningsCopy.js'

// The copy is the feature: "Earned all time" is what a supporter has EARNED
// (successful transfers), and the line under it says where that money is,
// as two numbers. A failed transfer never counts and never looks paid.

test('the whereabouts line is two real numbers that add up to earned', () => {
  assert.equal(
    earningsWhereaboutsLine({ lifetime_earned_cents: 4699, in_transit_cents: 999, paid_out_cents: 3700 }),
    '$9.99 on its way to your bank · $37.00 paid out',
  )
  // Nothing in the bank yet is still a number, not a hedge.
  assert.equal(
    earningsWhereaboutsLine({ lifetime_earned_cents: 1200, in_transit_cents: 1200, paid_out_cents: 0 }),
    '$12.00 on its way to your bank · $0.00 paid out',
  )
})

test('nothing earned, nothing to split', () => {
  assert.equal(earningsWhereaboutsLine({ lifetime_earned_cents: 0, in_transit_cents: 0, paid_out_cents: 0 }), '')
  assert.equal(earningsWhereaboutsLine({}), '')
})

test('a row says one of three words, and "paid" means the bank has it', () => {
  assert.deepEqual(transferStatusCopy({ status: 'paid', display_status: 'paid' }), { label: 'Paid', note: '' })
  assert.deepEqual(transferStatusCopy({ status: 'paid', display_status: 'on_its_way' }), { label: 'On its way', note: '' })
  assert.deepEqual(transferStatusCopy({ status: 'pending', display_status: 'on_its_way' }), { label: 'On its way', note: '' })
})

test('a failed row is never silent and never looks paid', () => {
  const failed = transferStatusCopy({ status: 'failed', display_status: 'failed' })
  assert.equal(failed.label, 'Failed')
  assert.equal(failed.note, "We're on it — you won't lose this payment.")
})

test('an older backend without display_status degrades safely', () => {
  // "paid" without display_status cannot tell bank from balance: on its way.
  assert.equal(transferStatusCopy({ status: 'paid' }).label, 'On its way')
  assert.equal(transferStatusCopy({ status: 'failed' }).label, 'Failed')
  assert.equal(transferStatusCopy(undefined).label, 'On its way')
})
