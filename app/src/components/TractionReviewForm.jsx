import { useState } from 'react'
import { Link } from 'react-router-dom'
import {
  EASE_OPTIONS,
  USE_AGAIN_OPTIONS,
  TRACTION_QUESTIONS,
  buildTractionReviewPayload,
  isTractionReviewComplete,
} from '../lib/tractionReview'

const STARS = [1, 2, 3, 4, 5]

// Same pill as the classic form's value-rating row, so the questionnaire reads
// as the same screen it replaces.
function Pill({ label, selected, onClick }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={selected}
      className={`px-4 py-2 rounded-full border text-sm transition-all ${
        selected
          ? 'border-[#9aab3a] bg-[#9aab3a]/15 text-[#9aab3a]'
          : 'border-white/15 text-white/60 hover:border-white/30 hover:text-white/80'
      }`}
    >
      {label}
    </button>
  )
}

/**
 * The Traction 3 post-task questionnaire, replacing the classic review form for
 * the length of the round (see isTractionWindowActive). The web counterpart of
 * mobile's TractionReviewSheet — same questions, same slugs, web styling.
 *
 * Skipping writes nothing at all — no placeholder row — so the questionnaire
 * can still be answered later.
 *
 * `role` is presentation only: the server derives the stored rater_role from
 * the caller's relation to the task and ignores anything the client claims.
 */
export default function TractionReviewForm({ role, onSubmit, skipHref }) {
  const copy = TRACTION_QUESTIONS[role]
  const isRequester = role === 'requester'

  const [ease, setEase] = useState('')
  const [stars, setStars] = useState(0)
  const [hovered, setHovered] = useState(0)
  const [useAgain, setUseAgain] = useState('')
  const [openFeedback, setOpenFeedback] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  const answers = { ease, stars, useAgain, openFeedback }
  const complete = isTractionReviewComplete(role, answers)

  async function submit(e) {
    e.preventDefault()
    if (!complete || submitting) return
    setError('')
    setSubmitting(true)
    try {
      await onSubmit(buildTractionReviewPayload(role, answers))
    } catch (err) {
      // A 409 is handled by the page (it swaps in the "already submitted"
      // state), so anything reaching here is worth retrying — the form keeps
      // its answers and the button comes back.
      setError(err?.message || "Couldn't submit your answers. Try again.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={submit} className="space-y-6">

      {/* Q1 — Ease / flow clarity */}
      <div className="space-y-2">
        <p className="text-white/70 text-sm font-medium">{copy.ease}</p>
        <div className="flex flex-wrap gap-2">
          {EASE_OPTIONS.map(opt => (
            <Pill
              key={opt.value}
              label={opt.label}
              selected={ease === opt.value}
              onClick={() => setEase(v => (v === opt.value ? '' : opt.value))}
            />
          ))}
        </div>
      </div>

      {/* Q2 — Stars. Requester only: the supporter's questionnaire rates nobody. */}
      {isRequester && (
        <div className="space-y-2">
          <p className="text-white/70 text-sm font-medium">{copy.stars}</p>
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
                <span className={s <= (hovered || stars) ? 'text-yellow-400' : 'text-white/20'}>★</span>
              </button>
            ))}
          </div>
        </div>
      )}

      {/* Q3 — Would use / work again */}
      <div className="space-y-2">
        <p className="text-white/70 text-sm font-medium">{copy.useAgain}</p>
        <div className="flex flex-wrap gap-2">
          {USE_AGAIN_OPTIONS.map(opt => (
            <Pill
              key={opt.value}
              label={opt.label}
              selected={useAgain === opt.value}
              onClick={() => setUseAgain(v => (v === opt.value ? '' : opt.value))}
            />
          ))}
        </div>
      </div>

      {/* Q4 — Open feedback, the only optional answer */}
      <div className="space-y-2">
        <p className="text-white/70 text-sm font-medium">
          {copy.open} <span className="text-white/30 font-normal">(optional)</span>
        </p>
        <textarea
          value={openFeedback}
          onChange={e => setOpenFeedback(e.target.value)}
          rows={3}
          placeholder="Optional"
          className="w-full bg-white/5 border border-white/10 rounded-xl px-4 py-3
                     text-white text-sm placeholder-white/25 resize-none
                     focus:outline-none focus:border-[#9aab3a]/50 transition-colors"
        />
      </div>

      {error && <p className="text-red-400 text-sm">{error}</p>}

      <div className="space-y-3 pt-1">
        <button
          type="submit"
          disabled={!complete || submitting}
          className="w-full py-3 rounded-xl bg-[#9aab3a] text-white font-semibold
                     text-sm hover:brightness-110 transition-all disabled:opacity-50
                     disabled:cursor-not-allowed disabled:hover:brightness-100"
        >
          {submitting ? 'Submitting…' : 'Submit'}
        </button>
        <div className="text-center">
          <Link to={skipHref} className="text-white/35 text-sm hover:text-white/60 transition-colors">
            Skip for now
          </Link>
        </div>
      </div>
    </form>
  )
}
