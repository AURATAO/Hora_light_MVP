import { useEffect, useMemo, useState } from 'react'
import { useParams } from 'react-router-dom'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'

const MINUTE_RATE_EUR = 0.5

export default function TaskDetail() {
  const { id } = useParams()
  const { user } = useAuth()
  const [task, setTask] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)

  // 編輯狀態的表單欄位
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [category, setCategory] = useState('task')
  const [locations, setLocations] = useState([''])
  const [minutes, setMinutes] = useState(30)
  const [prepay, setPrepay] = useState('') // EUR 字串
  const [mode, setMode] = useState('now') // 'now' | 'schedule'
  const [date, setDate] = useState('')
  const [timeStr, setTimeStr] = useState('')

  useEffect(() => {
    let alive = true
    setLoading(true); setError('')
    api(`/tasks/${id}`)
      .then((t) => { if (alive) { setTask(t) } })
      .catch((e) => setError(e.message || 'Failed to load'))
      .finally(() => setLoading(false))
    return () => { alive = false }
  }, [id])

  const isOwner = user?.email && task?.requester && user.email === task.requester

  // 進入編輯模式時把 task 值灌入表單
  function startEdit() {
    if (!task) return
    setTitle(task.title || '')
    setDescription(task.description || '')
    setCategory(task.category || 'task')
    setLocations((task.location_text || '').split(' | ').filter(Boolean).length
      ? (task.location_text || '').split(' | ').filter(Boolean)
      : [''])
    setMinutes(task.estimated_minutes || 30)
    setPrepay(((task.prepay_amount_cents || 0) / 100).toString())

    if (task.is_immediate) {
      setMode('now'); setDate(''); setTimeStr('')
    } else if (task.scheduled_at) {
      const d = new Date(task.scheduled_at)
      const pad = (n) => String(n).padStart(2, '0')
      const ds = `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}`
      const ts = `${pad(d.getHours())}:${pad(d.getMinutes())}`
      setMode('schedule'); setDate(ds); setTimeStr(ts)
    } else {
      setMode('now'); setDate(''); setTimeStr('')
    }
    setEditing(true)
  }

  const scheduledAtISO = useMemo(() => {
    if (mode === 'schedule' && date && timeStr) {
      return new Date(`${date}T${timeStr}`).toISOString()
    }
    return ''
  }, [mode, date, timeStr])

  const timeCost = useMemo(() => (Number(minutes || 0) * MINUTE_RATE_EUR), [minutes])
  const advance = useMemo(() => {
    if (prepay === '') return 0
    const n = Number(prepay); return Number.isNaN(n) ? 0 : Math.max(0, n)
  }, [prepay])
  const totalEstimate = useMemo(() => (timeCost + advance), [timeCost, advance])

  async function saveEdit() {
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
  }

  function addLocation(){ setLocations(prev => [...prev, '']) }
  function updateLocation(i, v){ setLocations(prev => prev.map((x, idx)=> idx===i?v:x)) }
  function removeLocation(i){ setLocations(prev => prev.filter((_, idx)=> idx!==i)) }

  if (loading) return <div className="p-6">Loading…</div>
  if (error) return <div className="p-6 text-red-500">{error}</div>
  if (!task) return <div className="p-6">Task not found.</div>

  const advanceEUR = (task.prepay_amount_cents || 0) / 100
  const whenText = task.is_immediate ? 'ASAP' : (task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : '—')

  return (
    <div className="bg-gradient-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
      <div className="mx-auto max-w-md space-y-4 border border-primary/30 backdrop-blur-md p-8 rounded-lg shadow">
        {!editing ? (
          <>
            <div className="flex items-start gap-3">
              <h2 className="text-2xl font-semibold flex-1">{task.title}</h2>
              <span className="inline-flex h-6 items-center rounded-full border border-white/15 bg-white/5 px-2 text-[11px] uppercase tracking-wide text-white/80 select-none pointer-events-none">
                {task.status}
              </span>
              {isOwner && task.status === 'open' && (
                <button onClick={startEdit} className="ml-2 rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40">
                  Edit
                </button>
              )}
            </div>

            <div className="text-sm text-white/80 space-y-1">
              <div><b>When:</b> {whenText}</div>
              <div><b>Estimated:</b> {task.estimated_minutes} min</div>
              <div><b>Advance:</b> {advanceEUR.toFixed(2)} EUR</div>
              <div><b>Locations:</b> {task.location_text || '—'}</div>
              <div className="text-xs opacity-80">ID: {task.id}</div>
            </div>

            <div className="border border-white/20 rounded-md p-3 whitespace-pre-wrap">
              {task.description || 'No description.'}
            </div>

            <div className="border border-white/20 rounded-md p-3">
              <div className="text-sm mb-2">Chat</div>
              <div id="talkjs-container" className="h-56 rounded bg-white/5 border border-white/10 flex items-center justify-center text-xs text-white/60">
                (Chat will appear here after assignment)
              </div>
            </div>
          </>
        ) : (
          // 編輯模式
          <div className="space-y-4">
            <h2 className="text-xl font-semibold">Edit Task</h2>

            <div className="grid gap-1">
              <label className="text-sm">Title</label>
              <input className="rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                value={title} onChange={(e)=>setTitle(e.target.value)} />
            </div>

            <div className="grid gap-1">
              <label className="text-sm">Description</label>
              <textarea rows={4} className="rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                value={description} onChange={(e)=>setDescription(e.target.value)} />
            </div>

            <div className="grid gap-1">
              <label className="text-sm">Category</label>
              <div className="flex gap-2">
                <button type="button" onClick={()=>setCategory('task')}
                  className={`px-3 py-1.5 rounded-md border ${category==='task'?'bg-white text-black border-white':'border-white/20 hover:border-white/40'}`}>
                  Task
                </button>
                <button type="button" onClick={()=>setCategory('companion')}
                  className={`px-3 py-1.5 rounded-md border ${category==='companion'?'bg-white text-black border-white':'border-white/20 hover:border-white/40'}`}>
                  Companion
                </button>
              </div>
            </div>

            <div className="grid gap-1">
              <label className="text-sm">Location(s)</label>
              <div className="space-y-2">
                {locations.map((loc,i)=>(
                  <div key={i} className="flex items-center gap-2">
                    <input className="flex-1 rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                      value={loc} onChange={(e)=>updateLocation(i, e.target.value)} />
                    {i===locations.length-1
                      ? <button type="button" onClick={addLocation} className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40">＋</button>
                      : <button type="button" onClick={()=>removeLocation(i)} className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40">×</button>}
                  </div>
                ))}
              </div>
            </div>

         
              <div className="grid gap-1">
                <label className="text-sm">Estimated minutes</label>
                <input type="number" min={5} step={5}
                  className="rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                  value={minutes} onChange={(e)=>setMinutes(Number(e.target.value))} />
                <div className="text-xs text-white/80 mt-1">
                  Time cost (~{MINUTE_RATE_EUR.toFixed(2)} EUR/min): <b>{(Number(minutes||0)*MINUTE_RATE_EUR).toFixed(2)} EUR</b>
                </div>
              </div>

              <div className="grid gap-1">
                <label className="text-sm">Advance (EUR)</label>
                <input type="number" min={0} step={0.01}
                  className="rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                  value={prepay} onChange={(e)=>setPrepay(e.target.value)} />
                <div className="text-xs text-white/80 mt-1">
                  Total estimate: <b>{(Number(minutes||0)*MINUTE_RATE_EUR + (Number(prepay)||0)).toFixed(2)} EUR</b>
                </div>
              </div>
            

            <div className="grid gap-2">
              <label className="text-sm">When</label>
              <div className="flex items-center gap-3">
                <label className="inline-flex items-center gap-2">
                  <input type="radio" name="when" checked={mode==='now'} onChange={()=>setMode('now')} />
                  <span>ASAP</span>
                </label>
                <label className="inline-flex items-center gap-2">
                  <input type="radio" name="when" checked={mode==='schedule'} onChange={()=>setMode('schedule')} />
                  <span>Schedule</span>
                </label>
              </div>
              {mode==='schedule' && (
                <div className="flex gap-2">
                  <input type="date" className="flex-1 rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                    value={date} onChange={(e)=>setDate(e.target.value)} />
                  <input type="time" className="w-40 rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                    value={timeStr} onChange={(e)=>setTimeStr(e.target.value)} />
                </div>
              )}
            </div>

            <div className="flex gap-3">
              <button onClick={saveEdit} className="rounded-md px-4 py-2 bg-white text-black">Save</button>
              <button onClick={()=>setEditing(false)} className="rounded-md px-4 py-2 border border-white/20 hover:border-white/40">Cancel</button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}