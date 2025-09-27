import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { ProfileCard } from '../components/Profilecard'
import TaskCard from '../components/TaskCard'
import { useRequireAuth } from '../auth/useRequireAuth'

function ThinCard({ children, className='' }) {
  return <div className={`border border-white/20 rounded-lg p-3 bg-white/5 ${className}`}>{children}</div>
}

export default function My() {
   console.log('[My] mounted')

  const [profile, setProfile] = useState(null)
  const [tab, setTab] = useState('available') // available | assigned | posted | done
  const [lists, setLists] = useState({ available:[], assigned:[], posted:[], done:[] })
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const { user, loading: authLoading } = useRequireAuth()

  useEffect(() => {
    if (authLoading || !user) return 
    // load profile
    api('/profile').then(setProfile)
    // load lists
    refreshLists()
  }, [])

  async function refreshLists() {
    setLoading(true)
    try {
      const [available, assigned, posted, done] = await Promise.all([
        api('/tasks/available'),
        api('/tasks/assigned'),
        api('/tasks/posted'),
        api('/tasks/done'),
      ])
      setLists({ available, assigned, posted, done })
    } finally {
      setLoading(false)
    }
  }

  async function saveProfile(patch) {
    setSaving(true)
    try {
      const next = await api('/profile', { method: 'PATCH', body: patch })
      setProfile(next)
    } catch (e) {
      alert(e.message || 'Failed to save profile')
    } finally {
      setSaving(false)
    }
  }

  async function acceptTask(id) {
    try {
      await api(`/tasks/${id}/accept`, { method: 'POST' })
      await refreshLists()
    } catch (e) {
      alert(e.message || 'Failed to accept')
    }
  }

  const tabs = [
    { key:'available', label:'Available' },
    { key:'assigned',  label:'Assigned' },
    { key:'posted',    label:'Posted' },
    { key:'done',      label:'Done' },
  ]

  return (
    <div className="bg-gradient-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
      <div className="mx-auto max-w-3xl space-y-6">

        {/* Profile card */}
        <ProfileCard  />

        {/* Tabs */}
        <ThinCard>
          <div className="flex gap-2 border-b border-white/10 pb-2 mb-3">
            {tabs.map(t => (
              <button key={t.key}
                className={`px-3 py-1.5 rounded-md border ${tab===t.key ? 'border-white/40 bg-white/10' : 'border-white/10 hover:border-white/30'}`}
                onClick={() => setTab(t.key)}
              >
                {t.label}
              </button>
            ))}
            <div className="ml-auto text-sm text-white/70">{loading ? 'Loading…' : ''}</div>
          </div>
          {tab === 'available' && (
            <TaskList items={lists.available} variant="available" onAccept={acceptTask} />
          )}
          {tab === 'assigned' && (
            <TaskList items={lists.assigned} variant="assigned" />
          )}
          {tab === 'posted' && (
            <TaskList items={lists.posted} variant="posted" />
          )}
          {tab === 'done' && (
            <TaskList items={lists.done} variant="done" />
          )}
        </ThinCard>
      </div>
    </div>
  )
}

function LabeledInput({ label, value, onChange, onBlur }) {
  return (
    <label className="text-sm grid gap-1">
      <span className="text-white/80">{label}</span>
      <input
        className="bg-transparent outline-none border border-white/10 focus:border-white/30 rounded px-2 py-1"
        value={value}
        onChange={(e)=> onChange(e.target.value)}
        onBlur={onBlur}
      />
    </label>
  )
}

function TaskList({ items,variant, onAccept, showManage=false }) {
  if (!items?.length) return <div className="text-white/70">No items.</div>
  return (
    <ul className="space-y-2">
      {items.map(t => (
         <TaskCard
          key={t.id}
          task={t}
          variant={variant}      // 'available' | 'assigned' | 'posted' | 'done'
          onAccept={onAccept}    // 只有 available 會用到
        />
      ))}
    </ul>
  )
}