import { useState } from 'react'
import { Link } from 'react-router-dom'
import { createPortal } from 'react-dom';
import { api } from '../api/client'


function euro(cents) {
  return `€${(cents / 100).toFixed(2)}`
}

export default function CancelTaskButton({ taskId, disabled = false, onDone }) {
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [result, setResult] = useState(null) // {refund_cents, bill_cents, total_minutes}
  const [error, setError] = useState('')

  const start = () => {
    setOpen(true)
    setReason('')
    setResult(null)
    setError('')
  }

  async function submit() {
    if (!reason.trim()) { setError('Reason is required'); return }
    setSubmitting(true); setError('')
    try {
      const res = await api(`/tasks/${taskId}/cancel`, {
        method: 'POST',
        body: { reason: reason.trim() },
      })
      setResult(res)
      onDone?.(res) // 讓父層刷新列表
    } catch (e) {
      // 後端的錯通常有 message，例如 open worklog 存在
      setError(e?.message || 'Failed to cancel')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <>
      <button
        className="text-red-300 underline disabled:opacity-50"
        onClick={(e) => { e.stopPropagation(); start(); }}
        disabled={disabled}
      >
        Cancel
      </button>

      {open && createPortal(
        <div className="fixed inset-0 z-[100] flex items-center justify-center" onKeyDown={(e) => e.stopPropagation()}>
          <div className="absolute inset-0 bg-black/50" onClick={() => !submitting && setOpen(false)} />
          <div className="relative w-[520px] max-w-[90vw] rounded-xl border border-white/15 bg-zinc-900 p-4 shadow-xl">
            {!result ? (
              <>
                <h3 className="text-lg font-semibold mb-2">Cancel task</h3>
                <p className="text-sm text-accent mb-3">
                  Rules: if a work session is open you can’t cancel. If already worked, we bill recorded minutes and refund the difference.
                </p>
                <label className="text-sm grid gap-1 mb-2">
                  <span className="text-accent">Reason (required)</span>
                  <textarea
                    className="bg-transparent outline-none border border-white/10 focus:border-white/30 rounded px-2 py-2 min-h-[84px] text-accent"
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                    maxLength={300}
                    autoFocus 
                    onKeyDown={(e) => e.stopPropagation()}
                  />
                </label>
                {error && <div className="text-sm text-red-400 mb-2">{error}</div>}
                <div className="flex justify-end gap-2">
                  <button
                    className="px-3 py-1.5 rounded border border-white/10 hover:border-white/30 text-accent"
                    onClick={() => setOpen(false)}
                    disabled={submitting}
                  >
                    Close
                  </button>
                  <button
                    className="px-3 py-1.5 rounded bg-red-500/90 hover:bg-red-500 text-black disabled:opacity-60"
                    onClick={submit}
                    disabled={submitting}
                  >
                    {submitting ? 'Cancelling…' : 'Confirm cancel'}
                  </button>
                </div>
              </>
            ) : (
              <>
                <h3 className="text-lg font-semibold mb-2">Task cancelled</h3>
                <div className="text-sm mb-3 space-y-1">
                  <div className="flex justify-between"><span>Billed</span><span>{euro(result.bill_cents)} <span className="opacity-70">({result.total_minutes} min)</span></span></div>
                  <div className="flex justify-between"><span>Refund</span><span>{euro(result.refund_cents)}</span></div>
                </div>
                <div className="flex justify-end gap-2">
                  <Link to={`/tasks/${taskId}`} className="px-3 py-1.5 rounded border border-white/10 hover:border-white/30">View task</Link>
                  <button className="px-3 py-1.5 rounded bg-white/90 text-black hover:bg-white" onClick={() => setOpen(false)}>Done</button>
                </div>
              </>
            )}
          </div>
        </div>, document.body
      )}
    </>
  )
}
