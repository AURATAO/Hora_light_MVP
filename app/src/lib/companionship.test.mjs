import test from 'node:test'
import assert from 'node:assert/strict'
import { isCompanionCategory, needsCompanionPolicy } from './companionship.js'

test('both spellings are companionship; nothing else is', () => {
  assert.equal(isCompanionCategory('companionship'), true)
  assert.equal(isCompanionCategory('companion'), true)
  for (const c of ['grocery', 'delivery', 'task', 'quick_errand', '', undefined, null]) {
    assert.equal(isCompanionCategory(c), false)
  }
})

test('the gate follows the category, whatever path set it', () => {
  // The AI prefill's spelling — the path that used to skip the policy.
  assert.equal(needsCompanionPolicy('companionship'), true)
  assert.equal(needsCompanionPolicy('companion'), true)
  assert.equal(needsCompanionPolicy('grocery'), false)
})

test('accepted once, or posted as companionship already, is not asked again', () => {
  assert.equal(needsCompanionPolicy('companionship', { acknowledged: true }), false)
  assert.equal(needsCompanionPolicy('companion', { preAcknowledged: true }), false)
})
