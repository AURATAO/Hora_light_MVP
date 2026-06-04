import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import AvatarUploader from '../components/AvatarUploader'
import { useToast } from '../providers/ToastProvider'

export default function Profile() {
  const navigate = useNavigate()
  const toast = useToast()

  const [name, setName] = useState('')
  const [phone, setPhone] = useState('')
  const [city, setCity] = useState('')
  const [bio, setBio] = useState('')
  const [avatarUrl, setAvatarUrl] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  useEffect(() => {
    api('/profile')
      .then(p => {
        setName(p?.name ?? '')
        setPhone(p?.phone ?? '')
        setCity(p?.city ?? '')
        setBio(p?.bio ?? '')
        setAvatarUrl(p?.avatar_url ?? '')
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  async function handleSave() {
    setSubmitted(true)
    if (!phone.trim() || !avatarUrl) return
    setSaving(true)
    try {
      await api('/profile', {
        method: 'PATCH',
        body: {
          name: name.trim(),
          phone: phone.trim(),
          city: city.trim(),
          bio: bio.trim(),
        },
      })
      navigate('/my', { replace: true })
    } catch (e) {
      toast(e.message || 'Failed to save profile')
      setSaving(false)
    }
  }

  return (
    <div className="min-h-screen bg-primary text-accent py-12 px-4">
      <div className="mx-auto max-w-sm space-y-4">

        <Link to="/my" className="inline-block text-sm opacity-60 hover:opacity-100 transition-opacity">
          ← Back
        </Link>

        <div className="rounded-2xl border border-white/10 bg-[#2D343F] p-6 space-y-5">
          <h1 className="font-heading text-xl text-white text-center">Edit Profile</h1>

          {loading ? (
            <div className="text-sm text-white/40 text-center py-4">Loading…</div>
          ) : (
            <>
              {submitted && (!phone.trim() || !avatarUrl) && (
                <div className="rounded-lg bg-red-500/10 border border-red-500/30 px-4 py-3 text-sm text-red-400">
                  Please complete your profile before continuing — photo and phone number are required.
                </div>
              )}

              {/* Avatar */}
              <div className="flex justify-center flex-col items-center">
                <AvatarUploader
                  value={avatarUrl}
                  size={112}
                  onChange={url => setAvatarUrl(url)}
                />
                {submitted && !avatarUrl && (
                  <p className="text-xs text-red-400 mt-1">Please upload a profile photo</p>
                )}
              </div>

              {/* Name */}
              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">Name</span>
                <input
                  type="text"
                  value={name}
                  onChange={e => setName(e.target.value)}
                  placeholder="Your name"
                  className="w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white
                             placeholder-white/30 outline-none focus:border-secondary/50 transition-colors"
                />
              </label>

              {/* Phone */}
              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">Phone number</span>
                <input
                  type="tel"
                  value={phone}
                  onChange={e => setPhone(e.target.value)}
                  placeholder="+1 212 555 0100"
                  className="w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white
                             placeholder-white/30 outline-none focus:border-secondary/50 transition-colors"
                />
                {submitted && !phone.trim() && (
                  <p className="text-xs text-red-400 mt-1">Phone number is required</p>
                )}
              </label>

              {/* City */}
              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">City</span>
                <input
                  type="text"
                  value={city}
                  onChange={e => setCity(e.target.value)}
                  placeholder="e.g. New York"
                  className="w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white
                             placeholder-white/30 outline-none focus:border-secondary/50 transition-colors"
                />
              </label>

              {/* Bio */}
              <label className="block space-y-1.5">
                <span className="text-sm font-secondary text-white/70">Bio</span>
                <textarea
                  rows={3}
                  value={bio}
                  onChange={e => setBio(e.target.value)}
                  placeholder="A short intro about yourself…"
                  className="w-full rounded-lg border border-white/15 bg-white/5 px-3 py-2 text-sm text-white
                             placeholder-white/30 outline-none focus:border-secondary/50 transition-colors resize-none"
                />
              </label>

              {/* Save */}
              <button
                onClick={handleSave}
                className="w-full rounded-xl py-3 text-sm font-secondary font-semibold text-white
                           hover:brightness-110 transition-all"
                style={{ backgroundColor: '#3A5A2D' }}
              >
                {saving ? 'Saving…' : 'Save changes'}
              </button>
            </>
          )}
        </div>

      </div>
    </div>
  )
}
