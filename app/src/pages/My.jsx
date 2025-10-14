import { useEffect, useRef, useState } from 'react'         // ← 加 useRef
import { useLocation, useNavigate } from 'react-router-dom' // ← 加這兩個
import { api } from '../api/client'
import { ProfileCard } from '../components/ProfileCard'
import TaskCard from '../components/TaskCard'
import { useAuth } from '../auth/AuthContext'
import NotificationFeed from '../components/NotificationFeed'

function ThinCard({ children, className='' }) {
  return <div className={`border border-white/20 rounded-lg p-3 bg-white/5 ${className}`}>{children}</div>
}

export default function My() {
  const [profile, setProfile] = useState(null)
  const [tab, setTab] = useState('available') // available | assigned | posted | done
  const [lists, setLists] = useState({ available:[], assigned:[], posted:[], done:[] })
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const { user, loading: authLoading } = useAuth()

  const loc = useLocation()          // ← 新增
  const nav = useNavigate()          // ← 新增
  const runIdRef = useRef(0)         // ← 新增：避免舊請求覆蓋新資料
  const [debug, setDebug] = useState(0)

  // Promise.all + race guard
 async function refreshLists() {
  setLoading(true)
  try {
    const [available, assigned, posted, done] = await Promise.all([
      api('/tasks/available'),
      api('/tasks/assigned'),
      api('/tasks/posted'),
      api('/tasks/done'),
    ])

    const asArr = (x, name) => {
      if (Array.isArray(x)) return x
      console.warn(`[refreshLists] ${name} is not array:`, x)
      return [] // 防呆
    }

    setLists({
      available: asArr(available, 'available'),
      assigned : asArr(assigned,  'assigned'),
      posted   : asArr(posted,    'posted'),
      done     : asArr(done,      'done'),
    })
  } catch (e) {
    console.error('[refreshLists] failed:', e)
    // 失敗時也給空陣列，避免下游 map 出錯
    setLists({ available: [], assigned: [], posted: [], done: [] })
  } finally {
    setLoading(false)
  }
}

  // 首次載入：等 auth 就緒，再抓 profile + 列表
  useEffect(() => {
    if (authLoading || !user) return
    api('/profile').then(setProfile)
    refreshLists()
  }, [authLoading, user])

  // ✅ 新增：從 URL 拿 tab（/my?tab=posted）
  useEffect(() => {
    const u = new URL(window.location.href)
    const qTab = u.searchParams.get('tab')
    if (qTab) setTab(qTab) // 'available' | 'assigned' | 'posted' | 'done'
  }, [])

  // ✅ 新增：處理建立任務後導來的 refresh 旗標（/my?refresh=1）
  useEffect(() => {
    const u = new URL(window.location.href)
    const need = u.searchParams.get('refresh') === '1' || loc.state?.refresh
    if (need) {
      refreshLists().finally(() => {
        u.searchParams.delete('refresh')
        nav(u.pathname + (u.search ? `?${u.searchParams.toString()}` : ''), { replace: true, state: {} })
      })
    }
  }, [loc.key]) // loc.key 導航時會變

  // ✅ 你問的這段：視窗回到前景時自動刷新
  useEffect(() => {
    const onFocus = () => refreshLists()
    window.addEventListener('focus', onFocus)
    return () => window.removeEventListener('focus', onFocus)
  }, [])

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
        <ProfileCard />
        <NotificationFeed className="mt-4" />
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
            <TaskList items={lists.available} variant="available" onAccept={acceptTask} onAfterChange={refreshLists} />
          )}
          {tab === 'assigned' && (
            <TaskList items={lists.assigned} variant="assigned" onAfterChange={refreshLists} />
          )}
          {tab === 'posted' && (
            <TaskList items={lists.posted} variant="posted" onAfterChange={refreshLists} />
          )}
          {tab === 'done' && (
            <TaskList items={lists.done} variant="done" onAfterChange={refreshLists} />
          )}
        </ThinCard>
      </div>
    </div>
  )
}

function TaskList({ items, variant, onAccept, onAfterChange }) {
  if (!items?.length) return <div className="text-white/70">No items.</div>
  return (
    <ul className="space-y-2">
      {items.map(t => (
        <TaskCard
          key={t.id}
          task={t}
          variant={variant}
          onAccept={onAccept}
          onAfterChange={onAfterChange}
        />
      ))}
    </ul>
  )
}