import test from 'node:test'
import assert from 'node:assert/strict'
import { mergeMessages, settlePending, newHandoffMessages, alertFor } from './opsChatsLive.js'

const m = (id, at, extra = {}) => ({ id, at, direction: 'outbound', text: id, ...extra })

test('an incremental fetch drops the overlap and keeps the order', () => {
  const prev = [m('a', '2026-11-17T10:00:00Z'), m('b', '2026-11-17T10:01:00Z')]
  const out = mergeMessages(prev, [m('b', '2026-11-17T10:01:00Z'), m('c', '2026-11-17T10:00:30Z', { direction: 'inbound' })])
  assert.deepEqual(out.map((x) => x.id), ['a', 'c', 'b'])
  assert.equal(mergeMessages(prev, [m('a', '2026-11-17T10:00:00Z')]), prev, 'nothing new: same array, no re-render')
})

test('a queued reply lands on its own stored row, not an earlier one with the same words', () => {
  const before = new Set(['old'])
  const msgs = [m('old', '2026-11-17T10:00:00Z', { text: 'ok' })]
  const p = { key: 'p1', status: 'queued', text: 'ok', before }
  assert.deepEqual(settlePending(msgs, [p]).landed, [], 'the old "ok" is not this one')
  const after = [...msgs, m('new', '2026-11-17T10:00:05Z', { text: 'ok' })]
  assert.deepEqual(settlePending(after, [p]).landed, ['p1'])
})

test('a redacted reply still lands; relays and sending replies never do', () => {
  const before = new Set()
  const p1 = { key: 'p1', status: 'queued', text: 'call me at +1 555 123 4567', before }
  const p2 = { key: 'p2', status: 'queued', text: 'thanks', before }
  const sending = { key: 'p3', status: 'sending', text: 'more', before }
  const msgs = [
    m('r', '2026-11-17T10:00:00Z', { text: 'supporter says hi', relay: true }),
    m('x', '2026-11-17T10:00:01Z', { text: 'call me at [number]' }),
    m('y', '2026-11-17T10:00:02Z', { text: 'thanks' }),
  ]
  const { local, landed } = settlePending(msgs, [p1, p2, sending])
  assert.deepEqual(landed.sort(), ['p1', 'p2'], 'exact text first, then the redacted one takes what is left')
  assert.deepEqual(local.map((p) => p.key), ['p3'])
})

test('alerts: the first list is a baseline; then only hand-offs with a newer customer message', () => {
  const l1 = [
    { identity_id: 'a', state: 'handoff', last_inbound_at: '2026-11-17T10:00:00Z' },
    { identity_id: 'b', state: 'idle', last_inbound_at: '2026-11-17T10:00:00Z' },
  ]
  const first = newHandoffMessages(null, l1)
  assert.equal(first.fresh.length, 0)
  const l2 = [
    { identity_id: 'a', state: 'handoff', last_inbound_at: '2026-11-17T10:02:00Z' },
    { identity_id: 'b', state: 'idle', last_inbound_at: '2026-11-17T10:02:00Z' },
    { identity_id: 'c', state: 'handoff', last_inbound_at: '2026-11-17T10:02:00Z' },
  ]
  const second = newHandoffMessages(first.next, l2)
  assert.deepEqual(second.fresh.map((c) => c.identity_id), ['a', 'c'])
  assert.equal(newHandoffMessages(second.next, l2).fresh.length, 0, 'nothing new on the next poll')
})

test('how ops are told: hidden notifies; visible chimes unless it is the open chat with focus', () => {
  const c = { identity_id: 'a' }
  assert.equal(alertFor(c, { hidden: true, focused: false, selectedId: 'a' }), 'notify')
  assert.equal(alertFor(c, { hidden: false, focused: true, selectedId: 'a' }), null, 'open and focused: they can see it')
  assert.equal(alertFor(c, { hidden: false, focused: false, selectedId: 'a' }), 'chime', 'open but the window is in the background')
  assert.equal(alertFor(c, { hidden: false, focused: true, selectedId: 'b' }), 'chime', 'another chat is open')
  assert.equal(alertFor(c, { hidden: false, focused: true, selectedId: null }), 'chime')
})
