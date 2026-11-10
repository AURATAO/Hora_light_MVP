// What HO:RA does not do (server/task_scope.go, D-26). A post, an edit or the
// AI box can be refused with
//   422 { error: "out_of_scope", category, message }  — the category's own
//       reply, which says what HO:RA can do instead; or
//   503 { error: "scope_unchecked", message }          — the check could not
//       run; nothing was posted, try again.
// Either way the server's sentence is what the person should read, beside
// the form — not a generic "couldn't understand that".

const CODES = new Set(['out_of_scope', 'scope_unchecked'])

/** { message, category, retry } for a scope refusal, null for anything else. */
export function scopeRefusal(err) {
  const body = err && typeof err.body === 'object' && err.body ? err.body : {}
  const code = err?.code || body.error
  if (!CODES.has(code)) return null
  const message = typeof body.message === 'string' && body.message.trim() ? body.message.trim() : err?.message || ''
  return { message, category: typeof body.category === 'string' ? body.category : null, retry: code === 'scope_unchecked' }
}
