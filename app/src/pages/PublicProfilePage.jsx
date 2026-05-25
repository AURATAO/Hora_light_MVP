import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ShieldCheck, ArrowLeft, Phone, CheckCircle, Clock } from 'lucide-react'
import { api } from '../api/client'

export default function PublicProfile() {
  const { id } = useParams()
  const [profile, setProfile] = useState(null)
  const [completedCount, setCompletedCount] = useState(null)
  const [inProgressCount, setInProgressCount] = useState(null)

  useEffect(() => {
    let ignore = false
    ;(async () => {
      try {
        const [p, completed, inprogress] = await Promise.all([
          api(`/profiles/${id}`),
          api(`/profiles/${id}/tasks?role=assignee&status=completed&limit=100`),
          api(`/profiles/${id}/tasks?role=assignee&status=open&limit=100`),
        ])
        if (ignore) return
        setProfile(p)
        setCompletedCount(Array.isArray(completed) ? completed.length : (completed?.total ?? completed?.items?.length ?? 0))
        setInProgressCount(Array.isArray(inprogress) ? inprogress.length : (inprogress?.total ?? inprogress?.items?.length ?? 0))
      } catch { /* silent */ }
    })()
    return () => { ignore = true }
  }, [id])

  if (!profile) return <div className="p-6">Loading…</div>

  return (
    <div className="bg-linear-to-br from-primary to-primary/30 text-accent min-h-screen py-20 px-4">
      <div className="mx-auto max-w-3xl space-y-4">

        {/* Header */}
        <div className="flex items-start gap-4">
          {profile.avatar_url ? (
            <img
              src={profile.avatar_url}
              alt=""
              className="w-14 h-14 rounded-full object-cover shrink-0"
            />
          ) : (
            <div
              className="w-14 h-14 rounded-full flex items-center justify-center text-white font-bold text-xl shrink-0 select-none"
              style={{ backgroundColor: '#9aab3a' }}
            >
              {(profile.name || '?')[0].toUpperCase()}
            </div>
          )}
          <div className="flex-1 min-w-0 pt-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-heading text-xl text-white">{profile.name || '—'}</span>
              {profile.is_verified_supporter && (
                <span
                  className="inline-flex items-center gap-1 text-xs font-semibold px-2 py-0.5 rounded-full"
                  style={{ backgroundColor: 'rgba(154,171,58,0.2)', color: '#9aab3a' }}
                >
                  <ShieldCheck size={13} />
                  Verified Supporter
                </span>
              )}
            </div>
            {profile.city && (
              <div className="text-sm text-white/60 mt-0.5">{profile.city}</div>
            )}
            {profile.phone && (
              <div className="flex items-center gap-1 text-sm text-white/60 mt-0.5">
                <Phone size={13} />
                {profile.phone}
              </div>
            )}
            {profile.bio && (
              <div className="text-sm text-white/80 mt-1.5 whitespace-pre-wrap">{profile.bio}</div>
            )}
          </div>
        </div>

        {/* Stat cards */}
        <div className="grid grid-cols-2 gap-3">
          <div className="border border-white/15 rounded-lg p-4 bg-white/5 flex flex-col gap-2">
            <CheckCircle size={20} color="#5dcaa5" />
            <span className="font-heading text-3xl text-white">
              {completedCount ?? '—'}
            </span>
            <span className="text-xs text-white/50 uppercase tracking-wide font-secondary">
              Tasks completed
            </span>
          </div>
          <div className="border border-white/15 rounded-lg p-4 bg-white/5 flex flex-col gap-2">
            <Clock size={20} color="#e8a833" />
            <span className="font-heading text-3xl text-white">
              {inProgressCount ?? '—'}
            </span>
            <span className="text-xs text-white/50 uppercase tracking-wide font-secondary">
              In progress
            </span>
          </div>
        </div>

        {/* Back */}
        <Link
          to="/my"
          className="inline-flex items-center gap-1 text-xs opacity-60 hover:opacity-100 transition-opacity"
        >
          <ArrowLeft size={15} />
          Back
        </Link>

      </div>
    </div>
  )
}
