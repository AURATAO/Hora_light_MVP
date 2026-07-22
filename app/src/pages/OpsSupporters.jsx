// Supporter application review panel — the ops-area counterpart to
// POST /ops/supporter-approve and /ops/supporter-reject (D-08). Rendered as a
// tab inside OpsFeed's page shell, so it inherits that page's admin gate.
import { useEffect, useState } from 'react'
import { useToast } from '../providers/ToastProvider'
import { approveSupporter, listSupporterApplications, rejectSupporter } from '../api/ops'

function formatWhen(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleDateString()
}

const STATUS_LABEL = {
  approved: 'Approved',
  rejected: 'Rejected',
  applied: 'Pending',
}

export default function OpsSupporters() {
  const toast = useToast()
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [busyEmail, setBusyEmail] = useState(null)
  const [showDecided, setShowDecided] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const data = await listSupporterApplications()
      setRows(Array.isArray(data) ? data : [])
    } catch (err) {
      console.error('[ops/supporter-applications] error', err)
      toast(err.message || "Couldn't load supporter applications")
      setRows([])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  // Optimistic: flip the row locally so the queue responds instantly, then
  // refetch for the server's version. On failure, restore the snapshot — the
  // row goes back to pending rather than lying about a decision that never
  // landed.
  async function decide(row, action) {
    const name = row.name || row.email || 'this applicant'
    const verb = action === 'approve' ? 'Approve' : 'Reject'
    if (!confirm(`${verb} ${name}?`)) return

    const snapshot = rows
    const optimisticStatus = action === 'approve' ? 'approved' : 'rejected'
    setRows(prev => prev.map(r => (
      r.email === row.email ? { ...r, supporter_status: optimisticStatus } : r
    )))
    setBusyEmail(row.email)

    try {
      if (action === 'approve') await approveSupporter(row.email)
      else await rejectSupporter(row.email)
      toast(`${name} ${optimisticStatus}.`, 'success')
      await load()
    } catch (err) {
      setRows(snapshot)
      toast(err.message || `Couldn't ${action} ${name}`)
    } finally {
      setBusyEmail(null)
    }
  }

  const pending = rows.filter(r => r.supporter_status === 'applied')
  const decided = rows.filter(r => r.supporter_status !== 'applied')

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">
          Pending applications {pending.length ? `(${pending.length})` : ''}
        </h2>
        <button className="border rounded px-3 py-1 text-sm" onClick={load} disabled={loading}>
          {loading ? 'Loading…' : 'Refresh'}
        </button>
      </div>

      <div className="border rounded overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-black/5">
            <tr>
              <th className="text-left p-2">Applicant</th>
              <th className="text-left p-2">Contact</th>
              <th className="text-left p-2">City</th>
              <th className="text-left p-2">Applied</th>
              <th className="text-left p-2">Actions</th>
            </tr>
          </thead>
          <tbody>
            {pending.map(r => (
              <tr key={r.id} className="border-t">
                <td className="p-2 font-medium">{r.name || '—'}</td>
                <td className="p-2">
                  <div className="text-xs">{r.email || '—'}</div>
                  <div className="text-xs opacity-70">{r.phone || '—'}</div>
                </td>
                <td className="p-2 text-xs">{r.city || '—'}</td>
                <td className="p-2 text-xs">{formatWhen(r.supporter_applied_at)}</td>
                <td className="p-2">
                  <div className="flex gap-1">
                    <button
                      className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40 disabled:opacity-50"
                      onClick={() => decide(r, 'approve')}
                      disabled={busyEmail === r.email}
                    >
                      Approve
                    </button>
                    <button
                      className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40 disabled:opacity-50"
                      onClick={() => decide(r, 'reject')}
                      disabled={busyEmail === r.email}
                    >
                      Reject
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {!pending.length && !loading && (
              <tr><td className="p-3 text-center opacity-60" colSpan="5">No pending applications</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <div>
        <button
          className="text-sm opacity-70 hover:opacity-100"
          onClick={() => setShowDecided(v => !v)}
        >
          {showDecided ? '▾' : '▸'} Decided ({decided.length})
        </button>

        {showDecided && (
          <div className="border rounded overflow-x-auto mt-2 opacity-70">
            <table className="w-full text-sm">
              <thead className="bg-black/5">
                <tr>
                  <th className="text-left p-2">Applicant</th>
                  <th className="text-left p-2">Contact</th>
                  <th className="text-left p-2">City</th>
                  <th className="text-left p-2">Applied</th>
                  <th className="text-left p-2">Status</th>
                </tr>
              </thead>
              <tbody>
                {decided.map(r => (
                  <tr key={r.id} className="border-t">
                    <td className="p-2 font-medium">{r.name || '—'}</td>
                    <td className="p-2">
                      <div className="text-xs">{r.email || '—'}</div>
                      <div className="text-xs opacity-70">{r.phone || '—'}</div>
                    </td>
                    <td className="p-2 text-xs">{r.city || '—'}</td>
                    <td className="p-2 text-xs">{formatWhen(r.supporter_applied_at)}</td>
                    <td className="p-2 text-xs">
                      <span className="inline-block border rounded px-1">
                        {STATUS_LABEL[r.supporter_status] || r.supporter_status}
                      </span>
                      {r.supporter_status === 'rejected' && r.supporter_rejected_at && (
                        <span className="ml-2 opacity-70">{formatWhen(r.supporter_rejected_at)}</span>
                      )}
                    </td>
                  </tr>
                ))}
                {!decided.length && (
                  <tr><td className="p-3 text-center opacity-60" colSpan="5">Nothing decided yet</td></tr>
                )}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
