import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import Modal from './Modal'
import { formatCents } from '../hooks/useTaskEstimate'
import { holdReleasedMessage, holdWillBeReleasedMessage } from '../lib/paymentCopy'

export default function CancelTaskButton({ taskId, disabled = false, onDone }) {
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState(null)
  const [error, setError] = useState('')
  // The hold standing against this task, fetched when the dialog opens.
  //
  // It cannot come from the card that renders this button: list payloads carry
  // no `payment` block (it is requester-only and attached by the task-detail
  // handler). One GET on a deliberate user action is cheap, and it guarantees
  // the amount promised back is the current one rather than a stale list's.
  const [hold, setHold] = useState(null)

  function start() {
    setOpen(true)
    setReason('')
    setResult(null)
    setError('')
    setHold(null)
    // Silent on failure: a cancel must never be blocked by not knowing the
    // hold. The dialog simply says nothing about money, which is what it did
    // before this existed.
    api(`/tasks/${taskId}`).then((t) => setHold(t?.payment ?? null)).catch(() => {})
  }

  async function submit() {
    if (!reason.trim()) { setError('Reason is required'); return }
    setSubmitting(true)
    setError('')
    try {
      const res = await api(`/tasks/${taskId}/cancel`, {
        method: 'POST',
        body: { reason: reason.trim() },
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

  const willReleaseMessage = holdWillBeReleasedMessage(hold)
  const releasedMessage = holdReleasedMessage(result)

  return (
    <>
      <button
        className="text-red-300 underline disabled:opacity-50"
        onClick={(e) => { e.stopPropagation(); start() }}
        disabled={disabled}
      >
        Cancel
      </button>

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
                disabled={submitting}
              >
                {submitting ? 'Cancelling\u2026' : 'Yes, cancel task'}
              </button>
            </>
          )
        }
      >
        {result ? (
          <div className="space-y-2 text-sm">
            <div className="flex justify-between">
              <span>Billed</span>
              <span>{formatCents(result.bill_cents)} <span className="opacity-70">({result.total_minutes} min)</span></span>
            </div>
            {/* What happened to the money, in the server's own numbers. The
                cancel path has always released the hold correctly and has
                never said so — which left a requester watching a reservation
                sit on their statement with no idea it was already reversed. */}
            {releasedMessage && <p className="text-white/70">{releasedMessage}</p>}
          </div>
        ) : (
          <>
            <p className="mb-3">
              If a work session is open you cannot cancel. If nobody has clocked in yet, cancelling costs nothing. If work has already been recorded, you are billed for that time only — your shopping budget is never charged.
            </p>
            {/* Named before they commit, not after. Null until the fetch lands
                and on every task with no hold. */}
            {willReleaseMessage && (
              <p className="mb-3 text-white/70">{willReleaseMessage}</p>
            )}
            <label className="grid gap-1">
              <span>Reason (required)</span>
              <textarea
                className="bg-transparent outline-none border border-white/10 focus:border-white/30 rounded px-2 py-2 min-h-[84px] text-accent resize-none"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                maxLength={300}
                autoFocus
                onKeyDown={(e) => e.stopPropagation()}
              />
            </label>
            {error && <p className="mt-2 text-red-400 text-sm">{error}</p>}
          </>
        )}
      </Modal>
    </>
  )
}
