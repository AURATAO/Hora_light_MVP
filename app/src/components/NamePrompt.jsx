import { useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'

/**
 * "What should we call you?" — asked ONCE of an account that has no display
 * name, and skippable.
 *
 * Until build 12 every profile was seeded with the email's local part, so a
 * supporter was announced to requesters as "taoaura.lavoro is on the way".
 * The seed is gone (migration 20260922075623 blanked it; the server never
 * writes it now), which leaves existing accounts nameless until asked. New
 * accounts are asked by the profile form itself, where the name is required;
 * this is for everyone who signed up before that.
 *
 * Gated on /auth/me's `display_name_set`, which the server derives from the
 * one place a name lives (server/names.go). Skipping is remembered per
 * browser and account, so nobody is nagged; the Edit Profile form is always
 * there. Mounted by ProtectLayout beside BetaModal and never on /profile,
 * where the same field is on screen already.
 */
const skipKey = (uid) => `hora_name_prompt_skipped:${uid}`

function readSkipped(uid) {
  try { return localStorage.getItem(skipKey(uid)) === '1' } catch { return false }
}
function writeSkipped(uid) {
  try { localStorage.setItem(skipKey(uid), '1') } catch { /* private mode */ }
}

export default function NamePrompt() {
  const { user, setUser } = useAuth()
  const loc = useLocation()
  const [name, setName] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  // Closed for the rest of this page load once answered or skipped, so a
  // route change does not re-open it before /auth/me is re-read.
  const [closed, setClosed] = useState(false)

  useEffect(() => { setError('') }, [name])

  // Only when the server has SAID there is no name. AuthContext briefly holds
  // the Supabase session user (no display_name_set at all) before /auth/me
  // answers, and "unknown" must not flash the prompt at people who have one.
  if (!user?.id || user.display_name_set !== false || closed) return null
  if (loc.pathname === '/profile') return null
  if (readSkipped(user.id)) return null

  async function save(e) {
    e?.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) { setError('Type a name, or skip for now.'); return }
    setSaving(true)
    try {
      await api('/profile', { method: 'PATCH', body: { name: trimmed } })
      setUser({ ...user, name: trimmed, display_name_set: true })
      setClosed(true)
    } catch (err) {
      setError(err?.message || 'Could not save your name. Try again.')
    } finally {
      setSaving(false)
    }
  }

  function skip() {
    writeSkipped(user.id)
    setClosed(true)
  }

  return (
    <div className="fixed inset-0 z-9998 flex items-end sm:items-center justify-center bg-black/70 backdrop-blur-sm px-4 pb-4 sm:pb-0">
      <form
        onSubmit={save}
        className="w-full max-w-md rounded-2xl border border-white/10 bg-surface shadow-2xl p-6 sm:p-8 space-y-4"
      >
        <h1 className="font-heading text-2xl text-white">What should we call you?</h1>
        <p className="text-sm text-white/60 font-secondary">
          This is the name supporters and requesters see on your tasks, in chat and in notifications.
        </p>
        <input
          type="text"
          autoFocus
          value={name}
          onChange={e => setName(e.target.value)}
          placeholder="Your name"
          maxLength={80}
          className="w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2.5 text-base text-white
                     placeholder-white/30 outline-none focus:border-secondary/50 transition-colors"
        />
        {error && <p className="text-xs text-red-400">{error}</p>}
        <div className="flex items-center justify-between gap-3 pt-1">
          <button
            type="button"
            onClick={skip}
            disabled={saving}
            className="text-sm text-white/50 hover:text-white/80 transition-colors disabled:opacity-40"
          >
            Skip for now
          </button>
          <button
            type="submit"
            disabled={saving}
            className="rounded-xl bg-brand px-5 py-2.5 text-sm font-secondary font-semibold text-white
                       hover:brightness-110 transition-all disabled:opacity-40"
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </form>
    </div>
  )
}
