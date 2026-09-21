import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import Modal from './Modal'
import { formatCents } from '../hooks/useTaskEstimate'
import {
  cancelChargeLine,
  cancelGraceCountdown,
  cancelReleaseLine,
  holdReleasedMessage,
} from '../lib/paymentCopy'

/**
 * The requester's way out of their own task, at every stage of it.
 *
 * WHAT CHANGED, AND WHY IT IS NOT JUST A COPY EDIT. This used to be offered
 * only on an OPEN, unaccepted task, and its dialog described the rules rather
 * than the amounts — "if work has already been recorded, you are billed for
 * that time only". Once a supporter accepted, there was no requester-side exit
 * at all: the server refused the cancel and ops had to do it by hand.
 *
 * Both halves are gone. The policy is now "the base fee is the supporter's
 * guarantee, so once they've committed, cancelling still pays it" (server/
 * billing.go cancelSettlementCents), and a requester is shown the actual
 * numbers that rule produces for THIS task before they commit to anything.
 *
 * Every figure comes from the server's `cancellation` block (S-05). Nothing
 * here multiplies, adds or compares money — the one thing this component does
 * with a number is count a deadline down.
 */
export default function CancelTaskButton({
  taskId,
  disabled = false,
  onDone,
  // The trigger's shape. The card's list row wants the old inline link; the
  // task detail page wants a secondary button sitting with the other actions.
  variant = 'link',
  label = 'Cancel',
}) {
  const [open, setOpen] = useState(false)
  const [reasonCode, setReasonCode] = useState('')
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState(null)
  const [error, setError] = useState('')
  // What cancelling right now would do, fetched when the dialog opens.
  //
  // It cannot come from the card that renders this button: list payloads carry
  // no `cancellation` block (it is requester-only and attached by the
  // task-detail handler). One GET on a deliberate user action is cheap, and it
  // guarantees the amount quoted is the current one rather than a stale list's.
  const [cancellation, setCancellation] = useState(null)
  const [hold, setHold] = useState(null)
  // Ticks only while a grace countdown is on screen. A requester watching
  // "free for 0:41 more" needs it to actually move, and needs the dialog to
  // change its mind about the money when it hits zero.
  const [now, setNow] = useState(() => Date.now())

  const graceEndsAt = cancellation?.grace_ends_at ?? null
  const withinGrace = Boolean(graceEndsAt) && Date.parse(graceEndsAt) > now
  const countdown = cancelGraceCountdown(graceEndsAt, now)

  useEffect(() => {
    if (!open || !graceEndsAt) return undefined
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [open, graceEndsAt])

  function start() {
    setOpen(true)
    setReasonCode('')
    setNote('')
    setResult(null)
    setError('')
    setCancellation(null)
    setHold(null)
    setNow(Date.now())
    // Silent on failure: a cancel must never be blocked by not knowing what it
    // costs. The dialog falls back to saying nothing about money, which is
    // what it did before any of this existed.
    api(`/tasks/${taskId}`)
      .then((t) => {
        setCancellation(t?.cancellation ?? null)
        setHold(t?.payment ?? null)
      })
      .catch(() => {})
  }

  const reasons = cancellation?.reasons ?? []
  const needsNote = reasonCode === 'other'

  async function submit() {
    if (!reasonCode) { setError('Pick a reason'); return }
    // The free text is still sent: it is what the ops feed and the audit row
    // record. Only the CODE is ever relayed to the supporter — see
    // server/cancel_reasons.go.
    const chosen = reasons.find((r) => r.value === reasonCode)
    const reason = needsNote ? (note.trim() || 'Other') : (chosen?.label || reasonCode)
    setSubmitting(true)
    setError('')
    try {
      const res = await api(`/tasks/${taskId}/cancel`, {
        method: 'POST',
        body: { reason, reason_code: reasonCode },
      })
      setResult(res)
      onDone?.(res)
    } catch (e) {
      const msg = e?.body?.error || e?.body?.message || (typeof e?.body === 'string' && e.body) || e?.message || 'Failed to cancel'
      setError(msg)
    } finally {
      setSubmitting(false)
    }
  }

  const releasedMessage = holdReleasedMessage(result)
  // Recomputed against the live countdown rather than read off the fetch: the
  // dialog can sit open across the boundary, and a requester who reads "free"
  // and taps twenty seconds later must not be surprised by a charge.
  const chargeLine = cancelChargeLine(cancellation, { withinGrace })
  const releaseLine = cancelReleaseLine(cancellation, hold, { withinGrace })

  const trigger = variant === 'button' ? (
    <button
      className="rounded-md border border-red-400/40 px-3 py-1.5 text-sm text-red-300 hover:border-red-400/70 disabled:opacity-50"
      onClick={(e) => { e.stopPropagation(); start() }}
      disabled={disabled}
    >
      {label}
    </button>
  ) : (
    <button
      className="text-red-300 underline disabled:opacity-50"
      onClick={(e) => { e.stopPropagation(); start() }}
      disabled={disabled}
    >
      {label}
    </button>
  )

  return (
    <>
      {trigger}

      <Modal
        open={open}
        onClose={() => !submitting && setOpen(false)}
        title={result ? 'Task cancelled' : 'Cancel this task?'}
        actions={
          result ? (
            <>
              <Link
                to={`/tasks/${taskId}`}
                className="px-3 py-1.5 rounded-md border border-white/20 hover:border-white/40 text-accent"
              >
                View task
              </Link>
              <button
                className="px-3 py-1.5 rounded-md bg-white/90 text-black hover:bg-white"
                onClick={() => setOpen(false)}
              >
                Done
              </button>
            </>
          ) : (
            <>
              <button
                className="px-3 py-1.5 rounded-md border border-white/20 hover:border-white/40 text-accent"
                onClick={() => setOpen(false)}
                disabled={submitting}
              >
                Never mind
              </button>
              <button
                className="px-3 py-1.5 rounded-md bg-red-500/90 hover:bg-red-500 text-black disabled:opacity-60"
                onClick={submit}
                disabled={submitting || !reasonCode}
              >
                {submitting ? 'Cancelling…' : 'Yes, cancel task'}
              </button>
            </>
          )
        }
      >
        {result ? (
          <div className="space-y-2 text-sm">
            <div className="flex justify-between">
              <span>Billed</span>
              <span>
                {formatCents(result.bill_cents)}{' '}
                <span className="opacity-70">({result.total_minutes} min)</span>
              </span>
            </div>
            {/* What happened to the money, in the server's own numbers. The
                cancel path has always released the hold correctly and has
                never said so — which left a requester watching a reservation
                sit on their statement with no idea it was already reversed. */}
            {releasedMessage && <p className="text-white/70">{releasedMessage}</p>}
          </div>
        ) : (
          <>
            {/* THE LIVE COUNTDOWN. Inside the grace window this is the whole
                message: the cancel is free right now and will not be in a
                moment, and a requester deciding in that moment is owed the
                seconds rather than a policy sentence. */}
            {withinGrace && countdown && (
              <p className="mb-3 rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm">
                Free cancellation for <span className="font-semibold">{countdown}</span> more — after
                that, the{' '}
                {/* The server's number, never a literal: the base fee differs
                    by category ($25 for companionship) and a hardcoded $12
                    would quietly understate half of them. */}
                {cancellation?.base_fee_cents ? `${formatCents(cancellation.base_fee_cents)} ` : ''}
                base fee goes to your supporter.
              </p>
            )}

            {/* The numbers, not the rules. Null on a task with no supporter,
                where cancelling has never cost anything. */}
            {chargeLine ? (
              <p className="mb-3">{chargeLine}</p>
            ) : (
              <p className="mb-3">
                Nobody has accepted this task yet, so cancelling it costs nothing.
              </p>
            )}
            {releaseLine && <p className="mb-3 text-white/70">{releaseLine}</p>}

            {/* A closed set, matching mobile and the server's own vocabulary.
                It used to be a free-text box, which is why a cancellation
                reason could never be relayed to the supporter: there was no
                way to tell "plans changed" from something written about
                them. */}
            <div className="space-y-1.5">
              <div className="text-xs text-white/60">Why are you cancelling?</div>
              <div className="space-y-1.5">
                {reasons.map((opt) => {
                  const selected = reasonCode === opt.value
                  return (
                    <button
                      key={opt.value}
                      type="button"
                      aria-pressed={selected}
                      onClick={() => setReasonCode(opt.value)}
                      className={[
                        'w-full flex items-center gap-2.5 text-left rounded-lg border px-3 py-2.5 text-sm transition',
                        selected ? 'border-white bg-white/10' : 'border-white/15 hover:border-white/30',
                      ].join(' ')}
                    >
                      <span
                        className={[
                          'shrink-0 h-4 w-4 rounded-full border grid place-items-center',
                          selected ? 'border-white' : 'border-white/40',
                        ].join(' ')}
                      >
                        {selected && <span className="h-2 w-2 rounded-full bg-white" />}
                      </span>
                      <span>{opt.label}</span>
                    </button>
                  )
                })}
              </div>
              {needsNote && (
                <textarea
                  className="w-full bg-transparent outline-none border border-white/10 focus:border-white/30 rounded px-2 py-2 min-h-[64px] text-accent resize-none"
                  placeholder="What happened? (only the HO:RA team sees this)"
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  maxLength={300}
                  autoFocus
                  onKeyDown={(e) => e.stopPropagation()}
                />
              )}
            </div>
            {error && <p className="mt-2 text-red-400 text-sm">{error}</p>}
          </>
        )}
      </Modal>
    </>
  )
}
