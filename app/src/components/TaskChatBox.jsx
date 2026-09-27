import { useMemo, useCallback, useEffect, useState } from 'react'
import Talk from 'talkjs'
import { Session, Chatbox } from '@talkjs/react'
import { api } from '../api/client'
import { CHAT_BLOCKED_LINE } from '../lib/safety'

/**
 * TaskChatBox
 * Renders TalkJS chat only after the task is assigned and TalkJS is ready.
 * Names are the server's (task.requester_name / assignee_name, me.name from
 * /auth/me); the emails are TalkJS ids only and are never shown as a name.
 * props:
 *  - task: { id, title, requester, assigned_to, requester_name, assignee_name }
 *  - me:   { email, name }
 *  - height?: number (default 320)
 *  - className?: string
 */
export default function TaskChatBox({ task, me, height, fullscreen = false, className = '' }) {
  const resolvedHeight = fullscreen ? '100%' : (height ?? Math.min(Math.round(window.innerHeight * 0.55), 600))
  const appId = import.meta.env.VITE_TALKJS_APP_ID
  const assigned = task?.assigned_to

  // 1) Wait for TalkJS to be ready before constructing Talk.User
  const [ready, setReady] = useState(false)
  useEffect(() => {
    let alive = true
    Talk.ready.then(() => { if (alive) setReady(true) })
    return () => { alive = false }
  }, [])

  // 1b) Identity verification signature (talkjs.com/docs/authentication) —
  // best-effort: TalkJS ignores a missing/stale signature until "Enforce
  // identity verification" is switched on in the dashboard, so a failed
  // fetch here degrades to today's unverified behavior rather than
  // blocking chat.
  const [signature, setSignature] = useState(null)
  useEffect(() => {
    if (!me?.email) return
    let alive = true
    api('/talkjs/signature')
      .then((res) => { if (alive) setSignature(res?.signature ?? null) })
      .catch(() => {})
    return () => { alive = false }
  }, [me?.email])

  // 2) Current user for TalkJS (only once ready)
  const syncUser = useCallback(() => {
    if (!ready || !me?.email) return null
    return new Talk.User({
      id: me.email,
      name: me.name || 'You',
      email: me.email,
      role: 'default',
    })
  }, [ready, me])

  // 3) The other participant (author vs assignee)
  const iAmRequester = !!task && !!me?.email && me.email === task.requester
  const otherEmail = useMemo(() => {
    if (!task || !me?.email) return ''
    return iAmRequester ? task.assigned_to || '' : task.requester || ''
  }, [task, me, iAmRequester])
  const otherName = iAmRequester ? task?.assignee_name : task?.requester_name

  // A block between the two (server/safety.go): both seats read-only. The
  // server already set this through TalkJS's REST API; passing it here too
  // keeps a conversation first opened AFTER the block from being created
  // read-write. Unblocked, access is left unset — TalkJS keeps whatever the
  // server set, so opening the chat can never undo a block.
  const blocked = task?.chat_blocked === true
  const access = blocked ? { access: 'Read' } : undefined

  // 4) Conversation for this task
  const syncConversation = useCallback(
    (session) => {
      if (!ready || !task?.id) return null
      const conv = session.getOrCreateConversation(`task_${task.id}`)
      conv.setParticipant(session.me, access)
      if (otherEmail) {
        const other = new Talk.User({
          id: otherEmail,
          name: otherName || 'Your HO:RA contact',
          email: otherEmail,
          role: 'default',
        })
        conv.setParticipant(other, access)
      }
      conv.setAttributes({ subject: task.title ?? 'Task', custom: { taskId: task.id } })
      return conv
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [ready, task?.id, task?.title, otherEmail, otherName, blocked]
  )

  // Guards & placeholders
  if (!appId) {
    return <div className="text-xs text-red-400">Missing VITE_TALKJS_APP_ID</div>
  }
  if (!assigned) {
    return (
      <div className={`h-56 rounded bg-white/5 border border-white/10 flex items-center justify-center text-xs text-white/60 ${className}`}>
        Accept the task to open chat
      </div>
    )
  }
  if (!ready) {
    return (
      <div className={`h-56 rounded bg-white/5 border border-white/10 flex items-center justify-center text-xs text-white/60 ${className}`}>
        Loading chat…
      </div>
    )
  }
  if (!me?.email) return null

  return (
    <Session appId={appId} syncUser={syncUser} signature={signature ?? undefined}>
      <Chatbox
        key={blocked ? 'read-only' : 'read-write'}
        syncConversation={syncConversation}
        className={fullscreen ? className : `rounded bg-white/5 border border-white/10 ${className}`}
        style={{ width: '100%', height: blocked && fullscreen ? 'calc(100% - 64px)' : resolvedHeight }}
        messageField={blocked ? { visible: false } : { placeholder: 'Type here…' }}
        showChatHeader={!fullscreen}
      />
      {blocked && (
        <p className="px-4 py-3 text-xs text-white/60 border-t border-white/10">{CHAT_BLOCKED_LINE}</p>
      )}
    </Session>
  )
}