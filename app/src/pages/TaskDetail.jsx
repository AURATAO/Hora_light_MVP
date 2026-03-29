import { useEffect, useMemo, useRef, useState } from 'react'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { api } from '../api/client'
import TaskChatBox from '../components/TaskChatBox'
import { useAuth } from '../auth/AuthContext'
import UserPill from '../components/UserPill'
import { gmapsPlaceUrl, gmapsDirectionsUrl } from '../utils/gmaps'
import { useLoader } from '../providers/LoaderProvider.jsx'
import PlaceInput from '../components/PlaceInput'
import { useToast } from '../providers/ToastProvider'


export default function TaskDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  // const { user, loading: authLoading } = useAuth()
  const { user } = useAuth()
  const { wrap } = useLoader()
  const toast = useToast()
  const [task, setTask] = useState(null)
  const [work, setWork] = useState({ items: [], total_minutes: 0, total_cost_cents: 0, has_open: false })
  const [gpsPos, setGpsPos] = useState(null)       // { lat, lng, accuracy } — assignee live
  const [gpsError, setGpsError] = useState('')
  const [latestGps, setLatestGps] = useState(null)  // last saved ping — shown to requester
  const watchIdRef = useRef(null)
  const pingIntervalRef = useRef(null)

  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const [chatOpen, setChatOpen] = useState(false)

  // Travel time estimate (supporter only, after task is accepted)
  const [travelEst, setTravelEst] = useState(null)   // { travel_minutes, task_minutes, total_minutes }
  const [travelLoading, setTravelLoading] = useState(false)
  const travelCalledRef = useRef(false)



  // 編輯表單狀態
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [category, setCategory] = useState('task')
  const [locations, setLocations] = useState([{ label: '' }])
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

  // Auto-fetch travel estimate for the assignee (supporter) once task is accepted
  useEffect(() => {
    if (!task?.assigned_to_id) return
    if (!user?.id || task.assigned_to_id !== user.id) return
    if (travelCalledRef.current) return

    // If already stored in task, use it directly
    if (task.travel_time_minutes != null && task.total_estimate_minutes != null) {
      setTravelEst({
        travel_minutes: task.travel_time_minutes,
        task_minutes: task.estimated_minutes,
        total_minutes: task.total_estimate_minutes,
      })
      travelCalledRef.current = true
      return
    }

    if (!navigator.geolocation) return
    travelCalledRef.current = true
    setTravelLoading(true)

    navigator.geolocation.getCurrentPosition(
      async (pos) => {
        try {
          const res = await api(`/tasks/${task.id}/estimate-travel`, {
            method: 'POST',
            body: {
              supporter_lat: pos.coords.latitude,
              supporter_lng: pos.coords.longitude,
            },
          })
          setTravelEst(res)
        } catch {
          // silently ignore — GPS denied or Maps error
        } finally {
          setTravelLoading(false)
        }
      },
      () => setTravelLoading(false), // GPS denied
      { timeout: 8000 }
    )
  }, [task?.assigned_to_id, task?.id, user?.id])

  // ?review=true → redirect to review page (for old completion emails)
  useEffect(() => {
    if (!task || !user) return
    const params = new URLSearchParams(location.search)
    if (params.get('review') !== 'true') return
    if (task.status !== 'completed') return
    if (task.requester_id !== user.id) return
    navigate(`/tasks/${id}/review`, { replace: true })
  }, [task, user, location.search, id, navigate])

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
      ; (async () => {
        setError('')
        await wrap(async () => {
          const t = await api(`/tasks/${id}`)
          if (!alive) return
          setTask(t)
          // 作者或接單者可看工時；403 就忽略
          try {
            const w = await api(`/tasks/${id}/worklogs`)
            if (alive) setWork(w)
          } catch {/* ignore */ }
        }).catch((e) => {
          if (alive) setError(e.message || 'Failed to load')
        })
      })()
    return () => { alive = false }
  }, [user, id])

  function normalizeLocationItem(x) {
    if (!x) return { label: '' }
    if (typeof x === 'string') return { label: x }
    if (typeof x === 'object') {
      const label = x.label || x.description || x.formatted || ''
      return { ...x, label }
    }
    return { label: String(x) }
  }

  function addLocation() {
    setLocations(prev => [...prev, { label: '' }])
  }

  function updateLocation(i, v) {
    setLocations(prev =>
      prev.map((x, idx) => (idx === i ? normalizeLocationItem(v) : x))
    )
  }

  function removeLocation(i) {
    setLocations(prev => prev.filter((_, idx) => idx !== i))
  }

  const isActivelyWorking = Boolean(
    user?.id && task?.assigned_to_id && user.id === task.assigned_to_id && work.has_open
  )
  const isOwnerWatching = Boolean(
    user?.id && task?.requester_id && user.id === task.requester_id && task?.assigned_to_id && task.status === 'open'
  )

  // Assignee: watch GPS + ping backend every 30s while clocked in
  useEffect(() => {
    if (!isActivelyWorking) {
      if (watchIdRef.current != null) {
        navigator.geolocation.clearWatch(watchIdRef.current)
        watchIdRef.current = null
      }
      clearInterval(pingIntervalRef.current)
      pingIntervalRef.current = null
      return
    }
    if (!navigator.geolocation) {
      setGpsError('GPS not supported on this device')
      return
    }
    let lastPos = null
    watchIdRef.current = navigator.geolocation.watchPosition(
      (pos) => {
        lastPos = { lat: pos.coords.latitude, lng: pos.coords.longitude, accuracy: Math.round(pos.coords.accuracy) }
        setGpsPos(lastPos)
        setGpsError('')
      },
      (err) => setGpsError(err.message),
      { enableHighAccuracy: true, maximumAge: 5000 }
    )
    // ping backend every 30s
    pingIntervalRef.current = setInterval(async () => {
      if (!lastPos) return
      try {
        await api(`/tasks/${id}/gps-ping`, {
          method: 'POST',
          body: { lat: lastPos.lat, lng: lastPos.lng, accuracy: lastPos.accuracy },
        })
      } catch { /* silent — don't interrupt UX */ }
    }, 30_000)
    return () => {
      if (watchIdRef.current != null) {
        navigator.geolocation.clearWatch(watchIdRef.current)
        watchIdRef.current = null
      }
      clearInterval(pingIntervalRef.current)
      pingIntervalRef.current = null
    }
  }, [isActivelyWorking, id])

  // Requester: poll latest GPS ping every 30s while task is open and assigned
  useEffect(() => {
    if (!isOwnerWatching) return
    let alive = true
    async function fetchLatest() {
      try {
        const data = await api(`/tasks/${id}/gps-latest`)
        if (alive) setLatestGps(data)
      } catch { /* ignore */ }
    }
    fetchLatest()
    const interval = setInterval(fetchLatest, 30_000)
    return () => { alive = false; clearInterval(interval) }
  }, [isOwnerWatching, id])

  // ✅ 用 UUID 判斷身分
  const isOwner = Boolean(user?.id && task?.requester_id && user.id === task.requester_id)
  const isAssignee = Boolean(user?.id && task?.assigned_to_id && user.id === task.assigned_to_id)
  const hasLogged = (work.total_minutes || 0) > 0
  const canComplete = Boolean((isOwner || isAssignee) && task?.status === 'open' && !!task?.assigned_to_id && !work.has_open && hasLogged)
  const canAccept = Boolean(!isOwner && !isAssignee && task?.status === 'open' && !task?.assigned_to_id)

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
        toast(e.message || 'Failed to accept')
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
        toast(e.message || 'Clock in failed')
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
        toast(e.message || 'Clock out failed')
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
        toast(e.message || 'Complete failed')
      }
    })
  }

  // 進入編輯模式時把 task 值灌入表單（與你原本相同邏輯）
  function startEdit() {
    if (!task) return

    setTitle(task.title || '')
    setDescription(task.description || '')
    setCategory(task.category || 'task')

    // 1) 先看 locations_geo（可能是 jsonb 或字串）
    let locItems = []
    let geo = task.locations_geo

    if (typeof geo === 'string') {
      try { geo = JSON.parse(geo) } catch { geo = [] }
    }

    if (Array.isArray(geo) && geo.length > 0) {
      locItems = geo.map(normalizeLocationItem)
    } else {
      // 2) 沒有 geo → 用 location_text
      const labels = (task.location_text || '')
        .split(' | ')
        .map(s => s.trim())
        .filter(Boolean)

      locItems = labels.length
        ? labels.map(normalizeLocationItem)
        : [{ label: '' }]
    }

    setLocations(locItems)

    setMinutes(task.estimated_minutes || 30)
    setPrepay(((task.prepay_amount_cents || 0) / 100).toString())

    if (task.is_immediate) {
      setMode('now')
      setDate('')
      setTimeStr('')
    } else if (task.scheduled_at) {
      const d = new Date(task.scheduled_at)
      const pad = (n) => String(n).padStart(2, '0')
      setMode('schedule')
      setDate(`${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`)
      setTimeStr(`${pad(d.getHours())}:${pad(d.getMinutes())}`)
    } else {
      setMode('now')
      setDate('')
      setTimeStr('')
    }

    setEditing(true)
  }


  const scheduledAtISO = useMemo(() => {
    if (mode === 'schedule' && date && timeStr) return new Date(`${date}T${timeStr}`).toISOString()
    return ''
  }, [mode, date, timeStr])

  const advance = useMemo(() => {
    if (prepay === '') return 0
    const n = Number(prepay)
    return Number.isNaN(n) ? 0 : Math.max(0, n)
  }, [prepay])
  const totalEUR = useMemo(() => (work.total_cost_cents || 0) / 100, [work.total_cost_cents])
  const totalDuration = useMemo(() => {
    const m = work.total_minutes || 0
    if (m === 0) return '0 min'
    const h = Math.floor(m / 60)
    const rem = m % 60
    if (h === 0) return `${rem} min`
    if (rem === 0) return `${h}h`
    return `${h}h ${rem}min`
  }, [work.total_minutes])

  async function saveEdit() {
    await wrap(async () => {
      try {
        // 先正規化 locations
        const locItems = locations.map(normalizeLocationItem)

        // 1) 給人看的文字（後端/列表用）
        const location_text = locItems
          .map(it => (it.label || '').trim())
          .filter(Boolean)
          .join(' | ')

        // 2) 給機器用的結構（之後要落 DB 再用）
        const locations_geo = locItems
          .filter(x => (x.label || '').trim())
          .map(x => ({
            label: (x.label || '').trim(),
            placeId: x.placeId || x.id || null,
            lat: typeof x.lat === 'number' ? x.lat : null,
            lng: typeof x.lng === 'number' ? x.lng : null,
          }))

        const payload = {
          title,
          description,
          category,
          location_text,
          locations_geo,
          estimated_minutes: Number(minutes) || 30,
          prepay_amount_cents: Math.round((advance || 0) * 100),
          is_immediate: mode === 'now',
          scheduled_at: mode === 'schedule' ? scheduledAtISO : '',
        }

        const updated = await api(`/tasks/${task.id}`, {
          method: 'PATCH',
          body: payload,
        })
        setTask(updated)
        setEditing(false)
      } catch (e) {
        toast(e.message || 'Failed to save')
      }
    })
  }


  if (error) return <div className="p-6 text-red-500">{error}</div>
  if (!task) return <div className="p-6">Task not found.</div>

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
            <div className="flex items-start justify-end gap-3">

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
            <h2 className="text-2xl font-semibold flex-1">{task.title}</h2>
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
              <div><b>Advance:</b> ${advanceEUR.toFixed(2)}</div>
              {/* <div><b>Locations:</b> {task.location_text || '—'}</div> */}
              <div><b className="block mb-2">Locations:</b>{' '}
                {locs.length ? (
                  <span className="inline-flex flex-wrap gap-2 align-middle ">
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

            {/* Travel estimate card — shown to assignee only */}
            {isAssignee && task.assigned_to_id && (travelLoading || travelEst) && (
              <div className="border border-white/20 rounded-md p-3 space-y-2 text-sm">
                {travelLoading ? (
                  <div className="text-white/50 text-xs">Calculating travel time…</div>
                ) : travelEst && (
                  <>
                    <div className="flex justify-between text-white/70">
                      <span>Expected duration</span>
                      <span className="text-white font-medium">
                        {task.estimated_minutes} min
                      </span>
                    </div>
                    <a
                      href={`https://www.google.com/maps/dir/?api=1&destination=${encodeURIComponent(task.location_text || '')}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="flex justify-between text-white/70 hover:text-white transition-colors"
                    >
                      <span>Travel time</span>
                      <span className="text-white font-medium">~ {travelEst.travel_minutes} min driving ↗</span>
                    </a>
                    <div className="border-t border-white/10 pt-2 flex justify-between font-semibold">
                      <span>Total estimate</span>
                      <span>~ {travelEst.total_minutes} min</span>
                    </div>
                  </>
                )}
              </div>
            )}

            {(isOwner || isAssignee) && (
              <div className="border border-white/20 rounded-md p-3 space-y-2">
                <div className="flex items-center justify-between">
                  <div className="text-sm">Logged: <b>{totalDuration}</b> · Est. <b>${totalEUR.toFixed(2)}</b></div>
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

                {/* Assignee: live GPS while clocked in */}
                {isAssignee && work.has_open && (
                  <div className="mt-1">
                    {gpsError ? (
                      <span className="inline-flex items-center gap-1 rounded-full border border-red-400/30 bg-red-400/10 px-2 py-0.5 text-xs text-red-300">
                        ⚠ GPS: {gpsError}
                      </span>
                    ) : gpsPos ? (
                      <a
                        href={`https://www.google.com/maps?q=${gpsPos.lat},${gpsPos.lng}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex items-center gap-1 rounded-full border border-white/20 bg-white/5 px-2 py-0.5 text-xs hover:border-white/40"
                      >
                        📍 {gpsPos.lat.toFixed(5)}, {gpsPos.lng.toFixed(5)}
                        <span className="text-white/50">±{gpsPos.accuracy}m</span>
                      </a>
                    ) : (
                      <span className="inline-flex items-center gap-1 rounded-full border border-white/15 bg-white/5 px-2 py-0.5 text-xs text-white/60">
                        📡 Acquiring GPS…
                      </span>
                    )}
                  </div>
                )}

                {/* Requester: last known supporter location */}
                {isOwner && task.assigned_to_id && task.status === 'open' && (
                  <div className="mt-1">
                    <div className="text-xs text-white/50 mb-1">Supporter location</div>
                    {latestGps ? (
                      <a
                        href={`https://www.google.com/maps?q=${latestGps.lat},${latestGps.lng}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex items-center gap-1 rounded-full border border-white/20 bg-white/5 px-2 py-0.5 text-xs hover:border-white/40"
                      >
                        📍 {Number(latestGps.lat).toFixed(5)}, {Number(latestGps.lng).toFixed(5)}
                        {latestGps.accuracy && <span className="text-white/50">±{latestGps.accuracy}m</span>}
                        <span className="text-white/40 ml-1">
                          {new Date(latestGps.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                        </span>
                      </a>
                    ) : (
                      <span className="inline-flex items-center gap-1 rounded-full border border-white/15 bg-white/5 px-2 py-0.5 text-xs text-white/50">
                        No location yet
                      </span>
                    )}
                  </div>
                )}
              </div>
            )}

            {task.assigned_to ? (
              <button
                type="button"
                onClick={() => setChatOpen(true)}
                className="w-full rounded-2xl border border-secondary/40 bg-surface/60 py-3.5 text-sm font-medium text-secondary hover:bg-surface hover:border-secondary/70 transition-all"
              >
                💬  Open Chat
              </button>
            ) : (
              <div className="w-full rounded-2xl border border-white/10 bg-surface/30 py-3.5 text-sm text-center text-white/30">
                💬 Chat available once a supporter accepts
              </div>
            )}

            {chatOpen && (
              <div className="fixed inset-0 z-50 flex flex-col bg-primary">
                {/* Header bar */}
                <div className="flex items-center px-4 py-4 bg-surface border-b border-white/10 shrink-0">
                  <button
                    type="button"
                    onClick={() => setChatOpen(false)}
                    className="flex items-center gap-1.5 text-secondary text-sm font-medium hover:opacity-80 transition-opacity"
                  >
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
                      <polyline points="15 18 9 12 15 6" />
                    </svg>
                    Back
                  </button>
                  <p className="flex-1 text-center text-sm font-semibold text-accent truncate px-4">{task.title}</p>
                </div>
                {/* Chat fills remaining space */}
                <div className="flex-1 min-h-0">
                  <TaskChatBox task={task} me={user} fullscreen />
                </div>
              </div>
            )}
          </>
        ) : (
          /* 編輯區（原本邏輯保留） */
          /* ... */
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault()
              saveEdit()
            }}
          >
            <div className="flex items-center justify-between">
              <h2 className="text-xl font-semibold">Edit task</h2>
              <button
                type="button"
                onClick={() => setEditing(false)}
                className="text-xs px-2 py-1 border border-white/20 rounded-md hover:border-white/40"
              >
                Cancel
              </button>
            </div>

            <div className="space-y-2">
              {/* Title */}
              <label className="block text-sm text-white/80">
                Title
                <input
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  required
                />
              </label>

              {/* Description */}
              <label className="block text-sm text-white/80">
                Description
                <textarea
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm min-h-20"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </label>

              {/* Category */}
              <label className="block text-sm text-white/80">
                Category
                <select
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={category}
                  onChange={(e) => setCategory(e.target.value)}
                >
                  <option value="task">Task</option>
                  <option value="companion">Companion</option>
                </select>
              </label>

              {/* Locations with PlaceInput */}
              <div className="grid gap-1">
                <label className="block text-sm text-white/80">Location(s)</label>
                <div className="space-y-2">
                  {locations.map((loc, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <div className="flex-1">
                        <PlaceInput
                          value={locations[i]}
                          placeholder={
                            i === 0 ? 'Address or meeting point' : 'Add another point'
                          }
                          onChange={(val) => updateLocation(i, val)}
                        />
                        {loc?.lat && loc?.lng && (
                          <div className="mt-1 text-xs text-white/60">
                            ({loc.lat.toFixed(6)}, {loc.lng.toFixed(6)})
                          </div>
                        )}
                      </div>
                      {i === locations.length - 1 ? (
                        <button
                          type="button"
                          onClick={addLocation}
                          className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40"
                        >
                          ＋
                        </button>
                      ) : (
                        <button
                          type="button"
                          onClick={() => removeLocation(i)}
                          className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40"
                        >
                          ×
                        </button>
                      )}
                    </div>
                  ))}
                </div>
              </div>

              {/* Estimated minutes */}
              <label className="block text-sm text-white/80">
                Estimated minutes
                <input
                  type="number"
                  min={5}
                  step={5}
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={minutes}
                  onChange={(e) => setMinutes(Number(e.target.value) || 0)}
                />
              </label>

              {/* Advance */}
              <label className="block text-sm text-white/80">
                Advance ($)
                <input
                  type="number"
                  min={0}
                  step={1}
                  className="mt-1 w-full rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                  value={prepay}
                  onChange={(e) => setPrepay(e.target.value)}
                />
              </label>

              {/* When */}
              <div className="space-y-2">
                <div className="text-sm text-white/80">When</div>
                <div className="flex gap-3 text-sm">
                  <label className="inline-flex items-center gap-2">
                    <input
                      type="radio"
                      name="mode"
                      value="now"
                      checked={mode === 'now'}
                      onChange={() => setMode('now')}
                    />
                    <span>ASAP</span>
                  </label>
                  <label className="inline-flex items-center gap-2">
                    <input
                      type="radio"
                      name="mode"
                      value="schedule"
                      checked={mode === 'schedule'}
                      onChange={() => setMode('schedule')}
                    />
                    <span>Schedule</span>
                  </label>
                </div>

                {mode === 'schedule' && (
                  <div className="flex gap-3">
                    <input
                      type="date"
                      className="flex-1 rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                      value={date}
                      onChange={(e) => setDate(e.target.value)}
                    />
                    <input
                      type="time"
                      className="flex-1 rounded-md bg-white/5 border border-white/20 px-3 py-2 text-sm"
                      value={timeStr}
                      onChange={(e) => setTimeStr(e.target.value)}
                    />
                  </div>
                )}
              </div>
            </div>

            <button
              type="submit"
              className="w-full mt-4 rounded-md bg-white text-black px-4 py-2 text-sm font-medium hover:bg-white/90 disabled:opacity-50"
            >
              Save changes
            </button>
          </form>
        )}
      </div>
    </div>
  )
}