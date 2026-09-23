// Promo codes — the ops-area counterpart to /admin/promo-codes (server/promo.go).
// Rendered as a tab inside OpsFeed's page shell, so it inherits that page's
// admin gate; the backend re-checks the allowlist on every call regardless.
//
// Minimal on purpose: a code is created, it is redeemed, it is deactivated.
// Nothing here edits a live code — a task posted under one carries a snapshot
// of its amount, so "editing" would only ever mean lying about history.
import { useEffect, useState } from 'react'
import { useToast } from '../providers/ToastProvider'
import { createPromoCode, deactivatePromoCode, listPromoCodes } from '../api/ops'
import { formatCents } from '../lib/formatCents'

function formatWhen(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

// A datetime-local value, or nothing. The input is local time; the API wants
// RFC 3339, which toISOString gives in UTC.
function toIso(local) {
  if (!local) return undefined
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

const EMPTY = { code: '', amount: '', validFrom: '', validUntil: '', maxRedemptions: '', firstTaskOnly: true, note: '' }

export default function OpsPromoCodes() {
  const toast = useToast()
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [form, setForm] = useState(EMPTY)
  const [saving, setSaving] = useState(false)
  const [busyId, setBusyId] = useState(null)

  async function load() {
    setLoading(true)
    try {
      const data = await listPromoCodes()
      setRows(Array.isArray(data) ? data : [])
    } catch (err) {
      console.error('[ops/promo-codes] error', err)
      toast(err.message || "Couldn't load promo codes")
      setRows([])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  async function create(e) {
    e.preventDefault()
    const amountCents = Math.round(Number(form.amount) * 100)
    if (!form.code.trim() || !Number.isFinite(amountCents) || amountCents <= 0) {
      toast('A code and a discount above $0 are required.')
      return
    }
    setSaving(true)
    try {
      await createPromoCode({
        code: form.code.trim(),
        amount_cents: amountCents,
        valid_from: toIso(form.validFrom),
        valid_until: toIso(form.validUntil),
        max_redemptions: form.maxRedemptions ? Number(form.maxRedemptions) : undefined,
        first_task_only: form.firstTaskOnly,
        note: form.note.trim(),
      })
      toast(`${form.code.trim()} created.`, 'success')
      setForm(EMPTY)
      await load()
    } catch (err) {
      toast(err.message || "Couldn't create that code")
    } finally {
      setSaving(false)
    }
  }

  async function deactivate(row) {
    if (!confirm(`Deactivate ${row.code}? Tasks already posted under it keep their discount.`)) return
    setBusyId(row.id)
    try {
      await deactivatePromoCode(row.id)
      toast(`${row.code} deactivated.`, 'success')
      await load()
    } catch (err) {
      toast(err.message || `Couldn't deactivate ${row.code}`)
    } finally {
      setBusyId(null)
    }
  }

  const field = (key) => (e) => setForm(f => ({ ...f, [key]: e.target.type === 'checkbox' ? e.target.checked : e.target.value }))

  return (
    <div className="space-y-6">
      <form onSubmit={create} className="border rounded p-3 space-y-2">
        <h2 className="text-lg font-semibold">New promo code</h2>
        <p className="text-xs opacity-70">
          A fixed amount off what the requester pays, at the hold and at settlement, never below $0.
          The supporter is paid in full — the platform absorbs the difference.
        </p>
        <div className="grid grid-cols-1 sm:grid-cols-6 gap-2">
          <input className="border rounded px-2 py-1 sm:col-span-2" placeholder="Code (e.g. WELCOME10)"
            value={form.code} onChange={field('code')} required />
          <input className="border rounded px-2 py-1" placeholder="Amount ($)" type="number" min="0.01" step="0.01"
            value={form.amount} onChange={field('amount')} required />
          <input className="border rounded px-2 py-1" placeholder="Max redemptions" type="number" min="1" step="1"
            value={form.maxRedemptions} onChange={field('maxRedemptions')} />
          <label className="text-xs sm:col-span-1">Valid from
            <input className="border rounded px-2 py-1 w-full" type="datetime-local" value={form.validFrom} onChange={field('validFrom')} />
          </label>
          <label className="text-xs sm:col-span-1">Valid until
            <input className="border rounded px-2 py-1 w-full" type="datetime-local" value={form.validUntil} onChange={field('validUntil')} />
          </label>
          <input className="border rounded px-2 py-1 sm:col-span-4" placeholder="Note for ops (what it's for)"
            value={form.note} onChange={field('note')} />
          <label className="flex items-center gap-2 text-sm sm:col-span-1">
            <input type="checkbox" checked={form.firstTaskOnly} onChange={field('firstTaskOnly')} />
            First task only
          </label>
          <button className="border rounded px-3 py-1 sm:col-span-1" type="submit" disabled={saving}>
            {saving ? 'Creating…' : 'Create'}
          </button>
        </div>
      </form>

      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">Codes {rows.length ? `(${rows.length})` : ''}</h2>
        <button className="border rounded px-3 py-1 text-sm" onClick={load} disabled={loading}>
          {loading ? 'Loading…' : 'Refresh'}
        </button>
      </div>

      <div className="border rounded overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-black/5">
            <tr>
              <th className="text-left p-2">Code</th>
              <th className="text-left p-2">Amount</th>
              <th className="text-left p-2">Redeemed</th>
              <th className="text-left p-2">Window</th>
              <th className="text-left p-2">Rules</th>
              <th className="text-left p-2">Status</th>
              <th className="text-left p-2"></th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && !loading ? (
              <tr><td className="p-2 opacity-70" colSpan={7}>No promo codes yet.</td></tr>
            ) : null}
            {rows.map(r => (
              <tr key={r.id} className="border-t align-top">
                <td className="p-2">
                  <div className="font-mono">{r.code}</div>
                  {r.note ? <div className="text-xs opacity-70">{r.note}</div> : null}
                </td>
                <td className="p-2">{formatCents(r.amount_cents)}</td>
                <td className="p-2">
                  {r.redemptions}{r.max_redemptions ? ` / ${r.max_redemptions}` : ''}
                </td>
                <td className="p-2 text-xs">
                  {r.valid_from ? `from ${formatWhen(r.valid_from)}` : ''}
                  {r.valid_from && r.valid_until ? <br /> : null}
                  {r.valid_until ? `until ${formatWhen(r.valid_until)}` : ''}
                  {!r.valid_from && !r.valid_until ? 'always' : ''}
                </td>
                <td className="p-2 text-xs">{r.first_task_only ? 'First task only' : 'Any task'}</td>
                <td className="p-2">
                  {r.active ? 'Active' : `Deactivated ${formatWhen(r.deactivated_at)}`}
                </td>
                <td className="p-2">
                  {r.active ? (
                    <button className="border rounded px-2 py-1 text-xs" onClick={() => deactivate(r)} disabled={busyId === r.id}>
                      {busyId === r.id ? '…' : 'Deactivate'}
                    </button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
