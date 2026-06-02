import { useEffect, useState } from 'react'         // ← 加 useRef
import { useLocation, useNavigate, Link } from 'react-router-dom' // ← 加這兩個
import { Hand, Settings } from 'lucide-react'
import { api } from '../api/client'
import TaskCard from '../components/TaskCard'
import SkeletonCard from '../components/SkeletonCard'
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
        await Promise.all([
          fetchPage('available', null),
          fetchPage('assigned', null),
          fetchPage('posted', null),
          fetchPage('done', null),
        ])
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
    { key:'posted',    label:'Posted' },
    { key:'available', label:'Available' },
    { key:'assigned',  label:'Assigned' },
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
    <div className="mx-auto max-w-3xl space-y-3">

      {/* Top bar + stat pills grouped tightly */}
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-3 min-w-0">
            <Link
              to="/profile"
              className="w-14 h-14 rounded-full shrink-0 select-none hover:brightness-110 transition-all overflow-hidden"
              style={profile?.avatar_url ? undefined : { backgroundColor: '#9aab3a' }}
            >
              {profile?.avatar_url ? (
                <img src={profile.avatar_url} alt="" className="w-14 h-14 rounded-full object-cover" />
              ) : (
                <span className="w-14 h-14 flex items-center justify-center text-white font-bold text-lg">
                  {(profile?.name || user?.email || '?')[0].toUpperCase()}
                </span>
              )}
            </Link>
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-heading text-lg text-white truncate inline-flex items-center gap-1">
                  Hey, {profile?.name || user?.email?.split('@')[0] || 'there'}
                  <Hand size={18} className="opacity-80 shrink-0" />
                </span>
                {user?.is_verified_supporter && (
                  <span
                    className="text-xs font-semibold px-2 py-0.5 rounded-full border"
                    style={{ color: '#9aab3a', borderColor: '#9aab3a' }}
                  >
                    ✓ Verified Supporter
                  </span>
                )}
                <Link to="/profile" className="opacity-40 hover:opacity-80 transition-opacity" title="Edit profile">
                  <Settings size={15} />
                </Link>
              </div>
            </div>
          </div>
          <Link
            to="/category"
            className="shrink-0 rounded-full border px-3 py-1.5 text-sm transition-colors hover:bg-white/5"
            style={{ borderColor: '#9aab3a', color: '#9aab3a' }}
          >
            + Post a Task
          </Link>
        </div>

        {/* Stat pills */}
        <div className="flex items-center gap-2 flex-wrap">
          <span className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-white/5 border border-white/15 text-xs">
            <span className="w-2 h-2 rounded-full bg-amber-400 shrink-0" />
            Active&nbsp;<span className="font-semibold">{lists.assigned.items.length}{lists.assigned.next ? '+' : ''}</span>
          </span>
          <span className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-white/5 border border-white/15 text-xs">
            <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: '#9aab3a' }} />
            Posted&nbsp;<span className="font-semibold">{lists.posted.items.length}{lists.posted.next ? '+' : ''}</span>
          </span>
          <span className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-white/5 border border-white/15 text-xs">
            <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: '#5dcaa5' }} />
            Completed&nbsp;<span className="font-semibold">{lists.done.items.length}{lists.done.next ? '+' : ''}</span>
          </span>
        </div>
      </div>

     <NotificationFeed key={String(loc.state?.refreshAt ?? 'static')} />

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
                    className={`shrink-0 px-3 py-1.5 rounded-full border text-sm font-secondary tracking-wide transition-colors ${
                      tab === t.key
                        ? 'border-transparent text-white'
                        : 'border-white/10 text-white/60 hover:border-white/25 hover:text-white/80'
                    }`}
                    style={tab === t.key ? { backgroundColor: '#9aab3a' } : undefined}
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
          !user?.is_verified_supporter ? (
            <VerificationBanner />
          ) : (
            <TaskList
              items={lists.available.items}
              next={lists.available.next}
              loading={lists.available.loading}
              variant="available"
              onAccept={acceptTask}
              onAfterChange={refreshLists}
              onLoadMore={() => fetchPage('available', lists.available.next)}
            />
          )
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

const CHECKR_URL = 'https://checkr.com'

function VerificationBanner() {
  return (
    <div className="rounded-2xl border border-white/10 bg-surface p-6 flex flex-col gap-4 text-center items-center">
      <div className="space-y-1">
        <h2 className="font-heading text-xl text-white">Become a Verified Supporter</h2>
        <p className="text-sm text-white/60 max-w-sm">
          To accept tasks on HO:RA, complete a quick background check. Takes ~5 minutes.
        </p>
      </div>
      <a
        href={CHECKR_URL}
        target="_blank"
        rel="noopener noreferrer"
        className="rounded-xl px-6 py-2.5 text-sm font-secondary font-semibold text-white transition-all hover:brightness-110"
        style={{ backgroundColor: '#9aab3a' }}
      >
        Get Verified →
      </a>
      <p className="text-xs text-white/40">Already applied? We'll notify you once approved.</p>
    </div>
  )
}

const EMPTY_STATES = {
  available: {
    message: 'No tasks available right now.',
    cta: null,
  },
  assigned: {
    message: "You haven't accepted any tasks yet.",
    cta: null,
  },
  posted: {
    message: "No tasks yet — post your first one!",
    cta: { label: 'Post a task', to: '/category' },
  },
  done: {
    message: 'No completed tasks yet.',
    cta: null,
  },
}

function TaskList({ items, next, loading, variant, onAccept, onAfterChange, onLoadMore }) {
  const empty = EMPTY_STATES[variant] || { message: 'Nothing here yet.', cta: null }
  return (
    <div className="min-w-0">
      {loading && (!items || items.length === 0) ? (
        <SkeletonCard count={3} />
      ) : (!items || items.length === 0) ? (
        <div className="py-8 flex flex-col items-center gap-3 text-center">
          <p className="text-white/60 text-sm">{empty.message}</p>
          {empty.cta && (
            <Link
              to={empty.cta.to}
              className="rounded-md border border-white/20 px-4 py-1.5 text-sm hover:border-white/40 transition-colors"
            >
              {empty.cta.label}
            </Link>
          )}
        </div>
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
            className="rounded-md border border-white/20 px-3 py-1 text-sm hover:border-white/40 disabled:opacity-50 flex items-center gap-2"
          >
            {loading ? (
              <>
                <span className="inline-block w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin" />
                Loading…
              </>
            ) : 'Load more'}
          </button>
        ) : items?.length > 0 ? (
          <p className="text-xs text-white/40">You've reached the end.</p>
        ) : null}
      </div>
    </div>
  )
}

