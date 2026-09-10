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
    // These endpoints answer a failure with { error, message } and the message
    // is written for the admin reading it ("Work has already started on this
    // task…"). Surface it as the Error's message so a toast shows the
    // explanation rather than "HTTP 409 /admin/tasks/…" plus raw JSON. The
    // code and status ride along for callers that branch on them.
    let parsed = null
    try { parsed = JSON.parse(t) } catch { /* not JSON — fall through */ }
    const err = new Error(parsed?.message || parsed?.error || `HTTP ${resp.status} ${path}\n${t.slice(0, 200)}`)
    err.status = resp.status
    err.code = parsed?.error
    err.body = parsed
    throw err
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

// POST /admin/tasks/:id/force-complete — close a task that is finished in
// reality but stuck open (no clock-out, or no completion photo). Skips the
// photo requirement completeTask enforces, which is the point of it.
export function forceCompleteTask(taskId) {
  return opsFetch(`/admin/tasks/${taskId}/force-complete`, { method: 'POST', body: '{}' })
}

// POST /admin/tasks/:id/cancel — cancel on the requester's behalf. Records
// 'cancelled', the same status the requester's own cancellation produces;
// removeTask is the platform takedown and is deliberately a different status.
export function adminCancelTask(taskId, reason) {
  return opsFetch(`/admin/tasks/${taskId}/cancel`, {
    method: 'POST',
    body: JSON.stringify({ reason }),
  })
}

// POST /admin/tasks/:id/adjust-time — correct the most recent work session by
// +/- minutes.
export function adjustTaskTime(taskId, delta) {
  return opsFetch(`/admin/tasks/${taskId}/adjust-time`, {
    method: 'POST',
    body: JSON.stringify({ delta }),
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

// GET /admin/supporters — approved supporters only, the set the reassign
// endpoint will accept. Deliberately not listSupporterApplications(): that one
// lists everyone who ever *applied*, which includes rejected and pending
// people and misses anyone approved without an application on file.
export function listApprovedSupporters() {
  return opsFetch('/admin/supporters')
}

// POST /admin/tasks/:id/reassign — rotate the supporter on an open task, or
// assign one directly to a task nobody has accepted. The backend re-checks the
// admin allowlist and every eligibility rule; this wrapper only shapes the call.
export function reassignTask(taskId, supporterId) {
  return opsFetch(`/admin/tasks/${taskId}/reassign`, {
    method: 'POST',
    body: JSON.stringify({ supporter_id: supporterId }),
  })
}
