import { useEffect, useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { api } from '../api/client'
import TaskCard from '../components/TaskCard'

function TabButton({ active, children, onClick }) {
  return (
    <button
      onClick={onClick}
      className={`px-3 py-1.5 rounded-md border ${active ? 'border-white/40 bg-white/10' : 'border-white/10 hover:border-white/30'}`}
    >
      {children}
    </button>
  )
}

export default function PublicProfile() {
  const { id } = useParams()
  const [profile, setProfile] = useState(null)
  const [tab, setTab] = useState('completed') // completed | inprogress | posted
  const [list, setList] = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let ignore = false
    ;(async () => {
      setLoading(true)
      try {
        const p = await api(`/profiles/${id}`)
        if (!ignore) setProfile(p)
      } finally {
        setLoading(false)
      }
    })()
    return () => (ignore = true)
  }, [id])

  useEffect(() => {
    let ignore = false
    ;(async () => {
      setLoading(true)
      try {
        let role = 'assignee', status = 'completed'
        if (tab === 'inprogress') { role = 'assignee'; status = 'open' }
        if (tab === 'posted')     { role = 'requester'; status = 'all' }

        const qs = new URLSearchParams({ role, status, limit: '20' })
        const items = await api(`/profiles/${id}/tasks?` + qs.toString())
        if (!ignore) setList(items)
      } finally {
        setLoading(false)
      }
    })()
    return () => (ignore = true)
  }, [id, tab])

  if (!profile) return <div className="p-6">Loading…</div>

  return (
    <div className="bg-linear-to-br from-primary to-primary/30 text-accent min-h-screen py-20 px-4">
      <div className="mx-auto max-w-3xl space-y-6">
        {/* Header */}
        <div className="flex items-start gap-4 border border-white/20 rounded-lg p-4 bg-white/5">
          <img src={profile.avatar_url || 'https://placehold.co/80x80?text=Avatar'} alt="" className="w-16 h-16 rounded-full object-cover border border-white/20" />
          <div className="flex-1">
            <div className="text-xl font-semibold">{profile.name || '—'}</div>
            <div className="text-sm opacity-70">{profile.city || '—'}</div>
            {profile.bio && <div className="text-sm mt-1 opacity-90 whitespace-pre-wrap">{profile.bio}</div>}
          </div>
        </div>

        {/* Tabs */}
        <div className="border border-white/20 rounded-lg p-3 bg-white/5">
          <div className="flex gap-2 border-b border-white/10 pb-2 mb-3 text-xs ">
            <TabButton active={tab==='completed'} onClick={()=>setTab('completed')}>Completed</TabButton>
            <TabButton active={tab==='inprogress'} onClick={()=>setTab('inprogress')}>In progress</TabButton>
            <TabButton active={tab==='posted'} onClick={()=>setTab('posted')}>Posted</TabButton>
            <Link to="/my" className="ml-auto text-sm underline opacity-80 pl-3 hover:opacity-100">Back</Link>
          </div>

          {loading ? (
            <div className="text-white/70">Loading…</div>
          ) : list.length === 0 ? (
            <div className="text-white/70">No items.</div>
          ) : (
            <ul className="space-y-2">
              {list.map(t => (
                <TaskCard key={t.id} task={t} variant="public" />
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  )
}