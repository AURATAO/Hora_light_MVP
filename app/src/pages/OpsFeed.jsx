// src/pages/OpsFeed.jsx
import { useEffect, useMemo, useState } from 'react'
import { useAuth } from "../auth/AuthContext.jsx";
import { useToast } from '../providers/ToastProvider'
import { opsFetch as fetchJSON, removeTask, reassignTask, listApprovedSupporters,
         forceCompleteTask, adminCancelTask, adjustTaskTime, REMOVAL_REASONS } from '../api/ops'
import OpsSupporters from './OpsSupporters.jsx'
import Modal from '../components/Modal.jsx'

// Display gate only — the server re-checks every /ops/* call against its own
// hardcoded allowlist (S-14/S-60.5), which stays the authorization system.
const ADMIN_EMAILS = new Set([
  'auratao.model@gmail.com',
  'taoaura.lavoro@gmail.com',
  'liang.you@horaapp.co',
  'liang.you@arcodiax.com',
  'rollod4@gmail.com',
  'daniele@arcodiax.com'
]);

const STATUS_FILTERS = ['all','accepted','completed','cancelled','removed'];

const TABS = [
  { key: 'tasks', label: 'Task feed' },
  { key: 'supporters', label: 'Supporter applications' },
];

export default function OpsFeed() {
  const { user } = useAuth()
  const toast = useToast()
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('all')
  const [q, setQ] = useState('')
  const [tab, setTab] = useState('tasks')
  // Takedown dialog: null when closed, otherwise the row being removed.
  const [removing, setRemoving] = useState(null)
  const [removeReason, setRemoveReason] = useState(REMOVAL_REASONS[0].value)
  const [removeNote, setRemoveNote] = useState('')
  const [removeBusy, setRemoveBusy] = useState(false)
  // Reassign dialog: null when closed, otherwise the row whose supporter is
  // being rotated. The approved-supporter list is fetched once on first open
  // and kept — it changes far less often than the feed.
  const [reassigning, setReassigning] = useState(null)
  const [supporters, setSupporters] = useState(null)
  const [supportersError, setSupportersError] = useState('')
  const [pickedSupporter, setPickedSupporter] = useState('')
  const [reassignBusy, setReassignBusy] = useState(false)

  const allowed = !!user && ADMIN_EMAILS.has(user.email)

  // Any logged time means a worklog exists, which is exactly what the reassign
  // endpoint refuses (task_in_progress). Checked here too so the dialog explains
  // it instead of spending a round-trip on a guaranteed 409.
  const reassignStarted =
    !!reassigning &&
    ((reassigning.running_minutes || 0) > 0 || (reassigning.total_minutes_done || 0) > 0)

  async function load() {
    if (!allowed) return
    setLoading(true)
    try {
      const data = await fetchJSON(`/ops/feed?status=${encodeURIComponent(filter||'all')}&q=${encodeURIComponent(q||'')}`)
      setRows(Array.isArray(data) ? data : [])
    } catch (err) {
      console.error('[ops/feed] error', err)
      setRows([])
    } finally {
      setLoading(false)
    }
  }

  // 狀態切換時重新載入（查詢字串 q 即時在前端過濾，按 Refresh 再打後端）
  useEffect(() => { load(); }, [allowed, filter]);

  // 除錯：看後端辨識的身份
  useEffect(() => {
    (async () => {
      try {
        const me = await fetchJSON('/auth/me');
        console.log('auth/me ->', me);
      } catch (e) {
        console.log('auth/me fail', e);
      }
    })();
  }, []);

  // 前端即時搜尋（不阻擋後端的 q 參數，你也可以改成每次打字就打後端）
  const filtered = useMemo(() => {
    if (!q) return rows;
    const k = q.toLowerCase();
    return rows.filter(r =>
      (`${r.title ?? ''} ${r.location_text ?? ''} ${r.requester_email ?? ''} ${r.supporter_email ?? ''}`)
        .toLowerCase()
        .includes(k)
    );
  }, [rows, q]);

  // --- 動作：強制完成
  async function forceComplete(id) {
    if (!confirm('Force complete this task?')) return;
    try {
      const res = await forceCompleteTask(id);
      toast(
        res?.already_completed
          ? 'Task was already completed'
          : `Task completed — ${res.total_minutes}m logged, both parties notified`
      );
      load();
    } catch (e) {
      toast(e.message || 'Force complete failed');
    }
  }

  // --- 動作：取消
  async function cancelTask(id) {
    const reason = prompt('Cancel reason (optional):');
    if (reason == null) return;
    try {
      const res = await adminCancelTask(id, reason);
      toast(res?.already_cancelled ? 'Task was already cancelled' : 'Task cancelled — both parties notified');
      load();
    } catch (e) {
      toast(e.message || 'Cancel failed');
    }
  }

  // --- 動作：時間調整（+/- 分鐘）
  async function adjustTime(id) {
    const raw = prompt('Add minutes (+/-):', '5');
    if (raw == null) return;
    const delta = parseInt(raw, 10);
    if (!Number.isFinite(delta) || delta === 0) return;
    try {
      const res = await adjustTaskTime(id, delta);
      toast(`Adjusted by ${delta > 0 ? '+' : ''}${delta}m — ${res.total_minutes}m logged in total`);
      load();
    } catch (e) {
      toast(e.message || 'Adjust failed');
    }
  }

  // --- 動作：平台下架（違反 beta 範圍）
  function openRemove(row) {
    setRemoving(row)
    setRemoveReason(REMOVAL_REASONS[0].value)
    setRemoveNote('')
  }

  async function confirmRemove() {
    if (!removing) return
    setRemoveBusy(true)
    try {
      const res = await removeTask(removing.task_id, removeReason, removeNote)
      toast(res?.already_removed ? 'Task was already removed' : 'Task removed — both parties notified')
      setRemoving(null)
      load()
    } catch (e) {
      toast(e.message || 'Remove failed')
    } finally {
      setRemoveBusy(false)
    }
  }

  // --- 動作：換人（支援者輪替 / 直接指派）
  async function openReassign(row) {
    setReassigning(row)
    setPickedSupporter('')
    setSupportersError('')
    if (supporters) return
    try {
      const list = await listApprovedSupporters()
      setSupporters(Array.isArray(list) ? list : [])
    } catch (e) {
      setSupportersError(e.message || 'Could not load supporters')
      setSupporters([])
    }
  }

  async function confirmReassign() {
    if (!reassigning || !pickedSupporter) return
    setReassignBusy(true)
    try {
      const res = await reassignTask(reassigning.task_id, pickedSupporter)
      toast(
        res?.previous_supporter
          ? `Reassigned to ${res.new_supporter_name || res.new_supporter} — all three parties notified`
          : `Assigned to ${res.new_supporter_name || res.new_supporter} — requester notified`
      )
      setReassigning(null)
      load()
    } catch (e) {
      toast(e.message || 'Reassign failed')
      // A lost race or a clock-in that landed first means this feed row is
      // stale, so refresh it rather than leaving the admin looking at it.
      if (e.status === 409) load()
    } finally {
      setReassignBusy(false)
    }
  }

  if (!user) return <div className="p-6">Please sign in.</div>;
  if (!allowed) return <div className="p-6">You are not authorized to view Ops.</div>;

  return (
    <div className="min-h-screen w-full flex justify-center bg-linear-to-br from-primary to-primary/30 text-accent">
      <div className="p-4 max-w-7xl w-full">
        <h1 className="text-xl font-semibold">Ops</h1>

        <div className="flex gap-2 pt-4">
          {TABS.map(t => (
            <button
              key={t.key}
              onClick={() => setTab(t.key)}
              className={`rounded-md border px-3 py-1 text-sm ${
                tab === t.key ? 'border-white/40 bg-white/10' : 'border-white/20 opacity-70 hover:opacity-100'
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>

        {tab === 'supporters' ? (
          <div className="py-4"><OpsSupporters /></div>
        ) : (
        <>
        <div className="grid grid-cols-1 sm:grid-cols-12 gap-2 py-4">
        <select
            className="border rounded px-2 py-1 sm:col-span-3"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
        >
            {STATUS_FILTERS.map((s) => (
            <option key={s} value={s}>{s}</option>
            ))}
        </select>

        <input
            className="border rounded px-2 py-1 sm:col-span-7 min-w-0 md:min-w-[280px]"
            placeholder="Search title/email/city"
            value={q}
            onChange={(e) => setQ(e.target.value)}
        />

        <button
            className="border rounded px-3 py-1 sm:col-span-2"
            onClick={load}
            disabled={loading}
        >
            {loading ? 'Loading…' : 'Refresh'}
        </button>
        </div>

        <div className="border rounded overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-black/5">
              <tr>
                <th className="text-left p-2">Task</th>
                <th className="text-left p-2">People</th>
                <th className="text-left p-2">Status</th>
                <th className="text-left p-2">Timeline</th>
                <th className="text-left p-2">Prepay</th>
                <th className="text-left p-2">Flags</th>
                <th className="text-left p-2">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(r => (
                <tr key={r.task_id} className="border-t">
                  <td className="p-2">
                    <div className="font-medium">{r.title}</div>
                    <div className="text-xs opacity-70">{r.location_text || '—'}</div>
                  </td>
                  <td className="p-2">
                    <div className="text-xs">Req: {r.requester_email || '—'}</div>
                    <div className="text-xs">Sup: {r.supporter_email || '—'}</div>
                  </td>
                  <td className="p-2 capitalize">{r.status}</td>
                  <td className="p-2 text-xs">
                    {r.first_start_at ? new Date(r.first_start_at).toLocaleTimeString() : '—'}
                    {' → '}
                    {r.last_end_at ? new Date(r.last_end_at).toLocaleTimeString() : (r.running_minutes ? 'running' : '—')}
                    {r.duration_minutes ? ` (${r.duration_minutes}m)` : ''}
                  </td>
                  <td className="p-2">
                    {typeof r.prepay_amount === 'number' ? `$${r.prepay_amount.toFixed(2)}` : '—'}
                  </td>
                  <td className="p-2 text-xs">
                    {r.running_minutes > 0 && <span className="inline-block border rounded px-1 mr-1">running</span>}
                    {r.status === 'cancelled' && <span className="inline-block border rounded px-1">cancelled</span>}
                    {r.status === 'removed' && <span className="inline-block border border-red-400/60 text-red-300 rounded px-1">removed</span>}
                  </td>
                  <td className="p-2">
                    <div className="flex gap-1">
                      <button className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40" onClick={()=>adjustTime(r.task_id)}>Adjust</button>
                      <button className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40" onClick={()=>forceComplete(r.task_id)}>Force</button>
                      <button className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40" onClick={()=>cancelTask(r.task_id)}>Cancel</button>
                      {r.status === 'open' && (
                        <>
                          <button
                            className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
                            onClick={()=>openReassign(r)}
                          >
                            Reassign
                          </button>
                          <button
                            className="rounded-md border border-red-400/50 px-2 py-1 text-xs text-red-300 hover:border-red-400"
                            onClick={()=>openRemove(r)}
                          >
                            Remove
                          </button>
                        </>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
              {!filtered.length && !loading && (
                <tr><td className="p-3 text-center opacity-60" colSpan="7">No tasks</td></tr>
              )}
            </tbody>
          </table>
        </div>
        </>
        )}
      </div>

      <Modal
        open={!!removing}
        onClose={() => (removeBusy ? null : setRemoving(null))}
        title="Remove task"
        actions={
          <>
            <button
              className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40"
              onClick={() => setRemoving(null)}
              disabled={removeBusy}
            >
              Cancel
            </button>
            <button
              className="rounded-md border border-red-400/60 px-3 py-1 text-sm text-red-300 hover:border-red-400"
              onClick={confirmRemove}
              disabled={removeBusy}
            >
              {removeBusy ? 'Removing…' : 'Remove task'}
            </button>
          </>
        }
      >
        <p className="mb-3">
          Take down <span className="font-medium text-white">{removing?.title}</span>? The requester is
          notified with the reason below{removing?.supporter_email ? ', and the supporter is detached and told the task is no longer available' : ''}.
        </p>

        <label className="block text-xs uppercase tracking-wide opacity-70 mb-1">Reason</label>
        <select
          className="w-full border rounded px-2 py-1 mb-3 bg-transparent"
          value={removeReason}
          onChange={(e) => setRemoveReason(e.target.value)}
        >
          {REMOVAL_REASONS.map((r) => (
            <option key={r.value} value={r.value} className="text-black">{r.label}</option>
          ))}
        </select>

        <label className="block text-xs uppercase tracking-wide opacity-70 mb-1">
          Internal note (optional)
        </label>
        <textarea
          className="w-full border rounded px-2 py-1 bg-transparent"
          rows={2}
          placeholder="Only recorded in the audit log — never shown to the user"
          value={removeNote}
          onChange={(e) => setRemoveNote(e.target.value)}
        />
      </Modal>

      <Modal
        open={!!reassigning}
        onClose={() => (reassignBusy ? null : setReassigning(null))}
        title={reassigning?.supporter_email ? 'Reassign supporter' : 'Assign supporter'}
        actions={
          <>
            <button
              className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40"
              onClick={() => setReassigning(null)}
              disabled={reassignBusy}
            >
              Cancel
            </button>
            <button
              className="rounded-md border border-white/40 px-3 py-1 text-sm hover:border-white/70 disabled:opacity-40"
              onClick={confirmReassign}
              disabled={reassignBusy || !pickedSupporter || reassignStarted}
            >
              {reassignBusy ? 'Reassigning…' : 'Confirm'}
            </button>
          </>
        }
      >
        <p className="mb-3">
          <span className="font-medium text-white">{reassigning?.title}</span>
        </p>

        <div className="mb-3 rounded border border-white/10 bg-white/5 px-3 py-2 text-xs">
          <div className="opacity-70">Current supporter</div>
          <div className="text-white">
            {reassigning?.supporter_email || 'Nobody — this task has not been accepted yet'}
          </div>
        </div>

        {reassignStarted ? (
          /* Mirrors the server's task_in_progress rule. The worklog is keyed to
             the current supporter's email and the GPS pings to their user id, so
             swapping now would file their tracked time under someone else. */
          <p className="mb-3 rounded border border-amber-400/40 bg-amber-400/5 px-3 py-2 text-xs text-amber-200">
            Work has already started on this task ({reassigning?.duration_minutes || 0}m logged), so its
            time and location belong to the current supporter. Remove the task and ask the requester
            to post it again instead.
          </p>
        ) : (
          <>
            <label className="block text-xs uppercase tracking-wide opacity-70 mb-1">
              New supporter
            </label>
            {supporters === null ? (
              <p className="text-xs opacity-60">Loading approved supporters…</p>
            ) : supportersError ? (
              <p className="text-xs text-red-300">{supportersError}</p>
            ) : (
              <select
                className="w-full border rounded px-2 py-1 mb-3 bg-transparent"
                value={pickedSupporter}
                onChange={(e) => setPickedSupporter(e.target.value)}
              >
                <option value="" className="text-black">Choose a supporter…</option>
                {supporters
                  .filter((s) => s.email !== reassigning?.supporter_email)
                  .filter((s) => s.email !== reassigning?.requester_email)
                  .map((s) => (
                    <option key={s.id} value={s.id} className="text-black">
                      {s.name} — {s.email}{s.city ? ` (${s.city})` : ''}
                    </option>
                  ))}
              </select>
            )}
            <p className="text-xs opacity-60">
              {reassigning?.supporter_email
                ? 'The new supporter is assigned and moved into the task chat, the previous one is unassigned and loses chat access, and all three parties are notified.'
                : 'The supporter is assigned directly, skipping the accept flow. They and the requester are notified.'}
            </p>
          </>
        )}
      </Modal>
    </div>
  );
}