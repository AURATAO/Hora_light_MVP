import { useMemo, useState, useEffect, useRef } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import Modal from '../components/Modal'
import InfoModal from '../components/InfoModal'
import DurationPicker from '../components/DurationPicker'
import AddressInput from '../components/AddressInput'
import { useToast } from '../providers/ToastProvider'

const OVERTIME_RATE = 0.50

const CATEGORY_LABELS = {
  delivery:     '🚀 Same-day Delivery',
  grocery:      '🛒 Grocery & Errands',
  laundry:      '👕 Laundry Service',
  companionship:'🤝 Companionship',
  queue:        '⏳ Queue & Wait',
  anything_else:'✨ Anything Else',
}

export default function NewTask() {
  const nav = useNavigate()
  const { user, loading: authLoading } = useAuth()
  const toast = useToast()

  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [category, setCategory] = useState('quick_errand')
  const [locs, setLocs] = useState([{ id: 0, result: null }])
  const nextLocId = useRef(1)
  const [minutes, setMinutes] = useState('30')
  const [prepay, setPrepay] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [touched, setTouched] = useState(false)
  const [mode, setMode] = useState('now') // 'now' | 'schedule'
  const [date, setDate] = useState('')
  const [timeStr, setTimeStr] = useState('')
  const [successOpen, setSuccessOpen] = useState(false)

  const [transport, setTransport] = useState('none')
  const [taskType, setTaskType] = useState('task') // 'task' | 'companion'
  const [urlCategory, setUrlCategory] = useState(null) // locked when navigated from CategoryHome
  const [searchParams] = useSearchParams()

  // Read category from URL once on mount and pre-configure form
  useEffect(() => {
    const cat = searchParams.get('category')
    if (!cat) return
    setUrlCategory(cat)
    if (cat === 'companionship') {
      // open policy modal — user must agree before companion is confirmed
      openCompanionPolicy()
    } else {
      setTaskType('task')
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  function updateLoc(i, result) {
    setLocs(prev => prev.map((l, idx) => idx === i ? { ...l, result } : l))
  }
  function addLoc() {
    setLocs(prev => [...prev, { id: nextLocId.current++, result: null }])
  }
  function removeLoc(i) {
    setLocs(prev => prev.filter((_, idx) => idx !== i))
  }

  const [compPolicyOpen, setCompPolicyOpen] = useState(false)
  const [compPolicyAgreed, setCompPolicyAgreed] = useState(false)
  const [compPolicyChecked, setCompPolicyChecked] = useState(false)

  const goToPosted = () => {
  setSuccessOpen(false);
  nav('/my', { replace: true });
};

  // ✅ 不要在 hook 後面用條件 return，改成條件渲染
  const notReady = authLoading || !user


  const errors = useMemo(() => {
    const e = {}
    if (!title.trim()) e.title = 'Title is required'
    if (!minutes || Number(minutes) < 10) e.minutes = 'Minimum 10 minutes'
    if (prepay !== '' && (Number.isNaN(Number(prepay)) || Number(prepay) < 0)) e.prepay = 'Invalid advance'
    if (mode === 'schedule' && (!date || !timeStr)) e.when = 'Pick date & time'
    // companion policy is enforced via its own modal
    return e
  }, [title, minutes, prepay, mode, date, timeStr,category, compPolicyAgreed])

  const canSubmit = Object.keys(errors).length === 0

  const baseFee = useMemo(() => {
    if (taskType === 'companion' || urlCategory === 'companionship') return 25
    if (Number(minutes) > 90) return 18
    return 12
  }, [taskType, urlCategory, minutes])
  const overtimeCost = useMemo(() => Math.max(0, (Number(minutes || 0) - 15)) * OVERTIME_RATE, [minutes])
  const timeCost = useMemo(() => baseFee + overtimeCost, [baseFee, overtimeCost])
  const advance = useMemo(() => {
    if (prepay === '') return 0
    const n = Number(prepay)
    return Number.isNaN(n) ? 0 : Math.max(0, n)
  }, [prepay])
  const totalEstimate = useMemo(() => (timeCost + advance), [timeCost, advance])

  const scheduledAtISO = useMemo(() => {
    if (mode !== 'schedule' || !date || !timeStr) return ''
    const dt = new Date(`${date}T${timeStr}`)
    return Number.isNaN(dt.getTime()) ? '' : dt.toISOString()
  }, [mode, date, timeStr])

  async function onSubmit(e) {
  e.preventDefault()
  setTouched(true)
  if (!canSubmit) return
  if (mode === 'schedule' && date && timeStr) {
    const selected = new Date(`${date}T${timeStr}`)
    if (selected < new Date()) {
      toast('Please select a future date and time', 'error')
      return
    }
  }
  setIsSubmitting(true)
  try {
    if (!user) throw new Error('Please sign in')

    const validLocs = locs.filter(l => l.result?.label)
    const location_text = validLocs.map(l => l.result.label.trim()).filter(Boolean).join(' | ')
    const locations_geo = validLocs.map(l => ({
      label: l.result.label,
      placeId: l.result.placeId || null,
      lat: l.result.lat || null,
      lng: l.result.lng || null,
    }))

    const payload = {
      title,
      description,
      category: taskType === 'companion' ? 'companion' : (urlCategory || category),
      location_text,
      locations_geo,
      estimated_minutes: Number(minutes) || 30,
      prepay_amount_cents: Math.round((advance || 0) * 100),
      is_immediate: mode === 'now',
      scheduled_at: mode === 'schedule' ? scheduledAtISO : '',
      transport_required: transport,
    }

    await api('/tasks', { method: 'POST', body: payload, noRedirect: true })
    setSuccessOpen(true)
    return
  } catch (err) {
    toast(err.message || 'Failed to create task')
  } finally {
    setIsSubmitting(false)
  }
}

  useEffect(() => {
    // 可選：觀察 session（除錯用）
    // console.log('[NewTask] user?', user?.id)
  }, [user])

  if (notReady) {
    return <div className="p-6">Loading…</div>
  }

  //Policy for companion category 
const COMP_POLICY_TEXT = `Companionship (Accompaniment) Policy

What it IS:
• Public-route accompaniment only (e.g., walking together in public areas, escorting someone to a nearby appointment, accompanying someone to a subway/bus stop).
• Non-medical, non-caregiving, and strictly for general presence/support in public.

What it is NOT:
• No medical or personal care (no medication handling, bathing, lifting, or physical assistance).
• No services involving minors.
• No intimate/sexual services, no dating positioning.
• No overnight stays.
• No staying inside a private residence.

Safety & boundaries:
• Keep the route in public places; either party can end the task anytime if uncomfortable.
• If a request involves restricted activities, it must be declined and reported to the platform.
`

function openCompanionPolicy() {
  setCompPolicyChecked(false)
  setCompPolicyOpen(true)
}

function closeCompanionPolicy() {
  setCompPolicyOpen(false)
}

function confirmCompanionPolicy() {
  setCompPolicyAgreed(true)
  setCompPolicyOpen(false)
  setTaskType('companion')
}

  return (
    <div className="bg-linear-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
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

          {/* Service type: Task vs Companion */}
          <div className="grid gap-2">
            <label className="text-sm text-white/70">Service type <span className="text-red-500">*</span></label>
            {urlCategory ? (
              /* Read-only pill when navigated from CategoryHome */
              <div className="flex items-center gap-2">
                <span className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-full border border-[#9aab3a] bg-[#9aab3a]/10 text-sm font-semibold text-[#9aab3a]">
                  {taskType === 'companion' ? '🤝 Companion' : CATEGORY_LABELS[urlCategory] || urlCategory}
                </span>
              </div>
            ) : (
              /* Interactive selector */
              <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => setTaskType('task')}
                  className={`rounded-xl p-3 text-left border transition-all ${
                    taskType === 'task'
                      ? 'border-[#9aab3a] bg-[#9aab3a]/10'
                      : 'border-white/10 bg-white/5 hover:border-white/20'
                  }`}
                >
                  <div className={`text-sm font-semibold ${taskType === 'task' ? 'text-[#9aab3a]' : 'text-white'}`}>Task</div>
                  <div className="text-xs text-white/40 mt-0.5 leading-snug">Errands, delivery, cleaning…</div>
                </button>
                <button
                  type="button"
                  onClick={() => {
                    if (taskType !== 'companion') openCompanionPolicy()
                  }}
                  className={`rounded-xl p-3 text-left border transition-all ${
                    taskType === 'companion'
                      ? 'border-[#9aab3a] bg-[#9aab3a]/10'
                      : 'border-white/10 bg-white/5 hover:border-white/20'
                  }`}
                >
                  <div className={`text-sm font-semibold ${taskType === 'companion' ? 'text-[#9aab3a]' : 'text-white'}`}>Companion</div>
                  <div className="text-xs text-white/40 mt-0.5 leading-snug">Accompany me to appointments…</div>
                </button>
              </div>
            )}
          </div>

          {/* Duration + category (DurationPicker) */}
          <DurationPicker
            value={Number(minutes)}
            onChange={(mins, cat) => { setMinutes(String(mins)); setCategory(cat) }}
          />

          {/* Location */}
          <div className="grid gap-3">
            {locs.map((loc, i) => (
              <div key={loc.id} className="grid gap-1">
                <div className="flex items-center justify-between">
                  <label className="text-sm">
                    {i === 0 ? '📍 Pick-up / Starting point' : `📍 Drop-off / Stop ${i + 1}`}
                  </label>
                  {i > 0 && (
                    <button
                      type="button"
                      onClick={() => removeLoc(i)}
                      className="text-xs text-white/40 hover:text-white/60"
                    >
                      Remove
                    </button>
                  )}
                </div>
                <AddressInput key={loc.id} onChange={(result) => updateLoc(i, result)} />
              </div>
            ))}
            {locs.length < 3 && (
              <button
                type="button"
                onClick={addLoc}
                className="flex items-center gap-1.5 text-sm text-white/60 hover:text-white/80 border border-dashed border-white/20 hover:border-white/40 rounded-md px-3 py-2 transition-colors w-fit"
              >
                <span className="text-base leading-none">+</span> Add drop-off / stop
              </button>
            )}
          </div>

          {/* When */}
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
                  min={new Date().toISOString().split('T')[0]}
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

          <InfoModal
            label="Shopping budget ($)"
            title="Shopping budget ($)"
            body={
              <div className="space-y-3">
                <p>If your task requires your supporter to purchase something on your behalf (e.g. groceries, supplies), enter the maximum amount they may need to spend.</p>
                <p>This is separate from the time cost.</p>

                <div>
                  <p className="font-semibold text-white mb-1">How it works</p>
                  <ul className="space-y-1">
                    <li>Your supporter will only buy after getting your confirmation first</li>
                    <li>They upload the receipt after purchase — we verify it before any amount is deducted</li>
                    <li>You are only charged for what was actually spent:</li>
                  </ul>
                  <ul className="mt-1 ml-3 space-y-0.5">
                    <li>spent less → refund of difference</li>
                    <li>spent more → difference collected</li>
                  </ul>
                </div>

                <div>
                  <p className="font-semibold text-white mb-1">Beta limits</p>
                  <ul className="space-y-1">
                    <li>Maximum shopping budget during beta: $30.00</li>
                    <li>This portion is settled separately after the task is completed</li>
                  </ul>
                </div>

                <p className="border border-white/20 rounded px-3 py-2 text-white/60 text-xs">
                  🎉 Shopping budget settlement is coming soon. During beta, please arrange payment directly with your supporter.
                </p>
              </div>
            }
          >
            <input
              type="number" min={0} step={0.01}
              className={`rounded-md px-3 py-2 bg-transparent outline-none border ${touched && errors.prepay ? 'border-red-400' : 'border-white/20'} focus:border-white/40`}
              value={prepay}
              onChange={(e)=>setPrepay(e.target.value)}
              placeholder="e.g., 12.50"
            />
            {touched && errors.prepay && <div className="text-sm text-red-400">{errors.prepay}</div>}
          </InfoModal>

          {/* Transport */}
          <div className="grid gap-2">
            <label className="text-sm">Helper needs a…</label>
            <div className="flex flex-wrap gap-2">
              {[
                { value: 'none', label: 'None' },
                { value: 'car', label: 'Car' },
                { value: 'bike', label: 'Bike' },
                { value: 'public', label: 'Public transport is fine' },
              ].map(opt => (
                <button
                  key={opt.value}
                  type="button"
                  onClick={() => setTransport(opt.value)}
                  className={`px-3 py-1.5 rounded-full border text-sm transition-colors ${
                    transport === opt.value
                      ? 'bg-white text-black border-white'
                      : 'border-white/20 hover:border-white/40'
                  }`}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>

          {/* Estimate summary */}
          <div className="text-xs text-white/80 rounded-md px-3 py-2 border border-white/20 grid gap-1">
            <div className="flex justify-between">
              <span>Start</span>
              <b>{mode === 'now' ? 'ASAP' : (date && timeStr ? new Date(`${date}T${timeStr}`).toLocaleString() : '—')}</b>
            </div>
            <div className="flex justify-between">
              <span>Base fee + overtime</span>
              <b>${baseFee.toFixed(2)}</b>
            </div>
            {overtimeCost > 0 && (
              <div className="flex justify-between">
                <span>Extra time</span>
                <b>${overtimeCost.toFixed(2)}</b>
              </div>
            )}
            {advance > 0 && (
              <div className="flex justify-between">
                <span>Shopping budget</span>
                <b>${advance.toFixed(2)}</b>
              </div>
            )}
            <div className="flex justify-between border-t border-white/20 pt-1 mt-0.5">
              <span>Total estimate</span>
              <b>${totalEstimate.toFixed(2)}</b>
            </div>
          </div>

          {/* Actions */}
          <div className="flex gap-3 pt-2">
            <button type="submit" disabled={!canSubmit || isSubmitting}
              className="rounded-md px-4 py-2 bg-white text-black disabled:opacity-50">
              {isSubmitting ? 'Posting…' : 'Create task'}
            </button>
            <button type="button"
              onClick={()=>{
                setTitle(''); setDescription(''); setCategory('quick_errand');
                setLocs([{ id: nextLocId.current++, result: null }]); setMinutes('30'); setPrepay('');
                setTransport('none'); setTouched(false); setTaskType('task');
                setCompPolicyAgreed(false); setCompPolicyChecked(false); setCompPolicyOpen(false);
              }}
              className="rounded-md px-4 py-2 border border-white/20 hover:border-white/40">
              Clear
            </button>
          </div>
        </form>
      </div>
      <Modal
        open={successOpen}
        onClose={goToPosted}        // 點背景/ESC/右上角都導去 posted
        title="Task Posted !"
        // 1.5 秒後跳轉
        autoCloseMs={5000}
        actions={
          <>
            <button
              className="rounded-md px-4 py-2 border border-white/20 hover:border-white/40"
              onClick={goToPosted}
            >
              My Task
            </button>
          </>
        }
      />
      <Modal
        open={compPolicyOpen}
        onClose={() => {
          closeCompanionPolicy()
          if (!compPolicyAgreed) { setTaskType('task'); setUrlCategory(null) }
        }}
        title="Companionship Policy"
        actions={
          <>
            <button
              className="rounded-md px-4 py-2 border border-white/20 hover:border-white/40"
              onClick={() => {
                closeCompanionPolicy()
                if (!compPolicyAgreed) { setTaskType('task'); setUrlCategory(null) }
              }}
            >
              Cancel
            </button>

            <button
              className="rounded-md px-4 py-2 bg-white text-black disabled:opacity-50"
              disabled={!compPolicyChecked}
              onClick={confirmCompanionPolicy}
            >
              Confirm & Continue
            </button>
          </>
        }
      >
        <div className="space-y-3">
          <div className="whitespace-pre-wrap rounded-md border border-white/20 p-3 max-h-[45vh] overflow-auto">
            {COMP_POLICY_TEXT}
          </div>

          <label className="flex items-start gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={compPolicyChecked}
              onChange={(e) => setCompPolicyChecked(e.target.checked)}
              className="mt-1"
            />
            <span>I have read and agree to the companionship policy.</span>
          </label>
        </div>
      </Modal>
    </div>
  )
}


