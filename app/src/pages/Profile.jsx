import SupporterStatusBanner from '../components/SupporterStatusBanner'
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, AuthAPI } from '../api/client'
import { useAuth } from '../auth/AuthContext.jsx'
import Modal from '../components/Modal'
import AvatarUploader from '../components/AvatarUploader'
import Earnings from '../components/Earnings'
import PaymentMethods from '../components/PaymentMethods'
import { useToast } from '../providers/ToastProvider'

// Web mirror of mobile's DeleteAccountSheet copy (App Store 5.1.1(v)).
const DELETION_REMOVES = [
  'Your name, phone number, city, photo and bio',
  "Your email and sign-in — you won't be able to sign in again",
  'Saved cards, your location history and notifications',
]
const DELETION_KEEPS =
  'Records of completed tasks and their payments are kept for tax and dispute purposes, with your name replaced by “Deleted user”. Reviews you wrote and safety reports stay too.'

export default function Profile() {
  const navigate = useNavigate()
  const toast = useToast()
  const { setUser } = useAuth()

  // Delete account: the server's blockers (open tasks, a balance, a payout on
  // its way) or the confirmation itself.
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteChecking, setDeleteChecking] = useState(false)
  const [deleteBlockers, setDeleteBlockers] = useState([])
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState('')

  async function openDelete() {
    setDeleteOpen(true)
    setDeleteChecking(true)
    setDeleteError('')
    try {
      const r = await AuthAPI.deletionPreview()
      setDeleteBlockers(Array.isArray(r?.blockers) ? r.blockers : [])
    } catch (e) {
      setDeleteError(e?.message || "Couldn't check your account. Try again.")
    } finally {
      setDeleteChecking(false)
    }
  }

  async function confirmDelete() {
    setDeleting(true)
    setDeleteError('')
    try {
      await AuthAPI.deleteAccount()
      setUser?.(null)
      navigate('/login', { replace: true })
    } catch (e) {
      if (Array.isArray(e?.body?.blockers)) setDeleteBlockers(e.body.blockers)
      else setDeleteError(e?.body?.message || e?.message || "Couldn't delete your account. Try again.")
      setDeleting(false)
    }
  }

  const [name, setName] = useState('')
  const [phone, setPhone] = useState('')
  const [city, setCity] = useState('')
  const [bio, setBio] = useState('')
  const [avatarUrl, setAvatarUrl] = useState('')
  // Approved supporters only see the Earnings card. Held as its own piece of
  // state rather than derived at render because the profile load is the only
  // thing that knows it, and an undefined here would flash the card to
  // requesters for one frame.
  const [supporterStatus, setSupporterStatus] = useState('')
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
        setSupporterStatus(p?.supporter_status ?? '')
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  // A name is required to save, same as phone and photo. New accounts reach
  // this form through the profile gate, so "required here" is "required at
  // signup"; an existing account without one is asked by NamePrompt instead.
  const missing = !name.trim() || !phone.trim() || !avatarUrl

  async function handleSave() {
    setSubmitted(true)
    if (missing) return
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
              {submitted && missing && (
                <div className="rounded-lg bg-red-500/10 border border-red-500/30 px-4 py-3 text-sm text-red-400">
                  Please complete your profile before continuing — name, photo and phone number are required.
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
                {submitted && !name.trim() && (
                  <p className="text-xs text-red-400 mt-1">Name is required</p>
                )}
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

        {/* Cards on file. Its own card rather than a section of the form
            above: saving a card is a Stripe round trip that has nothing to do
            with "Save changes", and putting it inside the form would make one
            button look like it owned both. Renders nothing at all when the
            backend has no Stripe configured. */}
        <PaymentMethods />

        {/* Getting paid. Its own card below the cards-on-file one, and the two
            are deliberately not merged: one is about money leaving this
            person and the other about money arriving, and they apply to
            different roles. Renders nothing for anyone who is not an approved
            supporter. */}
        <Earnings isSupporter={supporterStatus === 'approved'} />

        {/* THE WAY IN, for everyone who is not already in the pipeline. Web's
            mirror of mobile's Profile row: a requester who wants to earn had
            no entry point on this page at all, and the supporter surfaces are
            now hidden everywhere else — so without this the application is
            reachable only by knowing the URL.
            Applied and rejected get their status here instead, as on
            mobile's Profile. This comment used to say those states had
            "their own state elsewhere" — the only place was the Available
            tab, which is hidden from exactly those accounts, so an applicant
            saw nothing and a rejected one had no route to support. */}
        {(supporterStatus === 'applied' || supporterStatus === 'rejected') && (
          <SupporterStatusBanner status={supporterStatus} />
        )}
        {supporterStatus === 'none' && (
          <Link
            to="/become-supporter"
            className="flex items-center justify-between rounded-md border border-white/20 p-4 hover:border-white/40 transition-colors"
          >
            <span>
              <span className="block text-sm font-medium">Become a supporter</span>
              <span className="block text-xs text-white/60">Earn by helping people nearby.</span>
            </span>
            <span className="text-white/40">&rsaquo;</span>
          </Link>
        )}

        {/* In-app account deletion (App Store 5.1.1(v)); server/account_deletion.go. */}
        <div className="pt-4 text-center">
          <button type="button" onClick={openDelete} className="text-xs text-white/50 underline hover:text-white/80">
            Delete account
          </button>
        </div>

        <Modal
          open={deleteOpen}
          onClose={() => { if (!deleting) setDeleteOpen(false) }}
          title={deleteBlockers.length > 0 ? 'Not yet — a few things first' : 'Delete your account?'}
          actions={
            <>
              <button
                className="px-3 py-1.5 rounded-md border border-white/20 hover:border-white/40 text-accent"
                onClick={() => setDeleteOpen(false)}
                disabled={deleting}
              >
                {deleteBlockers.length > 0 ? 'OK' : 'Keep my account'}
              </button>
              {!deleteChecking && deleteBlockers.length === 0 && (
                <button
                  className="px-3 py-1.5 rounded-md bg-red-500/90 hover:bg-red-500 text-black disabled:opacity-60"
                  onClick={confirmDelete}
                  disabled={deleting}
                >
                  {deleting ? 'Deleting…' : 'Delete my account'}
                </button>
              )}
            </>
          }
        >
          {deleteChecking ? (
            <p>Checking your account…</p>
          ) : deleteBlockers.length > 0 ? (
            <div className="space-y-2">
              {deleteBlockers.map((b) => <p key={b.code}>• {b.message}</p>)}
              <p className="text-white/60">Once these are cleared, come back here and the deletion goes through.</p>
            </div>
          ) : (
            <div className="space-y-2">
              <p className="text-white/60">This removes, permanently:</p>
              {DELETION_REMOVES.map((line) => <p key={line}>• {line}</p>)}
              <p className="text-white/60">{DELETION_KEEPS}</p>
            </div>
          )}
          {deleteError && <p className="mt-2 text-red-400 text-sm">{deleteError}</p>}
        </Modal>

      </div>
    </div>
  )
}
