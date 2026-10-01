import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { REMOVED_GONE_BODY, REMOVED_GONE_TITLE, isTaskRemovedError, removalNotice } from './taskRemoval.js'

const mobileUtils = readFileSync(new URL('../../../mobile/src/lib/task-utils.ts', import.meta.url), 'utf8')
const mobileScreen = readFileSync(new URL('../../../mobile/src/app/task/[id].tsx', import.meta.url), 'utf8')

test('every removal notice is mobile\'s, word for word', () => {
  for (const reason of ['out_of_scope_private_residence', 'out_of_scope_other', 'inappropriate', 'other']) {
    assert.ok(mobileUtils.includes(JSON.stringify(removalNotice(reason))), `${reason} differs from mobile`)
  }
})

test('an unknown or missing reason falls back to the general notice', () => {
  assert.equal(removalNotice('something_new'), removalNotice('other'))
  assert.equal(removalNotice(null), removalNotice('other'))
})

test('the detached supporter\'s empty state is mobile\'s', () => {
  assert.ok(mobileScreen.includes(JSON.stringify(REMOVED_GONE_TITLE)))
  assert.ok(mobileScreen.includes(JSON.stringify(REMOVED_GONE_BODY)))
})

test('only a 403 task_removed counts as removed', () => {
  assert.equal(isTaskRemovedError({ status: 403, body: { error: 'task_removed' } }), true)
  assert.equal(isTaskRemovedError({ status: 403, body: { error: 'forbidden' } }), false)
  assert.equal(isTaskRemovedError({ status: 400, body: { error: 'task_removed' } }), false)
  assert.equal(isTaskRemovedError(null), false)
})
