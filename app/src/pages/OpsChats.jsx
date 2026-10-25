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
} from '../api/ops'

const REFRESH_MS = 10_000

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
  const [busy, setBusy] = useState('')            // 'send' | 'claim' | 'release'
  const [now, setNow] = useState(Date.now())
  const bottomRef = useRef(null)

  async function loadList() {
    try {
      const data = await listChannelConversations()
      setConvs(Array.isArray(data?.conversations) ? data.conversations : [])
      if (Number.isFinite(data?.handoff_timeout_minutes)) setTimeoutMin(data.handoff_timeout_minutes)
    } catch (err) {
      console.error('[ops/chats] list', err)
      toast(err.message || "Couldn't load chats")
    } finally {
      setLoading(false)
    }
  }

  async function loadThread(id, { quiet = false } = {}) {
    if (!id) return
    if (!quiet) setThreadLoading(true)
    try {
      const data = await getChannelThread(id)
      setThread(data)
    } catch (err) {
      console.error('[ops/chats] thread', err)
      if (!quiet) toast(err.message || "Couldn't load that chat")
    } finally {
      if (!quiet) setThreadLoading(false)
    }
  }

  // The list and the open thread refresh on a timer; the page is left open
  // on a second screen while a hand-off is live. Paused while the tab is
  // hidden — nobody is reading it, and the ops allowlist is small.
  useEffect(() => {
    loadList()
    const tick = () => {
      if (document.visibilityState === 'hidden') return
      setNow(Date.now())
      loadList()
      if (selected) loadThread(selected, { quiet: true })
    }
    const t = setInterval(tick, REFRESH_MS)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected])

  useEffect(() => {
    if (!selected) { setThread(null); return }
    setText('')
    loadThread(selected)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: 'end' })
  }, [thread?.messages?.length])

  const ordered = useMemo(() => {
    const held = convs.filter((c) => c.state === 'handoff')
    const rest = convs.filter((c) => c.state !== 'handoff')
    held.sort((a, b) => new Date(a.updated_at) - new Date(b.updated_at)) // oldest waiting first
    return { held, rest }
  }, [convs])

  const current = convs.find((c) => c.identity_id === selected) || null
  const heldNow = current?.state === 'handoff'

  // Minutes until the bot takes the chat back, from the server's clock.
  function minutesLeft(c) {
    if (c.state !== 'handoff' || !c.handoff_activity_at) return null
    const end = new Date(c.handoff_activity_at).getTime() + timeoutMin * 60_000
    return Math.max(0, Math.ceil((end - now) / 60_000))
  }

  async function send() {
    const t = text.trim()
    if (!t || !selected) return
    setBusy('send')
    try {
      await sendChannelMessage(selected, t)
      setText('')
      // Queued, not yet delivered: the thread shows it once the store has
      // the outbound row, which the next refresh picks up.
      await loadThread(selected, { quiet: true })
      await loadList()
    } catch (err) {
      toast(err.message || "Couldn't send")
      if (err.status === 409) await loadList()
    } finally {
      setBusy('')
    }
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
          <button className="rounded-md border border-white/20 px-2 py-0.5 text-xs hover:border-white/40" onClick={loadList}>
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
              ) : !thread?.messages?.length ? (
                <p className="text-sm opacity-60">No messages stored for this chat.</p>
              ) : (
                thread.messages.map((m, i) => {
                  const inbound = m.direction === 'inbound'
                  return (
                    <div key={`${m.at}-${i}`} className={`flex ${inbound ? 'justify-start' : 'justify-end'}`}>
                      <div
                        className={`max-w-[80%] rounded-lg px-3 py-2 text-sm whitespace-pre-wrap ${
                          inbound ? 'bg-white/10' : m.relay ? 'bg-emerald-500/20' : 'bg-white/20'
                        }`}
                      >
                        <div>{m.text}</div>
                        <div className="mt-1 text-[10px] opacity-60">
                          {inbound ? 'them' : m.relay ? "supporter's words, relayed" : 'HO:RA'} · {formatWhen(m.at)}
                        </div>
                      </div>
                    </div>
                  )
                })
              )}
              <div ref={bottomRef} />
            </div>

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
                      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
                    }}
                    disabled={busy !== ''}
                  />
                  <button
                    className="rounded-md border border-white/30 bg-white/10 px-4 text-sm hover:bg-white/20 disabled:opacity-50"
                    disabled={busy !== '' || !text.trim()}
                    onClick={send}
                  >
                    {busy === 'send' ? 'Sending…' : 'Send'}
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
