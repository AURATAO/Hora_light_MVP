import { useEffect, useState } from 'react'
import { api } from '../api/client'
import AvatarUploader from './AvatarUploader'
import { useAuth } from '../auth/AuthContext'

function ProfileCard() {
  const [profile, setProfile] = useState(null)
  const [draft, setDraft] = useState(null)
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)
  const { user, loading: authLoading } = useAuth()


  useEffect(() => {
  if (authLoading) return;          // 還在等 Supabase 回答，就先不動
  if (!user) {                      // 沒登入：結束 loading，顯示適當文案
    setLoading(false);
    setProfile(null);
    setDraft(null);
    return;
  }

  let alive = true;
  setLoading(true);
  (async () => {
    try {
      const p = await api('/profile');
      if (!alive) return;
      setProfile(p);
      setDraft(p);
    } catch (e) {
      // 可選：顯示錯誤
      console.error('[profile] load error:', e);
    } finally {
      if (alive) setLoading(false);
    }
  })();
  return () => { alive = false; };
}, [authLoading, user]);

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

   async function handleAvatarUploaded(url) {
    setSaving(true)
    try {
      const next = await api('/profile', {
        method: 'PATCH',
        body: { avatar_url: url },
      })
      setProfile(next)
      setDraft(next)
    } catch (e) {
      alert(e.message || 'Failed to update avatar')
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return <div className="border border-white/20 rounded-lg p-3 bg-white/5">Loading profile…</div>
  }

   const currentAvatar = (editing ? draft?.avatar_url : profile?.avatar_url) || ''

 return (
  <div className="border border-white/20 rounded-lg p-3 bg-white/5">
    {/* Header */}
    <div className="flex flex-col sm:flex-row sm:items-start gap-3 sm:gap-4">
      <div className="shrink-0">
        <AvatarUploader
          value={(editing ? draft?.avatar_url : profile?.avatar_url) || ''}
          onChange={async (url) => {
            setProfile(p => ({ ...(p || {}), avatar_url: url }))
            setDraft(d => ({ ...(d || {}), avatar_url: url }))
            try {
              await api('/profile', { method: 'PATCH', body: { avatar_url: url } })
            } catch (e) {
              alert(e.message || 'Failed to save avatar')
            }
          }}
        />
      </div>

      <div className="flex-1 min-w-0">
        {!editing ? (
          <>
            {/* 顯示模式：手機直排、桌機橫排 */}
            <div className="flex flex-col sm:flex-row sm:items-center gap-1 sm:gap-2">
              <div className="text-lg font-semibold truncate">{profile?.name || '—'}</div>
              {/* 手機允許換行；桌機可截斷 */}
              <span className="text-xs text-white/70 break-words sm:truncate">
                {profile?.email}
              </span>

              {/* Edit：手機獨立一行滿寬；桌機靠右 */}
              <div className="sm:ml-auto">
                <button
                  onClick={startEdit}
                  className="w-full sm:w-auto rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
                  title="Edit"
                >
                  ✎ Edit
                </button>
              </div>
            </div>

            <div className="mt-2 grid gap-2 grid-cols-1 sm:grid-cols-3">
              <div className="text-sm text-white/80 min-w-0">
                <span className="opacity-70">Phone:</span> {profile?.phone || '—'}
              </div>
              <div className="text-sm text-white/80 min-w-0">
                <span className="opacity-70">City:</span> {profile?.city || '—'}
              </div>
            </div>

            {/* 文字可換行避免撐爆 */}
            <div className="mt-2 text-sm text-white/80 whitespace-pre-wrap break-words">
              {profile?.bio || '—'}
            </div>
          </>
        ) : (
          <>
            {/* 編輯模式：表單在手機直排；桌機橫排不擠 */}
            <div className="grid gap-2">
              <div className="flex flex-col sm:flex-row sm:items-center gap-2">
                <input
                  className="bg-transparent outline-none border border-white/20 focus:border-white/40 rounded px-2 py-1 flex-1 min-w-0"
                  value={draft?.name || ''}
                  onChange={(e) => setDraft(d => ({ ...d, name: e.target.value }))}
                  placeholder="Your name"
                />
                <span className="text-xs text-white/60 break-words sm:truncate">
                  {profile?.email}
                </span>
              </div>

              <div className="grid gap-2 grid-cols-1 sm:grid-cols-3">
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

            {/* Actions：手機直排、滿寬；桌機橫排 */}
            <div className="mt-3 flex flex-col sm:flex-row gap-2">
              <button
                onClick={saveEdit}
                disabled={saving}
                className="w-full sm:w-auto rounded-md px-3 py-1.5 bg-white text-black disabled:opacity-50"
              >
                {saving ? 'Saving…' : 'Save'}
              </button>
              <button
                onClick={cancelEdit}
                className="w-full sm:w-auto rounded-md px-3 py-1.5 border border-white/20 hover:border-white/40"
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