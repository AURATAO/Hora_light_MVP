// Pure pieces of the live Ops Chats page (pages/OpsChats.jsx), kept here so
// node --test can check them.

// Merge an incremental thread fetch into the messages shown: drop repeats by
// id (the server overlaps a few seconds on ?since), keep the conversation
// order (by `at`).
export function mergeMessages(prev, next) {
  const seen = new Set(prev.map((m) => m.id).filter(Boolean))
  const add = next.filter((m) => !m.id || !seen.has(m.id))
  if (add.length === 0) return prev
  return [...prev, ...add].sort((a, b) => new Date(a.at) - new Date(b.at))
}

// Which optimistic replies are still to be drawn. A 'queued' one (the
// server said 202) gives way to the stored HO:RA row that carries it — one
// that was not in the thread when it was sent (p.before, a Set of ids):
// the same text first; then, since the store may redact a number in it,
// the earliest such row left over. Relayed supporter lines never count.
// Returns { local: pending still drawn, landed: keys to drop }.
export function settlePending(msgs, mine) {
  const used = new Set()
  const hits = new Map()
  const fresh = (p, m, i) => !used.has(i) && m.direction === 'outbound' && !m.relay && !(m.id && p.before?.has(m.id))
  for (const pass of ['text', 'any']) {
    for (const p of mine) {
      if (p.status !== 'queued' || hits.has(p.key)) continue
      const i = msgs.findIndex((m, j) => fresh(p, m, j) && (pass === 'any' || m.text === p.text))
      if (i >= 0) { used.add(i); hits.set(p.key, i) }
    }
  }
  return { local: mine.filter((p) => !hits.has(p.key)), landed: [...hits.keys()] }
}

// Hand-offs with a customer message newer than the last list: the ones to
// alert about. prev is null on the first list (a baseline, no alerts).
// Returns { next: Map identity_id → last inbound ms, fresh: rows }.
export function newHandoffMessages(prev, list) {
  const next = new Map()
  const fresh = []
  for (const c of list) {
    const t = c.last_inbound_at ? new Date(c.last_inbound_at).getTime() : 0
    next.set(c.identity_id, t)
    if (prev && c.state === 'handoff' && t > (prev.get(c.identity_id) ?? 0)) fresh.push(c)
  }
  return { next, fresh }
}

// How to tell ops about a fresh hand-off message (one row from
// newHandoffMessages). Tab hidden: 'notify' — the title count, the chime
// and a browser notification. Tab visible: 'chime' only — unless it is the
// chat that is open and the window has focus, where they are already
// looking at it: null.
export function alertFor(c, { hidden, focused, selectedId }) {
  if (hidden) return 'notify'
  if (focused && c.identity_id === selectedId) return null
  return 'chime'
}
