import { useEffect, useState } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { api } from '../api/client'

const STARS = [1, 2, 3, 4, 5]

const VALUE_OPTIONS = [
  { value: 'not_worth', label: 'Not worth it' },
  { value: 'fair',      label: 'Fair price' },
  { value: 'great',     label: 'Great value' },
]

export default function ReviewPage() {
  const { id: taskID } = useParams()
  const nav = useNavigate()

  const [task, setTask]               = useState(null)
  const [loading, setLoading]         = useState(true)
  const [alreadyReviewed, setAlready] = useState(false)
  const [submitting, setSubmitting]   = useState(false)
  const [error, setError]             = useState('')

  // form state
  const [stars, setStars]             = useState(0)
  const [hovered, setHovered]         = useState(0)
  const [valueRating, setValueRating] = useState('')
  const [wouldRehire, setWouldRehire] = useState(null) // true | false | null
  const [comment, setComment]         = useState('')

  useEffect(() => {
    api(`/tasks/${taskID}`)
      .then(t => setTask(t))
      .catch(() => setError('Could not load task.'))
      .finally(() => setLoading(false))
  }, [taskID])

  async function submit(e) {
    e.preventDefault()
    if (stars === 0) { setError('Please select a star rating.'); return }
    setError('')
    setSubmitting(true)
    try {
      await api(`/tasks/${taskID}/review`, {
        method: 'POST',
        body: { stars, value_rating: valueRating, would_rehire: wouldRehire, comment },
      })
      nav('/tasks/new', { replace: true })
    } catch (e) {
      if (e?.status === 409 || e?.message?.includes('already reviewed')) {
        setAlready(true)
      } else {
        setError(e?.message || 'Something went wrong.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  if (loading) {
    return (
      <div className="min-h-screen bg-[#1a1f2e] flex items-center justify-center">
        <div className="w-6 h-6 border-2 border-white/20 border-t-white/70 rounded-full animate-spin" />
      </div>
    )
  }

  const supporterName = task?.assigned_to
    ? task.assigned_to.split('@')[0].replace(/\./g, ' ').replace(/\b\w/g, c => c.toUpperCase())
    : 'Your supporter'

  if (alreadyReviewed) {
    return (
      <div className="min-h-screen bg-[#1a1f2e] flex items-center justify-center px-4">
        <div className="w-full max-w-md bg-[#2d3748] rounded-2xl p-8 text-center space-y-4">
          <div className="text-4xl">✓</div>
          <h2 className="text-white text-xl font-semibold">Already reviewed</h2>
          <p className="text-white/50 text-sm">You've already submitted a review for this task.</p>
          <Link to={`/tasks/${taskID}`} className="inline-block mt-2 text-[#9aab3a] text-sm hover:underline">
            Back to task
          </Link>
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-[#1a1f2e] py-10 px-4">
      <div className="mx-auto max-w-md">

        {/* Header */}
        <div className="mb-6">
          <Link to={`/tasks/${taskID}`} className="text-white/40 text-sm hover:text-white/70 transition-colors">
            ← Back
          </Link>
        </div>

        <div className="bg-[#2d3748] rounded-2xl p-6 space-y-6">

          {/* Title */}
          <div>
            <h1 className="text-white text-2xl font-semibold leading-snug">
              How did it go?
            </h1>
            <p className="text-white/50 text-sm mt-1">
              {supporterName} · {task?.title || 'Task'}
            </p>
          </div>

          <form onSubmit={submit} className="space-y-6">

            {/* Q1 — Star rating */}
            <div className="space-y-2">
              <p className="text-white/70 text-sm font-medium">Overall experience</p>
              <div className="flex gap-2">
                {STARS.map(s => (
                  <button
                    key={s}
                    type="button"
                    onClick={() => setStars(s)}
                    onMouseEnter={() => setHovered(s)}
                    onMouseLeave={() => setHovered(0)}
                    className="text-3xl transition-transform active:scale-110 focus:outline-none"
                    aria-label={`${s} star${s > 1 ? 's' : ''}`}
                  >
                    <span className={
                      s <= (hovered || stars)
                        ? 'text-yellow-400'
                        : 'text-white/20'
                    }>★</span>
                  </button>
                ))}
              </div>
            </div>

            {/* Q2 — Value for money */}
            <div className="space-y-2">
              <p className="text-white/70 text-sm font-medium">Value for money</p>
              <div className="flex flex-wrap gap-2">
                {VALUE_OPTIONS.map(opt => (
                  <button
                    key={opt.value}
                    type="button"
                    onClick={() => setValueRating(v => v === opt.value ? '' : opt.value)}
                    className={`px-4 py-2 rounded-full border text-sm transition-all ${
                      valueRating === opt.value
                        ? 'border-[#9aab3a] bg-[#9aab3a]/15 text-[#9aab3a]'
                        : 'border-white/15 text-white/60 hover:border-white/30 hover:text-white/80'
                    }`}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
            </div>

            {/* Q3 — Would rehire */}
            <div className="space-y-2">
              <p className="text-white/70 text-sm font-medium">Would you hire again?</p>
              <div className="flex gap-2">
                {[
                  { val: true,  label: 'Yes, definitely' },
                  { val: false, label: 'Not really' },
                ].map(opt => (
                  <button
                    key={String(opt.val)}
                    type="button"
                    onClick={() => setWouldRehire(v => v === opt.val ? null : opt.val)}
                    className={`flex-1 py-2.5 rounded-xl border text-sm transition-all ${
                      wouldRehire === opt.val
                        ? 'border-[#9aab3a] bg-[#9aab3a]/15 text-[#9aab3a]'
                        : 'border-white/15 text-white/60 hover:border-white/30 hover:text-white/80'
                    }`}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
            </div>

            {/* Q4 — Comment */}
            <div className="space-y-2">
              <p className="text-white/70 text-sm font-medium">
                Comments <span className="text-white/30 font-normal">(optional)</span>
              </p>
              <textarea
                value={comment}
                onChange={e => setComment(e.target.value)}
                rows={3}
                placeholder="Great job, very punctual…"
                className="w-full bg-white/5 border border-white/10 rounded-xl px-4 py-3
                           text-white text-sm placeholder-white/25 resize-none
                           focus:outline-none focus:border-[#9aab3a]/50 transition-colors"
              />
            </div>

            {error && (
              <p className="text-red-400 text-sm">{error}</p>
            )}

            {/* Submit */}
            <div className="space-y-3 pt-1">
              <button
                type="submit"
                disabled={submitting}
                className="w-full py-3 rounded-xl bg-[#9aab3a] text-white font-semibold
                           text-sm hover:brightness-110 transition-all disabled:opacity-50"
              >
                {submitting ? 'Submitting…' : 'Submit review'}
              </button>
              <div className="text-center">
                <Link
                  to={`/tasks/${taskID}`}
                  className="text-white/35 text-sm hover:text-white/60 transition-colors"
                >
                  Skip for now
                </Link>
              </div>
            </div>

          </form>
        </div>
      </div>
    </div>
  )
}
