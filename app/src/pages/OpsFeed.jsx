// src/pages/OpsFeed.jsx
import { useEffect, useMemo, useState } from 'react'
import { useAuth } from "../auth/AuthContext.jsx";
import { useToast } from '../providers/ToastProvider'
import { opsFetch as fetchJSON, removeTask, reassignTask, listApprovedSupporters,
         forceCompleteTask, adminCancelTask, adjustTaskTime, getTaskMoney, REMOVAL_REASONS } from '../api/ops'
import OpsSupporters from './OpsSupporters.jsx'
import OpsPromoCodes from './OpsPromoCodes.jsx'
import OpsPricing from './OpsPricing.jsx'
import OpsReports from './OpsReports.jsx'
import OpsChats from './OpsChats.jsx'
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

// Where a supporter's transfer stands, in the words their Earnings screen uses.
// `failed` is the only one that needs somebody to do something.
const PAYOUT_STATES = {
  none:       { label: 'No transfer yet', cls: 'border-white/20 opacity-60' },
  pending:    { label: 'Sending',         cls: 'border-white/30' },
  on_its_way: { label: 'On its way',      cls: 'border-amber-300/50 text-amber-200' },
  paid_out:   { label: 'Paid out',        cls: 'border-emerald-300/50 text-emerald-200' },
  failed:     { label: 'Transfer failed', cls: 'border-red-400/60 text-red-300' },
}

const usd = (cents) => (typeof cents === 'number' ? `$${(cents / 100).toFixed(2)}` : '—')

function PayoutBadge({ state }) {
  const s = PAYOUT_STATES[state] || PAYOUT_STATES.none
  return <span className={`inline-block rounded border px-1 ${s.cls}`}>{s.label}</span>
}

const STATUS_FILTERS = ['all','accepted','completed','cancelled','removed'];

const TABS = [
  { key: 'tasks', label: 'Task feed' },
  { key: 'supporters', label: 'Supporter applications' },
  { key: 'promo', label: 'Promo codes' },
  { key: 'pricing', label: 'Pricing' },
  { key: 'reports', label: 'User reports' },
  { key: 'chats', label: 'Chats' },
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
  // Only used when work has started: why the logged time is being voided.
  const [voidReason, setVoidReason] = useState('')

  // Cancel dialog: null when closed, otherwise the row being cancelled. The
  // cause decides what the requester and the supporter are each told.
  const [cancelling, setCancelling] = useState(null)
  const [cancelCause, setCancelCause] = useState('')
  const [cancelNote, setCancelNote] = useState('')
  const [cancelBusy, setCancelBusy] = useState(false)

  // Money dialog: the row it was opened for, and the detail once it arrives.
  // Fetched on open rather than with the feed because the receipt photo is a
  // signed link that expires — it is minted for the person about to look.
  const [moneyRow, setMoneyRow] = useState(null)
  const [money, setMoney] = useState(null)
  const [moneyError, setMoneyError] = useState('')

  const allowed = !!user && ADMIN_EMAILS.has(user.email)

  // Any logged time means a worklog exists, and the reassign endpoint refuses
  // that (task_in_progress) unless the logged time is voided with a reason.
  // Checked here so the dialog asks for the reason up front instead of
  // spending a round-trip on a guaranteed 409.
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
  function openCancel(row) {
    setCancelling(row)
    // A task somebody holds is most often cancelled because they left it.
    setCancelCause(row.supporter_email ? 'supporter_dropped' : '')
    setCancelNote('')
  }

  async function confirmCancel() {
    if (!cancelling) return
    setCancelBusy(true)
    try {
      const res = await adminCancelTask(cancelling.task_id, cancelNote.trim(), cancelCause)
      toast(res?.already_cancelled ? 'Task was already cancelled' : 'Task cancelled — both parties notified, nothing charged')
      setCancelling(null)
      load()
    } catch (e) {
      toast(e.message || 'Cancel failed')
    } finally {
      setCancelBusy(false)
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
    setVoidReason('')
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
      const res = await reassignTask(reassigning.task_id, pickedSupporter, reassignStarted ? voidReason.trim() : '')
      toast(
        res?.voided_sessions
          ? `Handed over to ${res.new_supporter_name || res.new_supporter} — ${res.voided_minutes}m voided, all three parties notified`
          : res?.previous_supporter
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

  // --- 錢：收據與轉帳
  async function openMoney(row) {
    setMoneyRow(row)
    setMoney(null)
    setMoneyError('')
    try {
      setMoney(await getTaskMoney(row.task_id))
    } catch (e) {
      setMoneyError(e.message || 'Could not load the money details')
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
        ) : tab === 'promo' ? (
          <div className="py-4"><OpsPromoCodes /></div>
        ) : tab === 'pricing' ? (
          <div className="py-4"><OpsPricing /></div>
        ) : tab === 'reports' ? (
          <div className="py-4"><OpsReports /></div>
        ) : tab === 'chats' ? (
          <div className="py-4"><OpsChats /></div>
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
                <th className="text-left p-2">Receipt &amp; transfer</th>
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
                    <div className="text-xs">Req: {r.requester_name || '—'}{r.requester_email ? <span className="opacity-60"> · {r.requester_email}</span> : null}</div>
                    <div className="text-xs">Sup: {r.supporter_email ? <>{r.supporter_name}<span className="opacity-60"> · {r.supporter_email}</span></> : '—'}</div>
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
                    <button
                      className="text-left hover:opacity-80"
                      onClick={() => openMoney(r)}
                      title="Open receipt and transfer details"
                    >
                      <div>
                        {typeof r.receipt_cents === 'number'
                          ? <>Receipt {usd(r.receipt_cents)}{r.has_receipt_photo ? ' · photo' : <span className="text-red-300"> · no photo</span>}</>
                          : <span className="opacity-60">No receipt</span>}
                      </div>
                      <div className="mt-1">
                        <PayoutBadge state={r.payout_state} />
                        {r.payout_cents > 0 ? <span className="ml-1 opacity-80">{usd(r.payout_cents)}</span> : null}
                      </div>
                    </button>
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
                      <button className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40" onClick={()=>openCancel(r)}>Cancel</button>
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
                <tr><td className="p-3 text-center opacity-60" colSpan="8">No tasks</td></tr>
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
        open={!!cancelling}
        onClose={() => (cancelBusy ? null : setCancelling(null))}
        title="Cancel task"
        actions={
          <>
            <button
              className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40"
              onClick={() => setCancelling(null)}
              disabled={cancelBusy}
            >
              Keep task
            </button>
            <button
              className="rounded-md border border-red-400/60 px-3 py-1 text-sm text-red-300 hover:border-red-400"
              onClick={confirmCancel}
              disabled={cancelBusy}
            >
              {cancelBusy ? 'Cancelling…' : 'Cancel task'}
            </button>
          </>
        }
      >
        <p className="mb-3">
          <span className="font-medium text-white">{cancelling?.title}</span>
        </p>
        <p className="mb-3 text-xs opacity-70">
          The hold on the requester's card is released and nothing is charged. The system pays the supporter nothing for this task; anything owed is decided case by case and paid by hand.
        </p>

        <label className="block text-xs uppercase tracking-wide opacity-70 mb-1">Why</label>
        <div className="mb-3 space-y-2 text-sm">
          {cancelling?.supporter_email ? (
            <label className="flex items-start gap-2">
              <input
                type="radio"
                className="mt-1"
                checked={cancelCause === 'supporter_dropped'}
                onChange={() => setCancelCause('supporter_dropped')}
              />
              <span>
                The supporter left or can't finish
                <span className="block text-xs opacity-60">
                  Requester: "Your supporter wasn't able to finish… You haven't been charged… Tell us if you'd like
                  us to set it up again with someone else." Supporter: "You've been taken off… Message HO:RA and tell us
                  what happened: how the time you logged is handled depends on the situation."
                </span>
              </span>
            </label>
          ) : null}
          <label className="flex items-start gap-2">
            <input
              type="radio"
              className="mt-1"
              checked={cancelCause === ''}
              onChange={() => setCancelCause('')}
            />
            <span>
              Another reason
              <span className="block text-xs opacity-60">
                Requester: "Your task has been cancelled by the HO:RA team. Reason: (your note). You haven't been
                charged…"
              </span>
            </span>
          </label>
        </div>

        <label className="block text-xs uppercase tracking-wide opacity-70 mb-1">
          {cancelCause === 'supporter_dropped' ? 'Internal note (optional)' : 'Reason shown to the requester (optional)'}
        </label>
        <textarea
          className="w-full border rounded px-2 py-1 bg-transparent"
          rows={2}
          placeholder={
            cancelCause === 'supporter_dropped'
              ? 'Only recorded in the audit log — never shown to anyone'
              : 'e.g. you asked us to by phone'
          }
          value={cancelNote}
          onChange={(e) => setCancelNote(e.target.value)}
        />
      </Modal>

      <Modal
        open={!!moneyRow}
        onClose={() => setMoneyRow(null)}
        title="Receipt & transfer"
        actions={
          <button
            className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40"
            onClick={() => setMoneyRow(null)}
          >
            Close
          </button>
        }
      >
        <p className="mb-3">
          <span className="font-medium text-white">{moneyRow?.title}</span>
          {moneyRow?.supporter_email ? <span className="opacity-60"> · {moneyRow.supporter_name || moneyRow.supporter_email}</span> : null}
        </p>

        {moneyError ? (
          <p className="text-xs text-red-300">{moneyError}</p>
        ) : !money ? (
          <p className="text-xs opacity-60">Loading…</p>
        ) : (
          <div className="space-y-4 text-xs">
            <section>
              <div className="mb-1 uppercase tracking-wide opacity-70">Receipt</div>
              {typeof money.receipt_cents === 'number' ? (
                <>
                  <div className="text-white">
                    {usd(money.receipt_cents)} spent
                    {typeof money.budget_cents === 'number' ? <span className="opacity-60"> · budget {usd(money.budget_cents)}</span> : null}
                  </div>
                  {money.receipt_photo_url ? (
                    <a href={money.receipt_photo_url} target="_blank" rel="noreferrer" className="mt-2 block">
                      <img
                        src={money.receipt_photo_url}
                        alt="Receipt"
                        className="max-h-80 w-auto rounded border border-white/10"
                      />
                      <span className="mt-1 block opacity-60">Opens full size · link expires in 15 minutes</span>
                    </a>
                  ) : (
                    <div className="mt-1 text-red-300">No receipt photo on file.</div>
                  )}
                </>
              ) : (
                <div className="opacity-60">
                  No receipt submitted
                  {typeof money.budget_cents === 'number' && money.budget_cents > 0 ? ` · budget ${usd(money.budget_cents)}` : ''}.
                </div>
              )}
            </section>

            <section>
              <div className="mb-1 uppercase tracking-wide opacity-70">Requester charged</div>
              {money.payments?.length ? money.payments.map((p, i) => (
                <div key={i} className="flex justify-between border-t border-white/10 py-1 first:border-t-0">
                  <span>{p.kind} · {p.status}</span>
                  <span className="text-white">
                    {typeof p.captured_cents === 'number' ? usd(p.captured_cents) : `hold ${usd(p.authorized_cents)}`}
                  </span>
                </div>
              )) : <div className="opacity-60">No charge on this task.</div>}
              {typeof money.settled_total_cents === 'number' ? (
                <div className="mt-1 opacity-70">
                  Settled {usd(money.settled_total_cents)}
                  {typeof money.settled_time_cost_cents === 'number' ? ` · time ${usd(money.settled_time_cost_cents)}` : ''}
                  {money.settled_at ? ` · ${new Date(money.settled_at).toLocaleString()}` : ''}
                </div>
              ) : null}
            </section>

            <section>
              <div className="mb-1 uppercase tracking-wide opacity-70">Transfer to supporter</div>
              {money.payouts?.length ? money.payouts.map((p) => (
                <div key={p.id} className="border-t border-white/10 py-2 first:border-t-0">
                  <div className="flex items-center justify-between">
                    <PayoutBadge state={p.state} />
                    <span className="text-white">{usd(p.amount_cents)}</span>
                  </div>
                  {typeof p.service_cents === 'number' ? (
                    <div className="mt-1 opacity-70">
                      Time {usd(p.service_cents)} − fee {usd(p.fee_cents)} + receipt {usd(p.reimbursement_cents)}
                    </div>
                  ) : null}
                  <div className="mt-1 opacity-60">
                    Sent {new Date(p.created_at).toLocaleString()}
                    {p.bank_paid_at ? ` · in bank ${new Date(p.bank_paid_at).toLocaleString()}` : ''}
                    {p.funding === 'promo_subsidy' ? ' · promo subsidy (platform funds)' : ''}
                  </div>
                  {p.stripe_transfer_id ? <div className="mt-1 font-mono opacity-50">{p.stripe_transfer_id}</div> : null}
                </div>
              )) : <div className="opacity-60">Nothing has been transferred for this task yet.</div>}
            </section>
          </div>
        )}
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
              disabled={reassignBusy || !pickedSupporter || (reassignStarted && !voidReason.trim())}
            >
              {reassignBusy ? 'Reassigning…' : reassignStarted ? 'Void time & hand over' : 'Confirm'}
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
            {reassigning?.supporter_email
              ? `${reassigning.supporter_name || ''} · ${reassigning.supporter_email}`.replace(/^ · /, '')
              : 'Nobody — this task has not been accepted yet'}
          </div>
        </div>

        {reassignStarted ? (
          /* The supporter started this task and is not finishing it. Their
             sessions come off the task (kept in the audit log) so the new
             supporter starts from zero and the requester pays for one person. */
          <div className="mb-3 rounded border border-amber-400/40 bg-amber-400/5 px-3 py-2 text-xs text-amber-200">
            <p>
              Work has already started on this task ({reassigning?.duration_minutes || 0}m logged). Handing it
              over <span className="font-semibold">voids that time</span>: the current supporter is not paid
              for it, the requester is not billed for it, and the new supporter starts from zero on the same
              card hold.
            </p>
            <p className="mt-2 opacity-90">
              If the supporter had a valid reason and should be paid for what they did, pay them separately —
              this action never pays them.
            </p>
            <label className="mt-3 block uppercase tracking-wide opacity-80 mb-1">
              Why (required — audit log only)
            </label>
            <textarea
              className="w-full border border-amber-400/40 rounded px-2 py-1 bg-transparent text-white"
              rows={2}
              placeholder="e.g. left after 10 minutes, not answering messages"
              value={voidReason}
              onChange={(e) => setVoidReason(e.target.value)}
            />
          </div>
        ) : null}

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
              {reassignStarted
                ? 'Requester: "Your supporter wasn\'t able to finish… You only pay for the time the new supporter spends on it." Previous supporter: "You\'ve been taken off… Message HO:RA and tell us what happened."'
                : reassigning?.supporter_email
                ? 'The new supporter is assigned and moved into the task chat, the previous one is unassigned and loses chat access, and all three parties are notified.'
                : 'The supporter is assigned directly, skipping the accept flow. They and the requester are notified.'}
            </p>
      </Modal>
    </div>
  );
}