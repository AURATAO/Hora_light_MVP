import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'

const MINUTE_RATE_EUR = 0.5

export default function NewTask() {

  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [category, setCategory] = useState('task') // task | companion
  const [locations, setLocations] = useState(['']) // 多地點
  const [minutes, setMinutes] = useState(30)
  const [prepay, setPrepay] = useState('') // 輸入 EUR（字串）
  const [loading, setLoading] = useState(false)
  const [touched, setTouched] = useState(false)
  const [mode, setMode] = useState('now') // 'now' | 'schedule'
  const [date, setDate] = useState('')    // YYYY-MM-DD
  const [timeStr, setTimeStr] = useState('') // HH:MM
  const nav = useNavigate()

  function addLocation() {
    setLocations((prev) => [...prev, ''])
  }
  function updateLocation(i, v) {
    setLocations((prev) => prev.map((x, idx) => (idx === i ? v : x)))
  }
  function removeLocation(i) {
    setLocations((prev) => prev.filter((_, idx) => idx !== i))
  }

   const errors = useMemo(() => {
    const e = {}
    if (!title.trim()) e.title = 'Title is required'
    if (!minutes || Number(minutes) <= 0) e.minutes = 'Minutes must be > 0'
    if (prepay !== '' && (Number.isNaN(Number(prepay)) || Number(prepay) < 0)) e.prepay = 'Invalid advance'
    if (mode === 'schedule' && (!date || !timeStr)) e.when = 'Pick date & time'
    return e
  }, [title, minutes, prepay, mode, date, timeStr])


  const canSubmit = Object.keys(errors).length === 0

  const timeCost = useMemo(() => {
    const m = Number(minutes) || 0
    return (m * MINUTE_RATE_EUR)
  }, [minutes])

  const advance = useMemo(() => {
    if (prepay === '') return 0
    const n = Number(prepay)
    return Number.isNaN(n) ? 0 : Math.max(0, n)
  }, [prepay])

  const totalEstimate = useMemo(() => (timeCost + advance), [timeCost, advance])

  const scheduledAtISO = useMemo(() => {
    if (mode !== 'schedule' || !date || !timeStr) return ''
    const dt = new Date(`${date}T${timeStr}`)   // 以本地時區組合
    if (Number.isNaN(dt.getTime())) return ''
    return dt.toISOString()                     // 後端要 RFC3339/ISO
    }, [mode, date, timeStr])

  async function onSubmit(e) {
    e.preventDefault()
    setTouched(true)
    if (!canSubmit) return
    setLoading(true)
    try {
      // 把多地點合併成一個字串（MVP 先這樣傳給後端）
      const location_text = locations
        .map((s) => s.trim())
        .filter(Boolean)
        .join(' | ')


      const payload = {
        title, description, category, location_text,
        estimated_minutes: Number(minutes) || 30,
        prepay_amount_cents: Math.round((advance || 0) * 100),
        is_immediate: mode === 'now',
        scheduled_at: mode === 'schedule' ? scheduledAtISO : '',
      }

      const t = await api('/tasks', { method: 'POST', body: payload })
      nav(`/tasks/${t.id}`)
    } catch (e) {
      alert(e.message || 'Failed to create task')
    } finally {
      setLoading(false)
    }
  }



  return (
    <div className="bg-gradient-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
      <div className="mx-auto max-w-md space-y-4 border border-primary/30 backdrop-blur-md p-8 rounded-lg shadow">
        <h2 className="text-2xl font-semibold">Post a Task</h2>
        <form onSubmit={onSubmit} className="grid gap-5">
          {/* Title */}
          <div className="grid gap-1">
            <label className="text-sm">Title <span className="text-red-500">*</span></label>
            <input
              className={`rounded-md px-3 py-2 bg-transparent outline-none border ${touched && errors.title ? 'border-red-400' : 'border-white/20'} focus:border-white/40`}
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Pick up groceries / Walk together to IKEA"
            />
            {touched && errors.title && <div className="text-sm text-red-400">{errors.title}</div>}
          </div>

          {/* Description */}
          <div className="grid gap-1">
            <label className="text-sm">Description</label>
            <textarea
              rows={4}
              className="rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Any details, notes, constraints."
            />
          </div>

          {/* Category */}
          <div className="grid gap-1">
            <label className="text-sm">Category</label>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => setCategory('task')}
                className={`px-3 py-1.5 rounded-md border ${category === 'task' ? 'bg-white text-black border-white' : 'border-white/20 hover:border-white/40'}`}
              >
                Task
              </button>
              <button
                type="button"
                onClick={() => setCategory('companion')}
                className={`px-3 py-1.5 rounded-md border ${category === 'companion' ? 'bg-white text-black border-white' : 'border-white/20 hover:border-white/40'}`}
              >
                Companion
              </button>
            </div>
          </div>

          {/* Locations with + */}
          <div className="grid gap-1">
            <label className="text-sm">Location(s)</label>
            <div className="space-y-2">
              {locations.map((loc, i) => (
                <div key={i} className="flex items-center gap-2">
                  <input
                    className="flex-1 rounded-md px-3 py-2 bg-transparent outline-none border border-white/20 focus:border-white/40"
                    value={loc}
                    onChange={(e) => updateLocation(i, e.target.value)}
                    placeholder={i === 0 ? 'Address or meeting point' : 'Add another point'}
                  />
                  {i === locations.length - 1 ? (
                    <button type="button" onClick={addLocation} className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40">＋</button>
                  ) : (
                    <button type="button" onClick={() => removeLocation(i)} className="px-2 py-1 rounded-md border border-white/20 hover:border-white/40">×</button>
                  )}
                </div>
              ))}
            </div>
          </div>
          {/* When: 立即 or 排程 */}
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

            {mode === 'schedule' && (
              <div className="flex gap-2">
                <input
                  type="date"
                  className={`rounded-md px-3 py-2 bg-transparent outline-none border ${errors.when ? 'border-red-400' : 'border-white/20'} focus:border-white/40 flex-1`}
                  value={date}
                  onChange={(e)=>setDate(e.target.value)}
                />
                <input
                  type="time"
                  className={`rounded-md px-3 py-2 bg-transparent outline-none border ${errors.when ? 'border-red-400' : 'border-white/20'} focus:border-white/40 w-40`}
                  value={timeStr}
                  onChange={(e)=>setTimeStr(e.target.value)}
                />
              </div>
            )}
            {errors.when && <div className="text-sm text-red-400">{errors.when}</div>}
          </div>

          {/* Minutes + Advance */}

          <div className="grid gap-1">
              <label className="text-sm">Estimated minutes <span className="text-red-500">*</span></label>
              <input
                type="number"
                min={5}
                step={5}
                className={`rounded-md px-3 py-2 bg-transparent outline-none border ${touched && errors.minutes ? 'border-red-400' : 'border-white/20'} focus:border-white/40`}
                value={minutes}
                onChange={(e) => setMinutes(Number(e.target.value))}
              />
              {touched && errors.minutes && <div className="text-sm text-red-400">{errors.minutes}</div>}

              {/* 預估金額 */}
              <div className="text-xs text-white/80 ">
                Time cost (~{MINUTE_RATE_EUR.toFixed(2)} EUR/min): <b>{timeCost.toFixed(2)} EUR</b>
              </div>

            <div className="grid gap-1">
              <label className="text-sm">Advance for purchase (EUR)</label>
              <input
                type="number"
                min={0}
                step={0.01}
                className={`rounded-md px-3 py-2 bg-transparent outline-none border ${touched && errors.prepay ? 'border-red-400' : 'border-white/20'} focus:border-white/40`}
                value={prepay}
                onChange={(e) => setPrepay(e.target.value)}
                placeholder="e.g., 12.50"
              />
              {touched && errors.prepay && <div className="text-sm text-red-400">{errors.prepay}</div>}
            </div>
          </div>

             {/* 預估區（加上開始時間） */}
          <div className="text-xs text-white/80 rounded-md px-3 py-2 bg-transparent outline-none border flex flex-col items-start">
            Start: <b>{mode === 'now' ? 'ASAP' : (date && timeStr ? new Date(`${date}T${timeStr}`).toLocaleString() : '—')}</b>
            <span className="mx-2">•</span>
            Time cost (~{MINUTE_RATE_EUR.toFixed(2)} EUR/min): <b>{timeCost.toFixed(2)} EUR</b>
            {advance > 0 && <> <span className="mx-2">•</span> Advance: <b>{advance.toFixed(2)} EUR</b> </>}
            <span className="mx-2">•</span>
            Total estimate: <b>{totalEstimate.toFixed(2)} EUR</b>
          </div>

          {/* Actions */}
          <div className="flex gap-3 pt-2">
            <button
              type="submit"
              disabled={!canSubmit || loading}
              className="rounded-md px-4 py-2 bg-white text-black disabled:opacity-50"
            >
              {loading ? 'Posting…' : 'Create task'}
            </button>
            <button
              type="button"
              onClick={() => {
                setTitle(''); setDescription(''); setCategory('task');
                setLocations(['']); setMinutes(30); setPrepay(''); setTouched(false)
              }}
              className="rounded-md px-4 py-2 border border-white/20 hover:border-white/40"
            >
              Clear
            </button>
          </div>

        </form>
      </div>
    </div>
  )
}