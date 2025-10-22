// app/src/components/UserPill.jsx
import { Link } from 'react-router-dom'
import { useProfileById } from '../hooks/useProfileById'

export default function UserPill({ userId, label = 'User', meId }) {
  if (!userId) return <span className="text-white/60">—</span>

  const { data, loading } = useProfileById(userId)
  const isMe = meId && userId === meId
  const href = isMe ? '/my' : `/u/${userId}`

  if (loading) {
    return (
      <span className="inline-flex items-center gap-2 px-2 py-1 rounded bg-white/10 border border-white/15">
       <span className="w-6 h-6 rounded-full bg-white/20 animate-pulse" />
        <span className="h-3 w-16 rounded bg-white/20 animate-pulse" />
      </span>
    )
  }

const avatar =
    data?.avatar_url ||
    data?.avatar ||
    data?.photo_url ||
    'https://placehold.co/32x32?text=:)'

  const emailLocal = (data?.email && String(data.email).split('@')[0]) || ''
  const shortId = String(userId).slice(0, 8)
  const displayBase =
    data?.name?.trim() ||
    data?.display_name?.trim() ||
    emailLocal ||
    `${label} ${shortId}`
  const display = isMe ? 'You' : displayBase

  return (
    <Link
      to={href}
      className="inline-flex items-center gap-2 px-2 py-1 rounded bg-white/10 hover:bg-white/15 border border-white/15"
      title={isMe ? 'Go to My profile' : 'View profile'}
    >
      <img src={avatar} alt="" className="w-6 h-6 rounded-full object-cover" />
      <span className="text-sm">{display}</span>
    </Link>
  )
}