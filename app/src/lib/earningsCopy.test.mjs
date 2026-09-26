import test from 'node:test'
import assert from 'node:assert/strict'
import {
  earningsWhereaboutsLine,
  transferStatusCopy,
  platformFeePercent,
  platformFeeExplainer,
  earnedBreakdownLine,
  transferBreakdownLine,
} from './earningsCopy.js'

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

// ── The platform fee, said out loud (D-14) ───────────────────────────────

test('platformFeePercent never rounds a rate it did not charge', () => {
  assert.equal(platformFeePercent(2000), '20%')
  assert.equal(platformFeePercent(250), '2.5%')
  assert.equal(platformFeePercent(0), '0%')
  assert.equal(platformFeePercent(undefined), '0%')
})

test('the explainer reads the backend rate, and ships with 20% when there is none', () => {
  assert.equal(
    platformFeeExplainer(2000),
    'HO:RA takes 20% of service fees; purchase reimbursements are always paid back in full.',
  )
  assert.equal(platformFeeExplainer(), platformFeeExplainer(2000))
})

test('a settlement with a fee names the fee beside the service, and the reimbursement whole', () => {
  assert.equal(
    earnedBreakdownLine({ time_cents: 1960, reimbursement_cents: 1240, platform_fee_cents: 490, platform_fee_bps: 2000 }),
    '$19.60 service (after 20% platform fee) + $12.40 reimbursement',
  )
  assert.equal(
    earnedBreakdownLine({ time_cents: 1960, reimbursement_cents: 0, platform_fee_cents: 490, platform_fee_bps: 2000 }),
    '$19.60 service (after 20% platform fee)',
  )
})

test('a row paid before the fee is never described as discounted', () => {
  assert.equal(
    transferBreakdownLine({ time_cents: 2700, receipt_cents: 1240, platform_fee_cents: 0 }),
    '$27.00 service + $12.40 reimbursement',
  )
  assert.equal(transferBreakdownLine({ time_cents: 0, receipt_cents: 1240 }), '$12.40 reimbursement')
  assert.equal(earnedBreakdownLine(null), '')
})
