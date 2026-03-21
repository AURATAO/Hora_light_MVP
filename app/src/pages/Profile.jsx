import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import AvatarUploader from '../components/AvatarUploader'
import { useToast } from '../providers/ToastProvider'

export default function Profile() {
  const navigate = useNavigate()
  const toast = useToast()

  const [phone, setPhone] = useState('')
  const [avatarUrl, setAvatarUrl] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  // Pre-fill with any existing values
  useEffect(() => {
    api('/profile')
      .then(p => {
        setPhone(p?.phone ?? '')
        setAvatarUrl(p?.avatar_url ?? '')
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const canSave = !saving && phone.trim().length > 0 && avatarUrl.trim().length > 0

  async function handleSave() {
    setSaving(true)
    try {
      await api('/profile', {
        method: 'PATCH',
        body: { phone: phone.trim() },
      })
      navigate('/', { replace: true })
    } catch (e) {
      toast(e.message || 'Failed to save profile')
      setSaving(false)
    }
  }

  return (
    <div className="min-h-screen bg-primary text-accent py-12 px-4">
      <div className="mx-auto max-w-sm space-y-6">

        {/* Banner */}
        <div className="rounded-xl border border-secondary/30 bg-secondary/10 px-4 py-3">
          <p className="text-sm font-secondary text-secondary text-center">
            Complete your profile to start using HO:RA
          </p>
        </div>

        <div className="rounded-2xl border border-white/10 bg-[#2D343F] p-6 space-y-6">
          <h1 className="font-heading text-xl text-white text-center">
            Set up your profile
          </h1>

          {loading ? (
            <div className="text-sm text-white/40 text-center py-4">Loading…</div>
          ) : (
            <>
              {/* Avatar */}
              <div className="flex flex-col items-center gap-2">
                <AvatarUploader
                  value={avatarUrl}
                  size={112}
                  onChange={url => setAvatarUrl(url)}
                />
                {!avatarUrl && (
                  <p className="text-xs text-white/40">Photo required</p>
                )}
              </div>

              {/* Phone */}
              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">
                  Phone number <span className="text-red-400">*</span>
                </span>
                <input
                  type="tel"
                  value={phone}
                  onChange={e => setPhone(e.target.value)}
                  placeholder="+1 212 555 0100"
                  className="w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white
                             placeholder-white/30 outline-none focus:border-secondary/50 transition-colors"
                />
              </label>

              {/* Save */}
              <button
                onClick={handleSave}
                disabled={!canSave}
                className="w-full rounded-xl bg-[#3A5A2D] py-3 text-sm font-secondary font-semibold text-white
                           hover:brightness-110 transition-all disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {saving ? 'Saving…' : 'Save & continue'}
              </button>

              {!canSave && !saving && (
                <p className="text-xs text-white/40 text-center">
                  Add a photo and phone number to continue
                </p>
              )}
            </>
          )}
        </div>

      </div>
    </div>
  )
}
