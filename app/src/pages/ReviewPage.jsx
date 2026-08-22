import { useEffect, useState } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { isTractionWindowActive, TRACTION_3_CONFIG } from '../lib/traction'
import TractionReviewForm from '../components/TractionReviewForm'

const STARS = [1, 2, 3, 4, 5]

const VALUE_OPTIONS = [
  { value: 'not_worth', label: 'Not worth it' },
  { value: 'fair',      label: 'Fair price' },
  { value: 'great',     label: 'Great value' },
]

// One review per person per task (server: reviews_task_rater_unique). A repeat
// submit is not a failure — it means the answers are already in.
function isAlreadyReviewed(e) {
  return e?.status === 409 || e?.body?.error === 'already reviewed' || !!e?.message?.includes('already reviewed')
}

// Shell shared by every state of this page, so the card, spacing and back link
// don't drift between the classic form and the questionnaire.
function ReviewShell({ taskID, children }) {
  return (
    <div className="min-h-screen bg-[#1a1f2e] py-10 px-4">
      <div className="mx-auto max-w-md">
        <div className="mb-6">
          <Link to={`/tasks/${taskID}`} className="text-white/40 text-sm hover:text-white/70 transition-colors">
            ← Back
          </Link>
        </div>
        <div className="bg-[#2d3748] rounded-2xl p-6 space-y-6">
          {children}
        </div>
      </div>
    </div>
  )
}

export default function ReviewPage() {
  const { id: taskID } = useParams()
  const nav = useNavigate()
  const { user } = useAuth()

  const [task, setTask]               = useState(null)
  const [loading, setLoading]         = useState(true)
  const [alreadyReviewed, setAlready] = useState(false)
  const [submitted, setSubmitted]     = useState(false)
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
      if (isAlreadyReviewed(e)) {
        setAlready(true)
      } else {
        setError(e?.message || 'Something went wrong.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  // Traction 3 questionnaire. Anything that isn't a duplicate is re-thrown so
  // the form can show it inline and keep the answers for a retry.
  async function submitQuestionnaire(body) {
    try {
      await api(`/tasks/${taskID}/review`, { method: 'POST', body })
      setSubmitted(true)
    } catch (e) {
      if (isAlreadyReviewed(e)) { setAlready(true); return }
      throw e
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
          <h2 className="text-white text-xl font-semibold">Already submitted</h2>
          <p className="text-white/50 text-sm">You've already answered for this task — thanks.</p>
          <Link to={`/tasks/${taskID}`} className="inline-block mt-2 text-[#9aab3a] text-sm hover:underline">
            Back to task
          </Link>
        </div>
      </div>
    )
  }

  if (submitted) {
    return (
      <div className="min-h-screen bg-[#1a1f2e] flex items-center justify-center px-4">
        <div className="w-full max-w-md bg-[#2d3748] rounded-2xl p-8 text-center space-y-4">
          <div className="text-4xl">✓</div>
          <h2 className="text-white text-xl font-semibold">Thanks for the feedback</h2>
          <p className="text-white/50 text-sm">This round closes {TRACTION_3_CONFIG.window}.</p>
          <Link to={`/tasks/${taskID}`} className="inline-block mt-2 text-[#9aab3a] text-sm hover:underline">
            Back to task
          </Link>
        </div>
      </div>
    )
  }

  // Which side of the task is answering. Same UUID comparison TaskDetail uses;
  // the server derives the stored rater_role the same way and ignores whatever
  // the client thinks, so this only decides which form is drawn.
  const isRequester = Boolean(user?.id && task?.requester_id && user.id === task.requester_id)
  const isSupporter = Boolean(user?.id && task?.assigned_to_id && user.id === task.assigned_to_id)

  // For the length of the Traction 3 round the questionnaire replaces the
  // classic review form, and the supporter gets one of their own. When the
  // window passes, both revert on their own: the flag is a date check.
  const questionnaireActive = isTractionWindowActive()

  if (questionnaireActive && (isRequester || isSupporter)) {
    return (
      <ReviewShell taskID={taskID}>
        <div>
          <h1 className="text-white text-2xl font-semibold leading-snug">How did it go?</h1>
          <p className="text-white/50 text-sm mt-1">
            {isRequester ? `${supporterName} · ` : ''}{task?.title || 'Task'}
          </p>
        </div>
        <TractionReviewForm
          role={isRequester ? 'requester' : 'supporter'}
          onSubmit={submitQuestionnaire}
          skipHref={`/tasks/${taskID}`}
        />
      </ReviewShell>
    )
  }

  // Outside the round there is nothing for a supporter to fill in: the classic
  // form is the requester rating the supporter, and posting it from the other
  // side would write a row with no answers in it.
  if (!isRequester) {
    return (
      <ReviewShell taskID={taskID}>
        <div className="text-center space-y-2">
          <h2 className="text-white text-xl font-semibold">Nothing to review</h2>
          <p className="text-white/50 text-sm">
            There's no review to leave for this task.
          </p>
          <Link to={`/tasks/${taskID}`} className="inline-block pt-2 text-[#9aab3a] text-sm hover:underline">
            Back to task
          </Link>
        </div>
      </ReviewShell>
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
