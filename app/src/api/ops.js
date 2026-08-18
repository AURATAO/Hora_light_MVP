// Shared fetch helper + endpoint wrappers for the /ops/* admin API.
//
// Lifted verbatim (behaviour-wise) out of pages/OpsFeed.jsx so the supporter
// panel and the task feed talk to the backend the same way instead of keeping
// two copies of the same helper. Auth is the Supabase access token as a Bearer
// header — `credentials: 'include'` rides along so a hora_session cookie also
// works, but the Bearer is what dualAuth actually reads for these pages.
import { supabase } from '../lib/supabaseClient'

const API = (import.meta.env?.VITE_API_BASE || '').trim()
if (!API) {
  console.warn('[ops] VITE_API_BASE is empty — requests will fail.')
}

export async function opsFetch(path, init = {}) {
  const { data: { session } } = await supabase.auth.getSession()
  const headers = new Headers(init.headers || {})
  if (session?.access_token) {
    headers.set('Authorization', `Bearer ${session.access_token}`)
  }
  headers.set('Content-Type', 'application/json')

  const url = path.startsWith('http') ? path : `${API}${path}`
  const resp = await fetch(url, { ...init, headers, credentials: 'include' })

  const ct = resp.headers.get('content-type') || ''
  if (!resp.ok) {
    const t = await resp.text().catch(() => '')
    throw new Error(`HTTP ${resp.status} ${path}\n${t.slice(0, 200)}`)
  }
  if (!ct.includes('application/json')) {
    const t = await resp.text().catch(() => '')
    throw new Error(`Expected JSON, got ${ct}\n${t.slice(0, 200)}`)
  }
  return resp.json()
}

// GET /ops/supporter-applications — every profile that has ever applied,
// newest first, each carrying a server-derived supporter_status.
export function listSupporterApplications() {
  return opsFetch('/ops/supporter-applications')
}

export function approveSupporter(email) {
  return opsFetch('/ops/supporter-approve', {
    method: 'POST',
    body: JSON.stringify({ email }),
  })
}

export function rejectSupporter(email) {
  return opsFetch('/ops/supporter-reject', {
    method: 'POST',
    body: JSON.stringify({ email }),
  })
}

// POST /admin/tasks/:id/remove — platform takedown of a task that breaks beta
// scope. Distinct from /ops/cancel, which records a requester cancellation.
// The backend re-checks the admin allowlist; this wrapper only shapes the call.
export function removeTask(taskId, reason, note) {
  return opsFetch(`/admin/tasks/${taskId}/remove`, {
    method: 'POST',
    body: JSON.stringify({ reason, note }),
  })
}

// Reason slugs the endpoint accepts, with the labels the ops panel shows.
// Mirrors removalReasons in server/admin_tasks.go.
export const REMOVAL_REASONS = [
  { value: 'out_of_scope_private_residence', label: 'Out of scope — private residence' },
  { value: 'out_of_scope_other', label: 'Out of scope — other' },
  { value: 'inappropriate', label: 'Inappropriate content' },
  { value: 'other', label: 'Other' },
]
