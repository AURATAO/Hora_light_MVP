import { useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import Modal from './Modal'

function euro(cents) {
  return `\u20AC${(cents / 100).toFixed(2)}`
}

export default function CancelTaskButton({ taskId, disabled = false, onDone }) {
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState(null)
  const [error, setError] = useState('')

  function start() {
    setOpen(true)
    setReason('')
    setResult(null)
    setError('')
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
                className="px-3 py-1.5 rounded border border-white/20 hover:border-white/40 text-accent"
              >
                View task
              </Link>
              <button
                className="px-3 py-1.5 rounded bg-white/90 text-black hover:bg-white"
                onClick={() => setOpen(false)}
              >
                Done
              </button>
            </>
          ) : (
            <>
              <button
                className="px-3 py-1.5 rounded border border-white/20 hover:border-white/40 text-accent"
                onClick={() => setOpen(false)}
                disabled={submitting}
              >
                Never mind
              </button>
              <button
                className="px-3 py-1.5 rounded bg-red-500/90 hover:bg-red-500 text-black disabled:opacity-60"
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
          <div className="space-y-1 text-sm">
            <div className="flex justify-between">
              <span>Billed</span>
              <span>{euro(result.bill_cents)} <span className="opacity-70">({result.total_minutes} min)</span></span>
            </div>
            <div className="flex justify-between">
              <span>Refund</span>
              <span>{euro(result.refund_cents)}</span>
            </div>
          </div>
        ) : (
          <>
            <p className="mb-3">
              If a work session is open you cannot cancel. If work has already been recorded, we will bill those minutes and refund the rest.
            </p>
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
