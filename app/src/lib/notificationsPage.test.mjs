import test from 'node:test'
import assert from 'node:assert/strict'
import { appendPage, hasMoreAfter, nextCursor } from './notificationsPage.js'

const row = (id, at) => ({ id, created_at: at })

test('a page is appended without repeating a row that straddles the cursor', () => {
  const current = [row('a', '3'), row('b', '2')]
  const merged = appendPage(current, [row('b', '2'), row('c', '1')])
  assert.deepEqual(merged.map((n) => n.id), ['a', 'b', 'c'])
})

test('a short page is the end; a full one is not', () => {
  assert.equal(hasMoreAfter([row('a', '1'), row('b', '1')], 2), true)
  assert.equal(hasMoreAfter([row('a', '1')], 2), false)
  assert.equal(hasMoreAfter([], 2), false)
  assert.equal(hasMoreAfter(undefined, 2), false)
})

test('the cursor is the oldest loaded row', () => {
  assert.equal(nextCursor([row('a', '3'), row('b', '2')]), '2')
  assert.equal(nextCursor([]), null)
})
