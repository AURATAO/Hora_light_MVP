// src/pages/OpsFeed.jsx
import { useEffect, useMemo, useState } from 'react'
import { useAuth } from "../auth/AuthContext.jsx";
import { supabase } from '../lib/supabaseClient'

// ✅ 從環境變數讀 API Base（例如 https://core.horaapp.co）
const API = (import.meta.env?.VITE_API_BASE || '').trim();
if (!API) {
  // 不阻擋執行，但提醒你記得在 .env 設定 VITE_API_BASE
  console.warn('[OpsFeed] VITE_API_BASE is empty — requests will fail.');
}

const ADMIN_EMAILS = new Set([
  'auratao.model@gmail.com',
  'liang.you@horaapp.co',
  'liang.you@arcodiax.com',
  'rollod4@gmail.com',
  'daniele@arcodiax.com'
]);

const STATUS_FILTERS = ['all','accepted','completed','cancelled'];

// ---- 共用：抓 JSON，順便幫忙把不是 JSON 的 404/HTML 報錯印出前 200 字 ----

async function fetchJSON(path, init={}) {
  // 取目前 session 的 access token
  const { data: { session } } = await supabase.auth.getSession();
  const headers = new Headers(init.headers || {});
  if (session?.access_token) {
    headers.set('Authorization', `Bearer ${session.access_token}`);
  }
  headers.set('Content-Type', 'application/json');

  const resp = await fetch(`${API}${path}`, {
    ...init,
    headers,
    credentials: 'include', // 留著也無妨，但依賴 Bearer
  });

  const ct = resp.headers.get('content-type') || '';
  if (!resp.ok) {
    const t = await resp.text().catch(()=> '')
    throw new Error(`HTTP ${resp.status} ${path}\n${t.slice(0,200)}`)
  }
  if (!ct.includes('application/json')) {
    const t = await resp.text().catch(()=> '')
    throw new Error(`Expected JSON, got ${ct}\n${t.slice(0,200)}`)
  }
  return resp.json();
}

export default function OpsFeed() {
  const { user } = useAuth()
  const [rows, setRows] = useState([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('all')
  const [q, setQ] = useState('')

  const allowed = !!user && new Set([
    'auratao.model@gmail.com',
    'liang.you@horaapp.co',
    'liang.you@arcodiax.com',
    'rollod4@gmail.com',
    'daniele@arcodiax.com'
  ]).has(user.email)

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
      if (!API) return;
      try {
        const url = `${API}/auth/me`;
        console.log('[auth] GET', url);
        const me = await fetchJSON(url);
        console.log('auth/me ->', me, '(API=', API, ')');
      } catch (e) {
        console.log('auth/me fail', e, '(API=', API, ')');
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
      const url = `${API}/ops/force-complete`;
      await fetchJSON(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ task_id: id }),
      });
      load();
    } catch (e) {
      alert(e.message);
    }
  }

  // --- 動作：取消
  async function cancelTask(id) {
    const reason = prompt('Cancel reason (optional):') || '';
    try {
      const url = `${API}/ops/cancel`;
      await fetchJSON(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ task_id: id, reason }),
      });
      load();
    } catch (e) {
      alert(e.message);
    }
  }

  // --- 動作：時間調整（+/- 分鐘）
  async function adjustTime(id) {
    const raw = prompt('Add minutes (+/-):', '5');
    if (raw == null) return;
    const delta = parseInt(raw, 10);
    if (!Number.isFinite(delta) || delta === 0) return;
    try {
      const url = `${API}/ops/adjust-time`;
      await fetchJSON(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ task_id: id, delta }),
      });
      load();
    } catch (e) {
      alert(e.message);
    }
  }

  if (!user) return <div className="p-6">Please sign in.</div>;
  if (!allowed) return <div className="p-6">You are not authorized to view Ops.</div>;

  return (
    <div className="min-h-screen w-full flex justify-center bg-linear-to-br from-primary to-primary/30 text-accent">
      <div className="p-4 max-w-7xl w-full">
        <h1 className="text-xl font-semibold">Ops Feed</h1>

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
                    {typeof r.prepay_amount === 'number' ? r.prepay_amount.toFixed(2) : '—'}
                  </td>
                  <td className="p-2 text-xs">
                    {r.running_minutes > 0 && <span className="inline-block border rounded px-1 mr-1">running</span>}
                    {r.status === 'cancelled' && <span className="inline-block border rounded px-1">cancelled</span>}
                  </td>
                  <td className="p-2">
                    <div className="flex gap-1">
                      <button className="border rounded px-2 py-0.5" onClick={()=>adjustTime(r.task_id)}>Adjust</button>
                      <button className="border rounded px-2 py-0.5" onClick={()=>forceComplete(r.task_id)}>Force</button>
                      <button className="border rounded px-2 py-0.5" onClick={()=>cancelTask(r.task_id)}>Cancel</button>
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
      </div>
    </div>
  );
}