import { useEffect, useState } from 'react'         // ← 加 useRef
import { useLocation, useNavigate } from 'react-router-dom' // ← 加這兩個
import { api } from '../api/client'
import { ProfileCard } from '../components/ProfileCard'
import TaskCard from '../components/TaskCard'
import { useAuth } from '../auth/AuthContext'
import NotificationFeed from '../components/NotificationFeed'
import { useLoader } from '../providers/LoaderProvider'
import { useToast } from '../providers/ToastProvider'


function ThinCard({ children, className='' }) {
  return <div className={`border border-white/20 rounded-lg p-3 bg-white/5 ${className}`}>{children}</div>
}

export default function My() {
  const { wrap } = useLoader()
  const toast = useToast()
  const [profile, setProfile] = useState(null)
  const [tab, setTab] = useState('available') // available | assigned | posted | done
  // const [lists, setLists] = useState({ available:[], assigned:[], posted:[], done:[] })
  // const [loading, setLoading] = useState(false)
  const [lists, setLists] = useState({
   available: { items: [], next: null, loading: false, loaded: false },
   assigned : { items: [], next: null, loading: false, loaded: false },
   posted   : { items: [], next: null, loading: false, loaded: false },
   done     : { items: [], next: null, loading: false, loaded: false },
 })
  const [saving, setSaving] = useState(false)
  const { user, loading: authLoading } = useAuth()

  const loc = useLocation()          
  const nav = useNavigate()          


  // Promise.all + race guard
//  async function refreshLists() {
//   setLoading(true)
//   try {
//     const [available, assigned, posted, done] = await Promise.all([
//       api('/tasks/available'),
//       api('/tasks/assigned'),
//       api('/tasks/posted'),
//       api('/tasks/done'),
//     ])

//     const asArr = (x, name) => {
//       if (Array.isArray(x)) return x
//       console.warn(`[refreshLists] ${name} is not array:`, x)
//       return [] // 防呆
//     }

//     setLists({
//       available: asArr(available, 'available'),
//       assigned : asArr(assigned,  'assigned'),
//       posted   : asArr(posted,    'posted'),
//       done     : asArr(done,      'done'),
//     })
//   } catch (e) {
//     console.error('[refreshLists] failed:', e)
//     // 失敗時也給空陣列，避免下游 map 出錯
//     setLists({ available: [], assigned: [], posted: [], done: [] })
//   } finally {
//     setLoading(false)
//   }
// }
 async function refreshLists() {
   // 只刷新當前 tab 第一頁
   await wrap(() => fetchPage(tab, null)) 
 }

  // 首次載入：等 auth 就緒，再抓 profile + 列表
  // useEffect(() => {
  //   if (authLoading || !user) return
  //   api('/profile').then(setProfile)
  //   refreshLists()
  // }, [authLoading, user])
  useEffect(() => {
   if (authLoading || !user) return
    ;(async () => {
      await wrap(async () => {
        const p = await api('/profile')
        setProfile(p)
        await fetchPage(tab, null) // 只抓目前 tab
      })
    })()
  }, [authLoading, user])

  useEffect(() => {
  if (loc.state?.refreshAt) {
    refreshLists()
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
}, [loc.state?.refreshAt])

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
      toast(e.message || 'Failed to save profile')
    } finally {
      setSaving(false)
    }
  }

  // async function acceptTask(id) {
  //   try {
  //     await api(`/tasks/${id}/accept`, { method: 'POST' })
  //     await refreshLists()
  //   } catch (e) {
  //     alert(e.message || 'Failed to accept')
  //   }
  // }
  async function acceptTask(id) {
    try {
    await wrap(async () => {
      await api(`/tasks/${id}/accept`, { method: 'POST' })
      await fetchPage('available', null)
      await fetchPage('assigned',  null)
    })
  } catch (e) {
    toast(e.message || 'Failed to accept')
  }
}



  const tabs = [
    { key:'available', label:'Available' },
    { key:'assigned',  label:'Assigned' },
    { key:'posted',    label:'Posted' },
    { key:'done',      label:'Done' },
  ]

  async function fetchPage(which, cursor=null) {
  setLists(prev => ({ ...prev, [which]: { ...prev[which], loading: true }}))

  const params = new URLSearchParams({ limit: '10' })
  if (cursor?.before_created_at && cursor?.before_id) {
    params.set('before_created_at', cursor.before_created_at)
    params.set('before_id', cursor.before_id)
  }
  try {
    await wrap(async () => {
      const endpoint =
        which === 'posted'   ? 'tasks/posted'   :
        which === 'assigned' ? 'tasks/assigned' :
        which === 'done'     ? 'tasks/done'     :
                               'tasks/available'
      const res = await api(`/${endpoint}?${params.toString()}`)
      const items = Array.isArray(res?.items) ? res.items : (Array.isArray(res) ? res : [])
      const next  = res?.next ?? null
      setLists(prev => ({
        ...prev,
        [which]: {
          items: cursor ? [...prev[which].items, ...items] : items,
          next,
          loading: false,
          loaded: true,
        }
      }))
    })
  } catch (e) {
    console.error('[fetchPage] failed:', e)
    setLists(prev => ({ ...prev, [which]: { ...prev[which], loading: false, loaded: true } }))
  }
}

  return (
  <div className="bg-linear-to-br from-primary to-primary/30 text-accent min-h-screen py-[100px] px-4">
    <div className="mx-auto max-w-3xl space-y-6">
      <div className="min-w-0">
        <ProfileCard />
      </div>

     <NotificationFeed key={String(loc.state?.refreshAt ?? 'static')} className="mt-4" />

      <ThinCard>
        {/* Tabs：手機可滑動、桌機正常；Loading 位置做 RWD */}
        <div className="border-b border-white/10 pb-2 mb-3">
          <div className="flex items-center gap-2">
            {/* 可水平滑動的容器（mobile） */}
            <div className="flex-1 overflow-x-auto md:overflow-visible whitespace-nowrap md:whitespace-normal -mx-1 px-1">
              <div className="inline-flex gap-2">
                {tabs.map(t => (
                  <button
                    key={t.key}
                    className={`shrink-0 px-3 py-1.5 rounded-md border text-sm md:text-base ${
                      tab === t.key ? 'border-white/40 bg-white/10' : 'border-white/10 hover:border-white/30'
                    }`}
                    onClick={() => {
                      setTab(t.key)
                      const u = new URL(window.location.href)
                      u.searchParams.set('tab', t.key)
                      nav(u.pathname + '?' + u.searchParams.toString(), { replace: true })

                      if (!lists[t.key].loaded && !lists[t.key].loading) {
                        fetchPage(t.key, null)
                      }
                    }}
                  >
                    {t.label}
                  </button>
                ))}
              </div>
            </div>

            {/* 桌機：Loading 放右側 */}
            <div className="ml-2 hidden md:block text-sm text-white/70">
              {lists[tab].loading ? 'Loading…' : ''}
            </div>
          </div>

          {/* 手機：Loading 放下一行，避免擠壓 tabs */}
          <div className="mt-2 md:hidden text-xs text-white/60">
            {lists[tab].loading ? 'Loading…' : ''}
          </div>
        </div>

        {tab === 'available' && (
          <TaskList
            items={lists.available.items}
            next={lists.available.next}
            loading={lists.available.loading}
            variant="available"
            onAccept={acceptTask}
            onAfterChange={refreshLists}
            onLoadMore={() => fetchPage('available', lists.available.next)}
          />
        )}

        {tab === 'assigned' && (
          <TaskList
            items={lists.assigned.items}
            next={lists.assigned.next}
            loading={lists.assigned.loading}
            variant="assigned"
            onAfterChange={refreshLists}
            onLoadMore={() => fetchPage('assigned', lists.assigned.next)}
          />
        )}

        {tab === 'posted' && (
          <TaskList
            items={lists.posted.items}
            next={lists.posted.next}
            loading={lists.posted.loading}
            variant="posted"
            onAfterChange={refreshLists}
            onLoadMore={() => fetchPage('posted', lists.posted.next)}
          />
        )}

        {tab === 'done' && (
          <TaskList
            items={lists.done.items}
            next={lists.done.next}
            loading={lists.done.loading}
            variant="done"
            onAfterChange={refreshLists}
            onLoadMore={() => fetchPage('done', lists.done.next)}
          />
        )}
      </ThinCard>
    </div>
  </div>
)
}

function TaskList({ items, next, loading, variant, onAccept, onAfterChange, onLoadMore }) {
  return (
    <div className="min-w-0">
      {(!items || items.length === 0) ? (
        <div className="text-white/70">No items.</div>
      ) : (
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
      )}
      <div className="pt-3">
        {next ? (
          <button
            disabled={loading}
            onClick={onLoadMore}
            className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40 disabled:opacity-50"
          >
            {loading ? 'Loading…' : 'Load more'}
          </button>
        ) : (
          <div className="text-xs text-white/50">{loading ? 'Loading…' : 'No more'}</div>
        )}
      </div>
    </div>
  )
}

