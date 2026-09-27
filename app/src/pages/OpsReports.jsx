// User reports — the ops view of POST /safety/report (server/safety.go).
// Rendered as a tab inside OpsFeed's page shell, so it inherits that page's
// admin gate; the backend re-checks the allowlist on every call regardless.
//
// Apple expects reports on a platform with chat to be acted on within 24
// hours (Guideline 1.2). Every report also emails the ops allowlist; this is
// where one is read in context and marked handled. Acting on a report —
// removing a task, contacting the people involved — happens with the tools
// that already exist on the Task feed tab.
import { useEffect, useState } from 'react'
import { useToast } from '../providers/ToastProvider'
import { listUserReports, resolveUserReport } from '../api/ops'

function formatWhen(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

export default function OpsReports() {
  const toast = useToast()
  const [status, setStatus] = useState('open')
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [busyId, setBusyId] = useState(null)

  async function load(which = status) {
    setLoading(true)
    try {
      const data = await listUserReports(which)
      setRows(Array.isArray(data?.reports) ? data.reports : [])
    } catch (err) {
      console.error('[ops/reports] error', err)
      toast(err.message || "Couldn't load reports")
      setRows([])
    } finally {
      setLoading(false)
    }
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { load(status) }, [status])

  async function resolve(r) {
    const note = window.prompt(`Resolve the report against ${r.reported_name || r.reported_email}? What did you do?`, '')
    if (note === null) return
    setBusyId(r.id)
    try {
      await resolveUserReport(r.id, note.trim())
      toast('Report resolved.', 'success')
      await load()
    } catch (err) {
      toast(err.message || "Couldn't resolve that report")
    } finally {
      setBusyId(null)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <select className="border rounded px-2 py-1" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="open">Open reports</option>
          <option value="all">All reports</option>
        </select>
        <button className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40" onClick={() => load()}>
          Refresh
        </button>
      </div>

      {loading ? (
        <p className="text-sm opacity-70">Loading…</p>
      ) : rows.length === 0 ? (
        <p className="text-sm opacity-70">{status === 'open' ? 'No open reports.' : 'No reports yet.'}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left opacity-70">
              <tr>
                <th className="py-2 pr-3">Filed</th>
                <th className="py-2 pr-3">Reporter</th>
                <th className="py-2 pr-3">Reported</th>
                <th className="py-2 pr-3">Reason</th>
                <th className="py-2 pr-3">Details</th>
                <th className="py-2 pr-3">Task</th>
                <th className="py-2 pr-3">Status</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id} className="border-t border-white/10 align-top">
                  <td className="py-2 pr-3 whitespace-nowrap">{formatWhen(r.created_at)}</td>
                  <td className="py-2 pr-3">
                    <div>{r.reporter_name}</div>
                    <div className="text-xs opacity-60">{r.reporter_email}</div>
                  </td>
                  <td className="py-2 pr-3">
                    <div>{r.reported_name}</div>
                    <div className="text-xs opacity-60">{r.reported_email}</div>
                    <div className="text-xs opacity-60">
                      {r.reported_count} report{r.reported_count === 1 ? '' : 's'} in total
                      {r.blocked ? ' · blocked by reporter' : ''}
                    </div>
                  </td>
                  <td className="py-2 pr-3">{r.reason_label || r.reason_code}</td>
                  <td className="py-2 pr-3 max-w-xs whitespace-pre-wrap">{r.details || '—'}</td>
                  <td className="py-2 pr-3">
                    <a className="underline" href={`/tasks/${r.task_id}`} target="_blank" rel="noopener noreferrer">
                      {r.task_title || r.task_id}
                    </a>
                  </td>
                  <td className="py-2 pr-3">
                    {r.resolved_at ? (
                      <div>
                        <div>Resolved {formatWhen(r.resolved_at)}</div>
                        {r.resolution && <div className="text-xs opacity-60">{r.resolution}</div>}
                      </div>
                    ) : (
                      <button
                        className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40 disabled:opacity-50"
                        disabled={busyId === r.id}
                        onClick={() => resolve(r)}
                      >
                        {busyId === r.id ? 'Resolving…' : 'Mark resolved'}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
