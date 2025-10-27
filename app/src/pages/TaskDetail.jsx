import { useEffect, useMemo, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import TaskChatBox from '../components/TaskChatBox'
import { useAuth } from '../auth/AuthContext'
import UserPill from '../components/UserPill'
import { gmapsPlaceUrl, gmapsDirectionsUrl } from '../utils/gmaps'
import { useLoader } from '../providers/LoaderProvider.jsx'

const MINUTE_RATE_EUR = 0.5

export default function TaskDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  // const { user, loading: authLoading } = useAuth()
  const { user } = useAuth()
  const { wrap } = useLoader()
  const [task, setTask] = useState(null)
  const [work, setWork] = useState({ items: [], total_minutes: 0, total_cost_cents: 0, has_open: false })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)


  // 編輯表單狀態
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [category, setCategory] = useState('task')
  const [locations, setLocations] = useState([''])
  const [minutes, setMinutes] = useState(30)
  const [prepay, setPrepay] = useState('')
  const [mode, setMode] = useState('now')
  const [date, setDate] = useState('')
  const [timeStr, setTimeStr] = useState('')

useEffect(() => {
  if (task) {
    window.__TASK__ = task
    console.debug('[TASK]', task)
  }
}, [task])


  const locs = (() => {
  // 1) 嘗試讀 locations_geo（支援 string / null / []）
  let geo = task?.locations_geo
  if (typeof geo === 'string') {
    try { geo = JSON.parse(geo) } catch { geo = [] }
  }
  if (Array.isArray(geo) && geo.length > 0) {
    // 只保留有 label 或座標/ID 的點
    return geo.filter(p => p && (p.label || p.placeId || p.id || (p.lat != null && p.lng != null)))
  }

  // 2) 回退到舊的 location_text（用 ' | ' 分隔）
  const labels = (task?.location_text || '')
    .split(' | ')
    .map(s => s.trim())
    .filter(Boolean)

  return labels.map(label => ({ label }))
  })()

  // ✅ 單一 effect：等 auth ready 再抓 任務 + 工時
  useEffect(() => {
    // if (authLoading || !user || !id) return
    if (!user || !id) return
    let alive = true
    // ;(async () => {
    //   setLoading(true)
    //   setError('')
    //   try {
    //     const t = await api(`/tasks/${id}`)
    //     if (!alive) return
    //     setTask(t)

    //     // 作者或接單者可看工時；403 就忽略
    //     try {
    //       const w = await api(`/tasks/${id}/worklogs`)
    //       if (alive) setWork(w)
    //     } catch {/* ignore */}
    //   } catch (e) {
    //     if (alive) setError(e.message || 'Failed to load')
    //   } finally {
    //     if (alive) setLoading(false)
    //   }
    // })()
     ;(async () => {
      setError('')
      await wrap(async () => {
        const t = await api(`/tasks/${id}`)
        if (!alive) return
        setTask(t)
        // 作者或接單者可看工時；403 就忽略
        try {
          const w = await api(`/tasks/${id}/worklogs`)
          if (alive) setWork(w)
        } catch {/* ignore */}
      }).catch((e) => {
        if (alive) setError(e.message || 'Failed to load')
      })
    })()
    return () => { alive = false }
  }, [ user, id])

  // ✅ 用 UUID 判斷身分
  const isOwner    = Boolean(user?.id && task?.requester_id && user.id === task.requester_id)
  const isAssignee = Boolean(user?.id && task?.assigned_to_id && user.id === task.assigned_to_id)
  const hasLogged  = (work.total_minutes || 0) > 0
  const canComplete = Boolean((isOwner || isAssignee) && task?.status === 'open' && !!task?.assigned_to_id && !work.has_open && hasLogged)
  const canAccept   = Boolean(!isOwner && !isAssignee && task?.status === 'open' && !task?.assigned_to_id)

  // async function reloadWorkAndTask() {
  //   const [t, w] = await Promise.all([
  //     api(`/tasks/${id}`),
  //     api(`/tasks/${id}/worklogs`).catch(() => work),
  //   ])
  //   setTask(t)
  //   setWork(w)
  // }
   async function reloadWorkAndTask() {
   await wrap(async () => {
     const [t, w] = await Promise.all([
       api(`/tasks/${id}`),
       api(`/tasks/${id}/worklogs`).catch(() => work),
     ])
     setTask(t)
     setWork(w)
   })
 }

  async function acceptFromDetail() {
    // try {
    //   await api(`/tasks/${id}/accept`, { method: 'POST' })
    //   await reloadWorkAndTask()
    // } catch (e) {
    //   alert(e.message || 'Failed to accept')
    // }
    await wrap(async () => {
     try {
       await api(`/tasks/${id}/accept`, { method: 'POST' })
       await reloadWorkAndTask()
     } catch (e) {
       alert(e.message || 'Failed to accept')
     }
   })
  }

  async function clockIn() {
    // try {
    //   await api(`/tasks/${id}/clock-in`, { method: 'POST' })
    //   await reloadWorkAndTask()
    // } catch (e) {
    //   alert(e.message || 'Clock in failed')
    // }
    await wrap(async () => {
     try {
       await api(`/tasks/${id}/clock-in`, { method: 'POST' })
       await reloadWorkAndTask()
     } catch (e) {
       alert(e.message || 'Clock in failed')
     }
   })
  }
  async function clockOut() {
    // try {
    //   await api(`/tasks/${id}/clock-out`, { method: 'POST' })
    //   await reloadWorkAndTask()
    // } catch (e) {
    //   alert(e.message || 'Clock out failed')
    // }
     await wrap(async () => {
     try {
       await api(`/tasks/${id}/clock-out`, { method: 'POST' })
       await reloadWorkAndTask()
     } catch (e) {
       alert(e.message || 'Clock out failed')
     }
   })
  }

  async function markCompleted() {
    // try {
    //   await api(`/tasks/${id}/complete`, { method: 'POST' })
    //   navigate('/my?tab=done', { replace: true })
    // } catch (e) {
    //   alert(e.message || 'Complete failed')
    // }
    await wrap(async () => {
     try {
       await api(`/tasks/${id}/complete`, { method: 'POST' })
       navigate('/my?tab=done', { replace: true })
     } catch (e) {
       alert(e.message || 'Complete failed')
     }
   })
  }

  // 進入編輯模式時把 task 值灌入表單（與你原本相同邏輯）
  function startEdit() {
    if (!task) return
    setTitle(task.title || '')
    setDescription(task.description || '')
    setCategory(task.category || 'task')
    const locs = (task.location_text || '').split(' | ').map(s=>s.trim()).filter(Boolean)
    setLocations(locs.length ? locs : [''])
    setMinutes(task.estimated_minutes || 30)
    setPrepay(((task.prepay_amount_cents || 0) / 100).toString())
    if (task.is_immediate) {
      setMode('now'); setDate(''); setTimeStr('')
    } else if (task.scheduled_at) {
      const d = new Date(task.scheduled_at)
      const pad = (n) => String(n).padStart(2, '0')
      setMode('schedule')
      setDate(`${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}`)
      setTimeStr(`${pad(d.getHours())}:${pad(d.getMinutes())}`)
    } else {
      setMode('now'); setDate(''); setTimeStr('')
    }
    setEditing(true)
  }

  const scheduledAtISO = useMemo(() => {
    if (mode === 'schedule' && date && timeStr) return new Date(`${date}T${timeStr}`).toISOString()
    return ''
  }, [mode, date, timeStr])

  const timeCost = useMemo(() => Number(minutes || 0) * MINUTE_RATE_EUR, [minutes])
  const advance  = useMemo(() => {
    if (prepay === '') return 0
    const n = Number(prepay)
    return Number.isNaN(n) ? 0 : Math.max(0, n)
  }, [prepay])
  const totalEUR = useMemo(() => (work.total_cost_cents || 0) / 100, [work.total_cost_cents])

  async function saveEdit() {
    // try {
    //   const location_text = locations.map(s=>s.trim()).filter(Boolean).join(' | ')
    //   const payload = {
    //     title,
    //     description,
    //     category,
    //     location_text,
    //     estimated_minutes: Number(minutes) || 30,
    //     prepay_amount_cents: Math.round((advance || 0) * 100),
    //     is_immediate: mode === 'now',
    //     scheduled_at: mode === 'schedule' ? scheduledAtISO : '',
    //   }
    //   const updated = await api(`/tasks/${task.id}`, { method: 'PATCH', body: payload })
    //   setTask(updated)
    //   setEditing(false)
    // } catch (e) {
    //   alert(e.message || 'Failed to save')
    // }
     await wrap(async () => {
     try {
       const location_text = locations.map(s=>s.trim()).filter(Boolean).join(' | ')
       const payload = { 
         title,
        description,
        category,
        location_text,
        estimated_minutes: Number(minutes) || 30,
        prepay_amount_cents: Math.round((advance || 0) * 100),
        is_immediate: mode === 'now',
        scheduled_at: mode === 'schedule' ? scheduledAtISO : '',
        }
       const updated = await api(`/tasks/${task.id}`, { method: 'PATCH', body: payload })
       setTask(updated)
       setEditing(false)
     } catch (e) {
       alert(e.message || 'Failed to save')
     }
   })
  }

  // if (loading) return <div className="p-6">Loading…</div>
  if (error)   return <div className="p-6 text-red-500">{error}</div>
  if (!task)   return <div className="p-6">Task not found.</div>

  const advanceEUR = (task.prepay_amount_cents || 0) / 100
  const whenText = task.is_immediate ? 'ASAP' : (task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : '—')


  function DebugMe() {
  const { user } = useAuth()
  useEffect(() => {
    // 直接把目前登入者掛到 window，方便 Console 查
    window.__ME__ = { id: user?.id, email: user?.email }
    console.debug('[ME]', window.__ME__)
  }, [user])
  return null
}

  return (
    <div className="bg-linear-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
      <div className="mx-auto max-w-md space-y-4 border border-primary/30 backdrop-blur-md p-8 rounded-lg shadow">
        {!editing ? (
          <>
            <div className="flex items-start gap-3">
              <h2 className="text-2xl font-semibold flex-1">{task.title}</h2>
              <span className="inline-flex h-6 items-center rounded-full border border-white/15 bg-white/5 px-2 text-[11px] uppercase tracking-wide text-white/80 select-none pointer-events-none">
                {task.status}
              </span>
              {canAccept && (
                <button onClick={acceptFromDetail} className="ml-2 rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40">
                  Accept
                </button>
              )}
              {canComplete ? (
                <button onClick={markCompleted} className="ml-2 rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40">
                  Mark completed
                </button>
              ) : ((isOwner || isAssignee) && task?.status === 'open') ? (
                <button disabled className="ml-2 text-xs opacity-60 border border-white/10 px-2 py-1 rounded-md cursor-not-allowed" title="Clock in & out at least once to complete">
                  Complete (needs clock)
                </button>
              ) : null}
              {isOwner && task.status === 'open' && (
                <button onClick={startEdit} className="ml-2 rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40">
                  Edit
                </button>
              )}
            </div>

            <div className="mt-3 grid gap-2 sm:grid-cols-2">
              <div className="flex items-center gap-3">
                <div className="text-white/70 text-sm">Requester:</div>
                <UserPill userId={task.requester_id} meId={user?.id} label="Requester" />
              </div>
              <div className="flex items-center gap-3">
                <div className="text-white/70 text-sm">Supporter:</div>
                {task.assigned_to_id
                  ? <UserPill userId={String(task.assigned_to_id)} meId={user?.id} label="Assignee" />
                  : <span className="text-white/60 text-sm">Not assigned yet</span>}
              </div>
            </div>

            <div className="text-sm text-white/80 space-y-1">
              <div><b>When:</b> {whenText}</div>
              <div><b>Estimated:</b> {task.estimated_minutes} min</div>
              <div><b>Advance:</b> {advanceEUR.toFixed(2)} EUR</div>
              {/* <div><b>Locations:</b> {task.location_text || '—'}</div> */}
              <div>  <b>Locations:</b>{' '}
                {locs.length ? (
                  <span className="inline-flex flex-wrap gap-1 align-middle">
                    {locs.map((p, i) => (
                      <a
                        key={i}
                        href={gmapsPlaceUrl(p)}
                        target="_blank" rel="noopener noreferrer"
                        className="inline-flex items-center gap-1 rounded-full border border-white/20 px-2 py-0.5 text-xs hover:border-white/40"
                        title="Open in Google Maps"
                      >
                        {/* 你可以換成自己的 icon */}
                        <span aria-hidden>📍</span>
                        {p.label || 'Open in Maps'}
                      </a>
                    ))}
                  </span>
                ) : '—'}
                </div>
                {/* （可選）兩點以上顯示一鍵路線 */}
                {locs.length >= 2 && (
                  <div className="mt-2">
                    <a
                      href={gmapsDirectionsUrl(locs)}
                      target="_blank" rel="noopener noreferrer"
                      className="inline-flex items-center gap-2 rounded-md border border-white/20 px-3 py-1 text-xs hover:border-white/40"
                      title="Open route in Google Maps"
                    >
                      🧭 Open route
                    </a>
                  </div>
                )}
                <div className="text-xs opacity-80">ID: {task.id}</div>
            </div>

            <div className="border border-white/20 rounded-md p-3 whitespace-pre-wrap">
              {task.description || 'No description.'}
            </div>
            <DebugMe />

            {(isOwner || isAssignee) && (
              <div className="border border-white/20 rounded-md p-3">
                <div className="flex items-center justify-between">
                  <div className="text-sm">Logged: <b>{work.total_minutes} min</b> · Est. <b>{totalEUR.toFixed(2)} EUR</b></div>
                  {isAssignee && task.status === 'open' && (
                    work.has_open ? (
                      <button onClick={clockOut} className="text-xs rounded-md border border-white/20 px-2 py-1 hover:border-white/40">
                        Clock out
                      </button>
                    ) : (
                      <button onClick={clockIn} className="text-xs rounded-md border border-white/20 px-2 py-1 hover:border-white/40">
                        Clock in
                      </button>
                    )
                  )}
                </div>
              </div>
            )}

            <div className="border border-white/20 rounded-md p-3">
              <div className="text-sm mb-2">Chat</div>
              <TaskChatBox task={task} me={user} height={320} />
            </div>
          </>
        ) : (
          /* 編輯區（原本邏輯保留） */
          /* ... */
          null
        )}
      </div>
    </div>
  )
}