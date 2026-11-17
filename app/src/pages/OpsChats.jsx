// Chats — the ops view of the iMessage/WhatsApp requester channel
// (server/channel_admin.go). Rendered as a tab inside OpsFeed's page shell, so
// it inherits that page's admin gate; the backend re-checks the allowlist on
// every call regardless.
//
// What this is for: when the bot hands a conversation to a person ("talk to
// a human", a payment that needs authentication, something the assistant
// judged a person should handle), ops get an email with the thread and a
// curl line. This page is the curl line with a UI — read the thread, say
// "I'm on it", reply as HO:RA, hand the chat back. Everything else on the
// page is a ledger: which numbers have talked to the bot, where each
// conversation is, and what was said, so a complaint can be checked.
//
// Rules the page keeps, same as the server:
//   - Never a full phone number. The server sends the last four digits and,
//     for a linked number, the account's display name (S-12).
//   - A reply only goes out on a handed-off chat (409 otherwise). The box is
//     disabled for every other state rather than hiding the rule.
//   - "Fix the request" (D-30): an internal note — never sent to the person —
//     that the server reads as an edit of their pending request, every address
//     checked like theirs. "Send summary and hand back" gives the chat back
//     and sends them the normal summary with its price: nothing is booked or
//     charged until they reply YES. There is no button that books.
//   - A hand-off times out: HandoffTimeout (10 minutes) without ops activity
//     and the bot takes the chat back and tells the person. The countdown
//     shown here is derived from the server's handoff_activity_at and its
//     stated timeout, not a client clock of its own.
import { useEffect, useMemo, useRef, useState } from 'react'
import { useToast } from '../providers/ToastProvider'
import {
  listChannelConversations,
  getChannelThread,
  sendChannelMessage,
  claimChannelChat,
  releaseChannelChat,
  getChannelDraft,
  editChannelDraft,
  handBackWithSummary,
} from '../api/ops'
import { mergeMessages, settlePending, newHandoffMessages } from '../lib/opsChatsLive'

// Polling (no Realtime: RLS stays deny-all and every read goes through the
// ops-only Go routes). The open thread polls fast only while it is with a
// person; the list polls faster while anyone is waiting. The fast thread
// poll asks only for new rows (?since) — one indexed query, no re-signing of
// every photo link — and a full reload every THREAD_FULL_MS keeps photo
// links fresh and picks up photos attached after their message.
const THREAD_HANDOFF_MS = 2_000
const LIST_HANDOFF_MS = 5_000
const IDLE_MS = 10_000
const THREAD_FULL_MS = 60_000
const EDIT_TIMES_KEY = 'opsChats.editMs'
const NOTIFY_ASKED_KEY = 'opsChats.notifyAsked'

function readEditTimes() {
  try {
    const v = JSON.parse(localStorage.getItem(EDIT_TIMES_KEY) || '[]')
    return Array.isArray(v) ? v.filter(Number.isFinite) : []
  } catch { return [] }
}

function rememberEditTime(ms) {
  try {
    localStorage.setItem(EDIT_TIMES_KEY, JSON.stringify([...readEditTimes(), ms].slice(-20)))
  } catch { /* storage blocked: no "usually" hint, nothing else */ }
}

// A short two-note chime, no audio file. The context is created on the
// first click on the page (browsers keep audio locked until a gesture).
let audioCtx = null
function unlockAudio() {
  try {
    if (!audioCtx) audioCtx = new (window.AudioContext || window.webkitAudioContext)()
    if (audioCtx.state === 'suspended') audioCtx.resume()
  } catch { audioCtx = null }
}
function chime() {
  if (!audioCtx) return
  try {
    const t = audioCtx.currentTime
    ;[880, 1320].forEach((f, i) => {
      const o = audioCtx.createOscillator()
      const g = audioCtx.createGain()
      o.frequency.value = f
      g.gain.setValueAtTime(0.0001, t + i * 0.12)
      g.gain.exponentialRampToValueAtTime(0.15, t + i * 0.12 + 0.02)
      g.gain.exponentialRampToValueAtTime(0.0001, t + i * 0.12 + 0.18)
      o.connect(g).connect(audioCtx.destination)
      o.start(t + i * 0.12)
      o.stop(t + i * 0.12 + 0.2)
    })
  } catch { /* no sound; the title and the notification still say it */ }
}


function formatWhen(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

function formatAgo(iso, now) {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 48) return `${h} h ago`
  return `${Math.round(h / 24)} d ago`
}

const STATE_LABEL = {
  idle: 'Idle',
  drafting: 'Drafting a task',
  awaiting_name: 'Asked for a name',
  awaiting_confirm: 'Waiting for YES on a task',
  awaiting_setup: 'Waiting for setup link',
  awaiting_approval: 'Waiting for YES on a charge',
  awaiting_budget: 'Asked for a budget',
  awaiting_task_choice: 'Asked which task',
  awaiting_cancel: 'Cancelling',
  handoff: 'With a person',
}

const FIELD_LABEL = {
  title: 'Errand',
  category: 'Type',
  description: 'Details for the supporter',
  pickup: 'Pickup',
  drop_off: 'Drop-off',
  when: 'When',
  budget: 'Shopping budget',
  notes: 'Notes',
  quote: 'Estimate',
}

const NOTE_EXAMPLES = 'e.g. “address: 335 E 6th St, New York, NY 10003” · “it’s Advil, 1 box, from any pharmacy” · “time: today 6pm”'

function money(c) {
  if (!c) return ''
  return c % 100 === 0 ? `$${c / 100}` : `$${(c / 100).toFixed(2)}`
}

// The request as the person will see it summarised: one row per field.
function DraftCard({ d, title }) {
  if (!d) return null
  const rows = [
    ['Errand', d.title],
    ['Pickup', d.pickup || (d.needs_budget && d.drop_off ? 'Any store near the drop-off' : '—')],
    ['Drop-off', d.drop_off || '—'],
    ['When', d.when],
    ['Shopping budget', d.budget_cents ? money(d.budget_cents) : d.needs_budget ? 'Not set — they will be asked' : '—'],
    ['Estimate', `${money(d.quote_cents) || '—'}${d.quote_minutes ? ` · about ${d.quote_minutes} min` : ''}`],
    ['Details for the supporter', d.description || '—'],
  ]
  if (d.notes?.length) rows.push(['Notes', d.notes.join(' | ')])
  return (
    <div className="rounded-md border border-white/15 p-2 text-xs">
      <div className="mb-1 font-semibold opacity-80">{title}</div>
      <dl className="grid grid-cols-3 gap-x-2 gap-y-1">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="opacity-60">{k}</dt>
            <dd className="col-span-2 whitespace-pre-wrap break-words">{v}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

// "Fix the request" is one server call that reads the note with the model
// and checks every address against Google (up to 45 s on the server). This
// says it is working, how long so far, and how long it usually takes here.
function EditProgress({ start, times }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 500)
    return () => clearInterval(t)
  }, [])
  const secs = start ? Math.max(0, Math.floor((performance.now() - start) / 1000)) : 0
  const usual = times.length >= 3 ? Math.round([...times].sort((a, b) => a - b)[Math.floor(times.length / 2)] / 1000) : null
  return (
    <div className="rounded-md border border-amber-300/30 p-2 text-xs" role="status" aria-live="polite">
      <div className="flex items-center gap-2">
        <span className="inline-block h-3 w-3 animate-spin rounded-full border-2 border-amber-300/70 border-t-transparent" />
        <span>
          Applying your note — reading it and checking any address
          {' · '}{secs}s{usual ? ` (usually about ${usual}s)` : ''}
        </span>
      </div>
      <div className="mt-2 h-1 overflow-hidden rounded bg-white/10">
        <div
          className="h-full bg-amber-300/60 transition-all duration-500"
          style={{ width: `${Math.min(95, usual ? (secs / usual) * 80 : secs * 6)}%` }}
        />
      </div>
      {secs >= 20 && (
        <p className="mt-1 opacity-70">Still working — an address lookup can be slow. It gives up after 45 s.</p>
      )}
    </div>
  )
}

function who(c) {
  return c.user_name ? `${c.user_name} · ${c.phone_hint}` : c.phone_hint
}

export default function OpsChats() {
  const toast = useToast()
  const [convs, setConvs] = useState([])
  const [timeoutMin, setTimeoutMin] = useState(10)
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState(null) // identity_id
  const [thread, setThread] = useState(null)     // { identity_id, channel, phone_hint, messages }
  const [threadLoading, setThreadLoading] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState('')            // 'claim' | 'release' | 'edit' | 'summary' (sends don't block)
  const [now, setNow] = useState(Date.now())
  const [draft, setDraft] = useState(null)        // { draft, missing, ready } — the pending request
  const [note, setNote] = useState('')            // internal: never sent
  const [edit, setEdit] = useState(null)          // the last edit: { before, after, changes, ready, missing }
  const [editStart, setEditStart] = useState(0)   // performance.now() when "Apply" was pressed
  const [editTimes, setEditTimes] = useState(readEditTimes)
  // Ops replies shown before the server has them: { key, identity, text,
  // before: ids shown at send time, status: 'sending' | 'queued' | 'failed',
  // error }. 'queued' is the server's 202; the row then gives way to the
  // stored message.
  const [pending, setPending] = useState([])
  const [unseen, setUnseen] = useState(0)         // customer messages in hand-offs while the tab was hidden
  const bottomRef = useRef(null)

  // The timers read these, not render-time state.
  const selectedRef = useRef(null)
  const heldRef = useRef(false)      // the open thread is with a person
  const anyHeldRef = useRef(false)   // anyone is waiting
  const cursorRef = useRef({ id: null, cursor: null, fullAt: 0 })
  const listBusy = useRef(false)
  const threadBusy = useRef(false)
  const seenInbound = useRef(null)   // identity_id → last_inbound_at (ms); null until the first list
  const sendChain = useRef(Promise.resolve())
  const wakeRef = useRef(() => {})
  const baseTitle = useRef(document.title)
  selectedRef.current = selected

  // A customer wrote in a handed-off chat while nobody was looking: count
  // it for the tab title, chime, and a browser notification (hint and
  // name only — never their number or their words, S-12).
  function alertNew(list) {
    const { next, fresh } = newHandoffMessages(seenInbound.current, list)
    seenInbound.current = next
    if (fresh.length === 0 || document.visibilityState !== 'hidden') return
    setUnseen((n) => n + fresh.length)
    chime()
    try {
      if ('Notification' in window && Notification.permission === 'granted') {
        for (const c of fresh) {
          const n = new Notification('Ops Chats — new message', {
            body: `${who(c)} wrote in a chat that's with a person.`,
            tag: `ops-chat-${c.identity_id}`,
          })
          n.onclick = () => { window.focus(); setSelected(c.identity_id); n.close() }
        }
      }
    } catch { /* notifications unavailable: the title still counts */ }
  }

  async function loadList() {
    if (listBusy.current) return
    listBusy.current = true
    try {
      const data = await listChannelConversations()
      const list = Array.isArray(data?.conversations) ? data.conversations : []
      setConvs(list)
      setNow(Date.now())
      anyHeldRef.current = list.some((c) => c.state === 'handoff')
      heldRef.current = list.some((c) => c.identity_id === selectedRef.current && c.state === 'handoff')
      if (Number.isFinite(data?.handoff_timeout_minutes)) setTimeoutMin(data.handoff_timeout_minutes)
      alertNew(list)
    } catch (err) {
      console.error('[ops/chats] list', err)
      toast(err.message || "Couldn't load chats")
    } finally {
      listBusy.current = false
      setLoading(false)
    }
  }

  async function loadDraft(id) {
    if (!id) return
    try {
      setDraft(await getChannelDraft(id))
    } catch (err) {
      console.error('[ops/chats] draft', err)
    }
  }

  // full: replace what is shown (first open, every THREAD_FULL_MS, after a
  // gap). Otherwise ask only for rows after the cursor and merge them.
  async function loadThread(id, { quiet = false, full = false } = {}) {
    if (!id) return
    if (threadBusy.current && quiet) return
    const cur = cursorRef.current
    const incremental = !full && cur.id === id && cur.cursor && Date.now() - cur.fullAt < THREAD_FULL_MS
    if (!quiet) setThreadLoading(true)
    threadBusy.current = true
    try {
      const data = await getChannelThread(id, incremental ? { since: cur.cursor } : {})
      if (selectedRef.current !== id) return // switched chats meanwhile
      if (incremental && data?.more) {
        threadBusy.current = false
        await loadThread(id, { quiet: true, full: true })
        return
      }
      const msgs = Array.isArray(data?.messages) ? data.messages : []
      cursorRef.current = {
        id,
        cursor: data?.cursor && !data.cursor.startsWith('0001-') ? data.cursor : null,
        fullAt: incremental ? cur.fullAt : Date.now(),
      }
      if (incremental) {
        setThread((t) => (t && t.identity_id === id ? { ...t, messages: mergeMessages(t.messages || [], msgs) } : { ...data, messages: msgs }))
      } else {
        setThread({ ...data, messages: msgs })
      }
    } catch (err) {
      console.error('[ops/chats] thread', err)
      if (!quiet) toast(err.message || "Couldn't load that chat")
    } finally {
      threadBusy.current = false
      if (!quiet) setThreadLoading(false)
    }
  }

  // Two timer chains that pick their next delay each time: the thread at
  // 2 s while it is with a person (10 s otherwise, nothing while the tab is
  // hidden), the list at 5 s while anyone is waiting (10 s otherwise). The
  // list keeps running while hidden — that is how a hidden tab hears about
  // a new message. Coming back to the tab (visible or focus) refreshes both
  // at once.
  useEffect(() => {
    let stopped = false
    let listTimer = null
    let threadTimer = null
    // A wake starts fresh chains; a chain from before it (maybe still
    // awaiting its request) sees a newer generation and ends, so there is
    // never more than one of each.
    let listGen = 0
    let threadGen = 0

    const listLoop = async (gen) => {
      await loadList()
      if (stopped || gen !== listGen) return
      listTimer = setTimeout(() => listLoop(gen), anyHeldRef.current ? LIST_HANDOFF_MS : IDLE_MS)
    }
    const threadLoop = async (gen) => {
      if (selectedRef.current && document.visibilityState !== 'hidden') {
        await loadThread(selectedRef.current, { quiet: true })
      }
      if (stopped || gen !== threadGen) return
      threadTimer = setTimeout(() => threadLoop(gen), heldRef.current ? THREAD_HANDOFF_MS : IDLE_MS)
    }
    const wake = () => {
      if (stopped) return
      clearTimeout(listTimer)
      clearTimeout(threadTimer)
      listLoop(++listGen)
      threadLoop(++threadGen)
    }
    wakeRef.current = wake

    let lastWake = 0
    const onBack = () => {
      if (document.visibilityState === 'hidden') return
      setUnseen(0)
      // visibilitychange and focus usually fire together: one refresh.
      if (Date.now() - lastWake < 500) return
      lastWake = Date.now()
      wake()
    }
    document.addEventListener('visibilitychange', onBack)
    window.addEventListener('focus', onBack)

    // Ask for notifications once, on a click (Safari only asks from a
    // gesture); the same click unlocks the chime.
    const onGesture = () => {
      unlockAudio()
      try {
        if ('Notification' in window && Notification.permission === 'default' && !localStorage.getItem(NOTIFY_ASKED_KEY)) {
          localStorage.setItem(NOTIFY_ASKED_KEY, '1')
          Notification.requestPermission().catch(() => {})
        }
      } catch { /* blocked storage or no Notification API */ }
    }
    window.addEventListener('pointerdown', onGesture)

    listLoop(listGen)
    threadTimer = setTimeout(() => threadLoop(threadGen), IDLE_MS)
    return () => {
      stopped = true
      clearTimeout(listTimer)
      clearTimeout(threadTimer)
      document.removeEventListener('visibilitychange', onBack)
      window.removeEventListener('focus', onBack)
      window.removeEventListener('pointerdown', onGesture)
      document.title = baseTitle.current
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    document.title = unseen > 0 ? `(${unseen}) Ops Chats` : baseTitle.current
  }, [unseen])

  useEffect(() => {
    cursorRef.current = { id: null, cursor: null, fullAt: 0 }
    heldRef.current = convs.some((c) => c.identity_id === selected && c.state === 'handoff')
    if (!selected) { setThread(null); setDraft(null); return }
    setThread(null)
    setText('')
    setNote('')
    setEdit(null)
    loadThread(selected, { full: true }).then(() => wakeRef.current())
    loadDraft(selected)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected])

  const current = convs.find((c) => c.identity_id === selected) || null
  const heldNow = current?.state === 'handoff'

  // Optimistic replies for this chat (settlePending says which have landed).
  const shown = useMemo(() => {
    const msgs = thread?.messages || []
    const mine = pending.filter((p) => p.identity === selected)
    if (mine.length === 0) return { msgs, local: [], landed: [] }
    return { msgs, ...settlePending(msgs, mine) }
  }, [thread, pending, selected])

  useEffect(() => {
    if (shown.landed.length) setPending((ps) => ps.filter((p) => !shown.landed.includes(p.key)))
  }, [shown.landed])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' })
  }, [shown.msgs.length, shown.local.length])

  const ordered = useMemo(() => {
    const held = convs.filter((c) => c.state === 'handoff')
    const rest = convs.filter((c) => c.state !== 'handoff')
    held.sort((a, b) => new Date(a.updated_at) - new Date(b.updated_at)) // oldest waiting first
    return { held, rest }
  }, [convs])

  // Minutes until the bot takes the chat back, from the server's clock.
  function minutesLeft(c) {
    if (c.state !== 'handoff' || !c.handoff_activity_at) return null
    const end = new Date(c.handoff_activity_at).getTime() + timeoutMin * 60_000
    return Math.max(0, Math.ceil((end - now) / 60_000))
  }

  function shownIds() {
    return new Set((thread?.messages || []).map((m) => m.id).filter(Boolean))
  }

  // Sends go out one after another, in the order typed, without blocking
  // the box: the next reply can be written while the last is in flight.
  function deliver(p) {
    sendChain.current = sendChain.current.then(async () => {
      try {
        await sendChannelMessage(p.identity, p.text)
        setPending((ps) => ps.map((q) => (q.key === p.key ? { ...q, status: 'queued', error: '' } : q)))
        if (selectedRef.current === p.identity) loadThread(p.identity, { quiet: true })
        loadList()
      } catch (err) {
        setPending((ps) => ps.map((q) => (q.key === p.key ? { ...q, status: 'failed', error: err.message || "Couldn't send" } : q)))
        if (err.status === 409) loadList()
      }
    })
  }

  function send() {
    const t = text.trim()
    if (!t || !selected) return
    const p = { key: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`, identity: selected, text: t, before: shownIds(), status: 'sending', error: '' }
    setPending((ps) => [...ps, p])
    setText('')
    deliver(p)
  }

  function retry(p) {
    const again = { ...p, status: 'sending', error: '', before: shownIds() }
    setPending((ps) => ps.map((q) => (q.key === p.key ? again : q)))
    deliver(again)
  }

  function discard(p) {
    setPending((ps) => ps.filter((q) => q.key !== p.key))
  }

  async function claim() {
    if (!selected) return
    setBusy('claim')
    try {
      await claimChannelChat(selected)
      toast(`On it — the ${timeoutMin}-minute timer restarted.`, 'success')
      await loadList()
    } catch (err) {
      toast(err.message || "Couldn't claim")
      if (err.status === 409) await loadList()
    } finally {
      setBusy('')
    }
  }

  async function release() {
    if (!selected) return
    if (!window.confirm('Hand this chat back to the bot? The person is told, and anything they were in the middle of resumes.')) return
    setBusy('release')
    try {
      await releaseChannelChat(selected)
      toast('Back with the bot.', 'success')
      await loadList()
      await loadThread(selected, { quiet: true })
    } catch (err) {
      toast(err.message || "Couldn't release")
      if (err.status === 409) await loadList()
    } finally {
      setBusy('')
    }
  }

  async function applyNote() {
    const t = note.trim()
    if (!t || !selected) return
    setBusy('edit')
    const started = performance.now()
    setEditStart(started)
    try {
      const res = await editChannelDraft(selected, t)
      const ms = Math.round(performance.now() - started)
      rememberEditTime(ms)
      setEditTimes(readEditTimes())
      setEdit(res)
      setNote('')
      if (res.ignored?.length) toast(`Couldn't read: ${res.ignored.join(', ')} — nothing changed there.`)
      await loadDraft(selected)
      await loadList()
    } catch (err) {
      // 422 (address), 403 (asked to book), 409 (with the bot): the server's
      // sentence says what happened; nothing was saved.
      toast(err.message || "Couldn't apply the note")
      if (err.status === 409) await loadList()
    } finally {
      setBusy('')
      setEditStart(0)
    }
  }

  async function sendSummary() {
    if (!selected) return
    if (!window.confirm('Hand the chat back and send them the summary? Nothing is booked until they reply YES.')) return
    setBusy('summary')
    try {
      const res = await handBackWithSummary(selected)
      const what = res.sent === 'summary' ? 'the summary'
        : res.sent === 'budget' ? 'the budget question (then the summary)'
          : 'the name question (then the summary)'
      toast(`Back with the bot — sent ${what}. Waiting for their YES.`, 'success')
      setEdit(null)
      await loadList()
      await loadThread(selected, { quiet: true })
      await loadDraft(selected)
    } catch (err) {
      toast(err.message || "Couldn't hand back")
      if (err.status === 409) await loadList()
    } finally {
      setBusy('')
    }
  }

  // A reply can be typed and sent while a note is applied; not while the
  // chat is being handed back.
  const handingBack = busy === 'release' || busy === 'summary'
  const ready = edit ? edit.ready : !!draft?.ready
  const missing = edit ? edit.missing : draft?.missing

  function Row({ c }) {
    const left = minutesLeft(c)
    const active = c.identity_id === selected
    return (
      <button
        onClick={() => setSelected(c.identity_id)}
        className={`w-full text-left rounded-md border px-3 py-2 text-sm ${
          active ? 'border-white/40 bg-white/10' : 'border-white/15 hover:border-white/30'
        }`}
      >
        <div className="flex items-center justify-between gap-2">
          <span className="font-medium truncate">{who(c)}</span>
          <span className="text-xs opacity-60 whitespace-nowrap">{formatAgo(c.updated_at, now)}</span>
        </div>
        <div className="flex items-center justify-between gap-2 text-xs opacity-70">
          <span>{c.channel === 'imessage' ? 'iMessage' : 'WhatsApp'} · {STATE_LABEL[c.state] || c.state}</span>
          {left !== null && (
            <span className={left <= 2 ? 'text-amber-300' : ''}>
              {left === 0 ? 'bot takes it back any moment' : `${left} min before the bot takes it back`}
            </span>
          )}
        </div>
      </button>
    )
  }

  return (
    <div className="grid grid-cols-1 md:grid-cols-12 gap-4">
      {/* Left: the queue, then the ledger */}
      <div className="md:col-span-4 space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold uppercase tracking-wide opacity-70">Needs a person</h2>
          <button className="rounded-md border border-white/20 px-2 py-0.5 text-xs hover:border-white/40" onClick={() => wakeRef.current()}>
            Refresh
          </button>
        </div>
        {loading ? (
          <p className="text-sm opacity-70">Loading…</p>
        ) : ordered.held.length === 0 ? (
          <p className="text-sm opacity-60">Nobody is waiting. The bot has every chat.</p>
        ) : (
          <div className="space-y-2">{ordered.held.map((c) => <Row key={c.identity_id} c={c} />)}</div>
        )}

        <h2 className="text-sm font-semibold uppercase tracking-wide opacity-70 pt-2">Recent chats</h2>
        {!loading && ordered.rest.length === 0 ? (
          <p className="text-sm opacity-60">No conversations yet.</p>
        ) : (
          <div className="space-y-2">{ordered.rest.map((c) => <Row key={c.identity_id} c={c} />)}</div>
        )}
      </div>

      {/* Right: one thread */}
      <div className="md:col-span-8">
        {!selected ? (
          <div className="rounded-md border border-white/15 p-6 text-sm opacity-70">
            Pick a chat. Handed-off chats are listed first; you can reply to those as HO:RA.
            Every other chat is read-only — the bot is talking.
          </div>
        ) : (
          <div className="rounded-md border border-white/15 flex flex-col" style={{ minHeight: '60vh' }}>
            <div className="flex items-center justify-between gap-2 border-b border-white/10 px-3 py-2">
              <div className="text-sm">
                <div className="font-medium">{current ? who(current) : thread?.phone_hint || '…'}</div>
                <div className="text-xs opacity-70">
                  {current ? `${current.channel === 'imessage' ? 'iMessage' : 'WhatsApp'} · ${STATE_LABEL[current.state] || current.state}` : ''}
                  {current?.user_id ? ' · linked account' : ' · number not linked to an account'}
                </div>
              </div>
              {heldNow && (
                <div className="flex gap-2">
                  <button
                    className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40 disabled:opacity-50"
                    disabled={busy !== ''}
                    onClick={claim}
                    title={`Restarts the ${timeoutMin}-minute timer without sending anything`}
                  >
                    {busy === 'claim' ? '…' : "I'm on it"}
                  </button>
                  <button
                    className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40 disabled:opacity-50"
                    disabled={busy !== ''}
                    onClick={release}
                  >
                    {busy === 'release' ? '…' : 'Hand back to bot'}
                  </button>
                </div>
              )}
            </div>

            <div className="flex-1 overflow-y-auto px-3 py-3 space-y-2" style={{ maxHeight: '55vh' }}>
              {threadLoading && !thread ? (
                <p className="text-sm opacity-70">Loading…</p>
              ) : !shown.msgs.length && !shown.local.length ? (
                <p className="text-sm opacity-60">No messages stored for this chat.</p>
              ) : (
                shown.msgs.map((m, i) => {
                  const inbound = m.direction === 'inbound'
                  return (
                    <div key={m.id || `${m.at}-${i}`} className={`flex ${inbound ? 'justify-start' : 'justify-end'}`}>
                      <div
                        className={`max-w-[80%] rounded-lg px-3 py-2 text-sm whitespace-pre-wrap ${
                          inbound ? 'bg-white/10' : m.relay ? 'bg-emerald-500/20' : 'bg-white/20'
                        }`}
                      >
                        {Array.isArray(m.photos) && m.photos.length > 0 && (
                          // Photos the person texted: signed links (an hour),
                          // from the ops-only thread route.
                          <div className="mb-1 flex flex-wrap gap-1">
                            {m.photos.map((u, j) => (
                              <a key={j} href={u} target="_blank" rel="noopener noreferrer">
                                <img src={u} alt={`Photo ${j + 1} from them`} className="h-32 max-w-full rounded object-cover" />
                              </a>
                            ))}
                          </div>
                        )}
                        {m.text && <div>{m.text}</div>}
                        <div className="mt-1 text-[10px] opacity-60">
                          {inbound ? 'them' : m.relay ? "supporter's words, relayed" : 'HO:RA'} · {formatWhen(m.at)}
                        </div>
                      </div>
                    </div>
                  )
                })
              )}
              {shown.local.map((p) => (
                // Ours, not yet stored: "sending" until the server takes it
                // (202), "sent" until the stored row replaces it.
                <div key={p.key} className="flex justify-end">
                  <div
                    className={`max-w-[80%] rounded-lg px-3 py-2 text-sm whitespace-pre-wrap ${
                      p.status === 'failed' ? 'border border-red-400/50 bg-red-500/10' : 'bg-white/20'
                    } ${p.status === 'sending' ? 'opacity-60' : ''}`}
                  >
                    <div>{p.text}</div>
                    <div className="mt-1 text-[10px] opacity-70">
                      {p.status === 'sending' && 'HO:RA · sending…'}
                      {p.status === 'queued' && 'HO:RA · sent'}
                      {p.status === 'failed' && (
                        <span className="text-red-300">
                          Not sent — {p.error}{' '}
                          <button className="underline" onClick={() => retry(p)}>Retry</button>
                          {' · '}
                          <button className="underline" onClick={() => discard(p)}>Discard</button>
                        </span>
                      )}
                    </div>
                  </div>
                </div>
              ))}
              <div ref={bottomRef} />
            </div>

            {heldNow && (
              <div className="border-t border-white/10 p-3 space-y-2">
                <div className="flex items-center justify-between">
                  <h3 className="text-xs font-semibold uppercase tracking-wide opacity-70">Fix the request</h3>
                  <span className="text-[10px] opacity-60">Internal — never sent to them</span>
                </div>
                {busy === 'edit' ? (
                  <EditProgress start={editStart} times={editTimes} />
                ) : edit ? (
                  <div className="space-y-2">
                    {edit.changes?.length ? (
                      <table className="w-full text-xs">
                        <thead>
                          <tr className="opacity-60 text-left"><th className="pr-2">Field</th><th className="pr-2">Before</th><th>After</th></tr>
                        </thead>
                        <tbody>
                          {edit.changes.map((c) => (
                            <tr key={c.field} className="align-top">
                              <td className="pr-2 opacity-70">{FIELD_LABEL[c.field] || c.field}</td>
                              <td className="pr-2 line-through opacity-60 whitespace-pre-wrap break-words">{c.before || '—'}</td>
                              <td className="whitespace-pre-wrap break-words">{c.after || '—'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    ) : (
                      <p className="text-xs opacity-70">Nothing changed.</p>
                    )}
                    <DraftCard d={edit.after} title={edit.created ? 'New request (from the chat and your note)' : 'After your note'} />
                  </div>
                ) : draft?.draft ? (
                  <DraftCard d={draft.draft} title="Their request now" />
                ) : (
                  <p className="text-xs opacity-60">No request yet. A note starts one from what they wrote plus your note.</p>
                )}
                <div className="flex gap-2">
                  <textarea
                    className="flex-1 rounded-md border border-amber-300/30 bg-transparent px-3 py-2 text-sm"
                    rows={2}
                    maxLength={1000}
                    placeholder={NOTE_EXAMPLES}
                    value={note}
                    onChange={(e) => setNote(e.target.value)}
                    disabled={busy !== ''}
                  />
                  <button
                    className="rounded-md border border-white/30 px-3 text-sm hover:bg-white/10 disabled:opacity-50"
                    disabled={busy !== '' || !note.trim()}
                    onClick={applyNote}
                  >
                    {busy === 'edit' ? 'Applying…' : 'Apply'}
                  </button>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <span className="text-[11px] opacity-60">
                    {ready
                      ? 'They get the summary with the price and reply YES themselves. Nothing is booked before that.'
                      : missing ? `Still missing: ${missing}.` : ''}
                  </span>
                  <button
                    className="rounded-md border border-emerald-400/40 bg-emerald-500/10 px-3 py-1 text-xs hover:bg-emerald-500/20 disabled:opacity-50"
                    disabled={busy !== '' || !ready}
                    onClick={sendSummary}
                  >
                    {busy === 'summary' ? '…' : 'Send summary and hand back'}
                  </button>
                </div>
              </div>
            )}

            <div className="border-t border-white/10 p-3">
              {heldNow ? (
                <div className="flex gap-2">
                  <textarea
                    className="flex-1 rounded-md border border-white/20 bg-transparent px-3 py-2 text-sm"
                    rows={2}
                    maxLength={1500}
                    placeholder="Reply as HO:RA… (Enter to send, Shift+Enter for a new line)"
                    value={text}
                    onChange={(e) => setText(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); if (!handingBack) send() }
                    }}
                    disabled={handingBack}
                  />
                  <button
                    className="rounded-md border border-white/30 bg-white/10 px-4 text-sm hover:bg-white/20 disabled:opacity-50"
                    disabled={handingBack || !text.trim()}
                    onClick={send}
                  >
                    Send
                  </button>
                </div>
              ) : (
                <p className="text-xs opacity-60">
                  This chat is with the bot — nothing you type here would be sent. If the person asks for a human,
                  the bot hands it over and it appears under “Needs a person”.
                </p>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
