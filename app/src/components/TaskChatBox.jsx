import { useMemo, useCallback, useEffect, useState } from 'react'
import Talk from 'talkjs'
import { Session, Chatbox } from '@talkjs/react'

function deriveName(email) {
  if (!email) return 'User'
  const i = email.indexOf('@')
  return (i > 0 ? email.slice(0, i) : email).replace(/\./g, ' ')
}

/**
 * TaskChatBox
 * Renders TalkJS chat only after the task is assigned and TalkJS is ready.
 * props:
 *  - task: { id, title, requester, assigned_to }
 *  - me:   { email, name }
 *  - height?: number (default 320)
 *  - className?: string
 */
export default function TaskChatBox({ task, me, height = 320, className = '' }) {
  const appId = import.meta.env.VITE_TALKJS_APP_ID
  const assigned = task?.assigned_to

  // 1) Wait for TalkJS to be ready before constructing Talk.User
  const [ready, setReady] = useState(false)
  useEffect(() => {
    let alive = true
    Talk.ready.then(() => { if (alive) setReady(true) })
    return () => { alive = false }
  }, [])

  // 2) Current user for TalkJS (only once ready)
  const syncUser = useCallback(() => {
    if (!ready || !me?.email) return null
    return new Talk.User({
      id: me.email,
      name: me.name || deriveName(me.email),
      email: me.email,
      role: 'default',
    })
  }, [ready, me])

  // 3) The other participant (author vs assignee)
  const otherEmail = useMemo(() => {
    if (!task || !me?.email) return ''
    return me.email === task.requester ? task.assigned_to || '' : task.requester || ''
  }, [task, me])

  // 4) Conversation for this task
  const syncConversation = useCallback(
    (session) => {
      if (!ready || !task?.id) return null
      const conv = session.getOrCreateConversation(`task_${task.id}`)
      conv.setParticipant(session.me)
      if (otherEmail) {
        const other = new Talk.User({
          id: otherEmail,
          name: deriveName(otherEmail),
          email: otherEmail,
          role: 'default',
        })
        conv.setParticipant(other)
      }
      conv.setAttributes({ subject: task.title ?? 'Task', custom: { taskId: task.id } })
      return conv
    },
    [ready, task?.id, task?.title, otherEmail]
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
    <Session appId={appId} syncUser={syncUser}>
      <Chatbox
        syncConversation={syncConversation}
        className={`rounded bg-white/5 border border-white/10 ${className}`}
        style={{ width: '100%', height }}
      />
    </Session>
  )
}