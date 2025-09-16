import { useEffect, useState } from 'react'
import { api } from '../api/client'

function ProfileCard() {
  const [profile, setProfile] = useState(null)
  const [draft, setDraft] = useState(null)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    (async () => {
      try {
        const p = await api('/profile')
        setProfile(p)
        setDraft(p)
      } finally {
        setLoading(false)
      }
    })()
  }, [])

  function startEdit() {
    setDraft(profile)
    setEditing(true)
  }
  function cancelEdit() {
    setDraft(profile)
    setEditing(false)
  }
  async function saveEdit() {
    setSaving(true)
    try {
      const next = await api('/profile', {
        method: 'PATCH',
        body: {
          name: draft?.name ?? '',
          phone: draft?.phone ?? '',
          city: draft?.city ?? '',
          avatar_url: draft?.avatar_url ?? '',
          bio: draft?.bio ?? '',
        },
      })
      setProfile(next)
      setDraft(next)
      setEditing(false)
    } catch (e) {
      alert(e.message || 'Failed to save profile')
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return <div className="border border-white/20 rounded-lg p-3 bg-white/5">Loading profile…</div>
  }

  return (
    <div className="border border-white/20 rounded-lg p-3 bg-white/5">
      {/* Header */}
      <div className="flex items-start gap-4">
        <img
          src={(editing ? draft?.avatar_url : profile?.avatar_url) || 'https://placehold.co/80x80?text=Avatar'}
          alt=""
          className="w-16 h-16 rounded-full border border-white/20 object-cover"
        />
        <div className="flex-1">
          {!editing ? (
            <>
              <div className="flex items-center gap-2">
                <div className="text-lg font-semibold">{profile?.name || '—'}</div>
                <span className="text-xs text-white/60">{profile?.email}</span>
                <button
                  onClick={startEdit}
                  className="ml-auto rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
                  title="Edit"
                >
                  ✎ Edit
                </button>
              </div>
              <div className="mt-2 grid gap-2 sm:grid-cols-3">
                <div className="text-sm text-white/80"><span className="opacity-70">Phone:</span> {profile?.phone || '—'}</div>
                <div className="text-sm text-white/80"><span className="opacity-70">City:</span> {profile?.city || '—'}</div>
                <div className="text-sm text-white/80"><span className="opacity-70">Avatar URL:</span> {profile?.avatar_url || '—'}</div>
              </div>
              <div className="mt-2 text-sm text-white/80 whitespace-pre-wrap">{profile?.bio || '—'}</div>
            </>
          ) : (
            <>
              {/* Editing form (細線出現) */}
              <div className="grid gap-2">
                <div className="flex items-center gap-2">
                  <input
                    className="bg-transparent outline-none border border-white/20 focus:border-white/40 rounded px-2 py-1 flex-1"
                    value={draft?.name || ''}
                    onChange={(e) => setDraft(d => ({ ...d, name: e.target.value }))}
                    placeholder="Your name"
                  />
                  <span className="text-xs text-white/60">{profile?.email}</span>
                </div>
                <div className="grid gap-2 sm:grid-cols-3">
                  <label className="text-sm grid gap-1">
                    <span className="text-white/80">Phone</span>
                    <input
                      className="bg-transparent outline-none border border-white/20 focus:border-white/40 rounded px-2 py-1"
                      value={draft?.phone || ''}
                      onChange={(e) => setDraft(d => ({ ...d, phone: e.target.value }))}
                    />
                  </label>
                  <label className="text-sm grid gap-1">
                    <span className="text-white/80">City</span>
                    <input
                      className="bg-transparent outline-none border border-white/20 focus:border-white/40 rounded px-2 py-1"
                      value={draft?.city || ''}
                      onChange={(e) => setDraft(d => ({ ...d, city: e.target.value }))}
                    />
                  </label>
                  <label className="text-sm grid gap-1">
                    <span className="text-white/80">Avatar URL</span>
                    <input
                      className="bg-transparent outline-none border border-white/20 focus:border-white/40 rounded px-2 py-1"
                      value={draft?.avatar_url || ''}
                      onChange={(e) => setDraft(d => ({ ...d, avatar_url: e.target.value }))}
                    />
                  </label>
                </div>
                <label className="text-sm grid gap-1">
                  <span className="text-white/80">Bio</span>
                  <textarea
                    rows={3}
                    className="bg-transparent outline-none border border-white/20 focus:border-white/40 rounded px-2 py-1"
                    value={draft?.bio || ''}
                    onChange={(e) => setDraft(d => ({ ...d, bio: e.target.value }))}
                    placeholder="Short bio"
                  />
                </label>
              </div>

              {/* Actions */}
              <div className="mt-3 flex gap-2">
                <button
                  onClick={saveEdit}
                  disabled={saving}
                  className="rounded-md px-3 py-1.5 bg-white text-black disabled:opacity-50"
                >
                  {saving ? 'Saving…' : 'Save'}
                </button>
                <button
                  onClick={cancelEdit}
                  className="rounded-md px-3 py-1.5 border border-white/20 hover:border-white/40"
                >
                  Cancel
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

export { ProfileCard }