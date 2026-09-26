import { test } from 'node:test'
import assert from 'node:assert/strict'
import { RESEND_COOLDOWN_MS, resendSecondsLeft, resendLabel } from './otpResend.js'

test('cooldown is thirty seconds', () => {
  assert.equal(RESEND_COOLDOWN_MS, 30_000)
})

test('counts down in whole seconds and stops at zero', () => {
  const readyAt = 100_000
  assert.equal(resendSecondsLeft(readyAt, 70_000), 30)
  assert.equal(resendSecondsLeft(readyAt, 99_001), 1) // 999ms rounds up, never "0s" while blocked
  assert.equal(resendSecondsLeft(readyAt, 100_000), 0)
  assert.equal(resendSecondsLeft(readyAt, 200_000), 0)
  assert.equal(resendSecondsLeft(0, 200_000), 0) // never sent: nothing to wait for
})

test('label says how long, then says the action', () => {
  assert.equal(resendLabel(100_000, 88_500), 'Resend in 12s')
  assert.equal(resendLabel(100_000, 100_000), 'Resend code')
  assert.equal(resendLabel(0, 1), 'Resend code')
})
