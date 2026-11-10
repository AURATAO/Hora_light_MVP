// Pricing — the ops-area counterpart to /admin/pricing (server/pricing.go).
// Rendered as a tab inside OpsFeed's page shell, so it inherits that page's
// admin gate; the backend re-checks the allowlist on every call regardless.
//
// A change is a new schedule, never an edit of the old one: every task stores
// the prices it was posted at, so the old row stays true for the tasks posted
// under it. New tasks get the new prices within about a minute.
import { useEffect, useState } from 'react'
import { useToast } from '../providers/ToastProvider'
import { getPricing, setPricing } from '../api/ops'
import { formatCents } from '../lib/formatCents'

const FIELDS = [
  { key: 'base_fee_default_cents', label: 'Base fee (first 15 min)', unit: '$' },
  { key: 'per_minute_rate_cents', label: 'Per minute after that', unit: '$' },
  { key: 'base_fee_companionship_cents', label: 'Companionship base fee', unit: '$' },
  { key: 'surge_rate_cents_per_min', label: 'Evening per minute (9 PM–8 AM)', unit: '$' },
  { key: 'platform_fee_bps', label: 'Platform fee', unit: '%' },
]

// The form holds what a person types: dollars and percent. Cents and basis
// points only at the boundary.
function toForm(s) {
  const f = {}
  for (const { key, unit } of FIELDS) {
    const v = s?.[key]
    f[key] = v == null ? '' : unit === '%' ? String(v / 100) : (v / 100).toFixed(2)
  }
  f.note = ''
  return f
}

function fromForm(f) {
  const out = { note: f.note.trim() }
  for (const { key, unit } of FIELDS) {
    const n = Number(String(f[key]).replace(/[^0-9.]/g, ''))
    out[key] = Number.isFinite(n) ? Math.round(n * 100) : NaN
  }
  return out
}

function fee(bps) {
  return bps % 100 === 0 ? `${bps / 100}%` : `${(bps / 100).toFixed(2).replace(/0+$/, '').replace(/\.$/, '')}%`
}

function formatWhen(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString()
}

// What a supporter and the platform get from a typical 45-minute task, so the
// person changing a number sees what it does before saving.
function example(s) {
  if (!s) return null
  const service = s.base_fee_default_cents + 30 * s.per_minute_rate_cents
  const platform = Math.floor((service * s.platform_fee_bps + 5000) / 10000)
  return { service, platform, supporter: service - platform }
}

export default function OpsPricing() {
  const toast = useToast()
  const [data, setData] = useState(null)
  const [form, setForm] = useState(toForm(null))
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const d = await getPricing()
      setData(d)
      setForm(toForm(d?.live))
    } catch (err) {
      console.error('[ops/pricing] error', err)
      toast(err.message || "Couldn't load pricing")
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  async function save(e) {
    e.preventDefault()
    const body = fromForm(form)
    if (FIELDS.some(({ key }) => !Number.isFinite(body[key]))) {
      toast('Every price needs a number.')
      return
    }
    if (!body.note) {
      toast('Say why the prices are changing.')
      return
    }
    const live = data?.live
    const lines = FIELDS.map(({ key, label, unit }) => {
      const now = unit === '%' ? fee(live?.[key] ?? 0) : formatCents(live?.[key] ?? 0)
      const next = unit === '%' ? fee(body[key]) : formatCents(body[key])
      return now === next ? null : `${label}: ${now} → ${next}`
    }).filter(Boolean)
    if (lines.length === 0) {
      toast('Nothing changed.')
      return
    }
    if (!confirm(`New prices for tasks posted from now on:\n\n${lines.join('\n')}\n\nTasks already posted keep their prices.`)) return
    setSaving(true)
    try {
      await setPricing(body)
      toast('New prices are live for new tasks.', 'success')
      await load()
    } catch (err) {
      toast(err.message || "Couldn't save the prices")
    } finally {
      setSaving(false)
    }
  }

  const field = (key) => (e) => setForm(f => ({ ...f, [key]: e.target.value }))
  const preview = example(Object.values(fromForm(form)).some(v => Number.isNaN(v)) ? null : fromForm(form))

  return (
    <div className="space-y-6">
      <form onSubmit={save} className="border rounded p-3 space-y-3">
        <h2 className="text-lg font-semibold">Prices</h2>
        <p className="text-xs opacity-70">
          Applies to tasks posted from now on, within about a minute. Every task keeps the prices it was posted
          at, so a change never re-prices a task in progress. Update the website&apos;s price copy yourself.
        </p>
        <div className="grid grid-cols-1 sm:grid-cols-5 gap-2">
          {FIELDS.map(({ key, label, unit }) => (
            <label key={key} className="text-xs">
              {label} ({unit})
              <input className="border rounded px-2 py-1 w-full text-sm" type="number" min="0" step="0.01"
                value={form[key]} onChange={field(key)} required disabled={loading} />
            </label>
          ))}
        </div>
        <input className="border rounded px-2 py-1 w-full text-sm" placeholder="Why (required, e.g. raise margin)"
          value={form.note} onChange={field('note')} maxLength={280} disabled={loading} />
        {preview ? (
          <p className="text-xs opacity-70">
            A 45-minute task: {formatCents(preview.service)} service → supporter {formatCents(preview.supporter)},
            HO:RA {formatCents(preview.platform)} (before Stripe fees).
          </p>
        ) : null}
        <button className="border rounded px-3 py-1" type="submit" disabled={saving || loading}>
          {saving ? 'Saving…' : 'Save new prices'}
        </button>
      </form>

      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">History</h2>
        <button className="border rounded px-3 py-1 text-sm" onClick={load} disabled={loading}>
          {loading ? 'Loading…' : 'Refresh'}
        </button>
      </div>
      <div className="border rounded overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-black/5">
            <tr>
              <th className="text-left p-2">When</th>
              <th className="text-left p-2">Base</th>
              <th className="text-left p-2">/min</th>
              <th className="text-left p-2">Companionship</th>
              <th className="text-left p-2">Evening /min</th>
              <th className="text-left p-2">Fee</th>
              <th className="text-left p-2">By</th>
              <th className="text-left p-2">Why</th>
            </tr>
          </thead>
          <tbody>
            {(data?.history ?? []).map((r, i) => (
              <tr key={r.id} className="border-t">
                <td className="p-2 whitespace-nowrap">{formatWhen(r.created_at)}{i === 0 ? ' · live' : ''}</td>
                <td className="p-2">{formatCents(r.base_fee_default_cents)}</td>
                <td className="p-2">{formatCents(r.per_minute_rate_cents)}</td>
                <td className="p-2">{formatCents(r.base_fee_companionship_cents)}</td>
                <td className="p-2">{formatCents(r.surge_rate_cents_per_min)}</td>
                <td className="p-2">{fee(r.platform_fee_bps)}</td>
                <td className="p-2">{r.created_by || '—'}</td>
                <td className="p-2">{r.note || '—'}</td>
              </tr>
            ))}
            {!loading && (data?.history ?? []).length === 0 ? (
              <tr><td className="p-2 opacity-60" colSpan={8}>No schedule yet: the server is using its compiled prices.</td></tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </div>
  )
}
