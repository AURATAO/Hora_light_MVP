import { useEffect, useState } from 'react'
import { useLocation, useNavigate, Link } from 'react-router-dom'
import { Hand, Settings } from 'lucide-react'
import { api } from '../api/client'
import TaskCard from '../components/TaskCard'
import SkeletonCard from '../components/SkeletonCard'
import { useAuth } from '../auth/AuthContext'
import NotificationFeed from '../components/NotificationFeed'
import { useLoader } from '../providers/LoaderProvider'
import { useToast } from '../providers/ToastProvider'
import SupporterStatusBanner from '../components/SupporterStatusBanner'


function ThinCard({ children, className='' }) {
  return <div className={`border border-white/20 rounded-lg p-3 bg-white/5 ${className}`}>{children}</div>
}

export default function My() {
  const { wrap } = useLoader()
  const toast = useToast()
  const [profile, setProfile] = useState(null)
  const [tab, setTab] = useState('posted') // available | assigned | posted | done
  // const [lists, setLists] = useState({ available:[], assigned:[], posted:[], done:[] })
  // const [loading, setLoading] = useState(false)
  const [lists, setLists] = useState({
   available: { items: [], next: null, loading: false, loaded: false },
   assigned : { items: [], next: null, loading: false, loaded: false },
   posted   : { items: [], next: null, loading: false, loaded: false },
   // The requester's FINISHED tasks, read from /tasks/posted/closed — which
   // is authoritative for them and is the only source that carries the count
   // behind "See all (N)".
   postedClosed: { items: [], next: null, total: 0, loading: false, loaded: false },
   done     : { items: [], next: null, loading: false, loaded: false },
 })
  const [saving, setSaving] = useState(false)
  // Whether the posted history has been opened past its three-row preview.
  const [postedHistoryExpanded, setPostedHistoryExpanded] = useState(false)
  const { user, loading: authLoading } = useAuth()

  // EVERY TAB BUT "POSTED" IS A SUPPORTER SURFACE. Available is other people's
  // jobs, Assigned is what you are working on, Done is what you were paid for —
  // none of it exists for a requester-only account, and offering all three as
  // permanently-empty tabs is the single loudest way the app told them they
  // were in the wrong place.
  //
  // A LONE TAB IS NOISE for the same reason: "Posted" over a list of posted
  // tasks says nothing the heading above it does not. So the strip disappears
  // entirely rather than shrinking to one.
  //
  // Read from /auth/me, which now derives supporter_status from the same
  // function the profile endpoint uses. is_verified_supporter is the fallback
  // for a client running against a server that predates that field — it is
  // what the derivation's first case reads anyway, so the two agree today and
  // the fallback simply keeps an older pairing working.
  const isApprovedSupporter =
    user?.supporter_status === 'approved' ||
    (user?.supporter_status === undefined && Boolean(user?.is_verified_supporter))
  const tabs = isApprovedSupporter
    ? [
      { key:'posted',    label:'Posted' },
      { key:'available', label:'Available' },
      { key:'assigned',  label:'Assigned' },
      { key:'done',      label:'Done' },
    ]
    : [{ key:'posted', label:'Posted' }]


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
   //
   // The Posted tab is two lists now — live tasks and finished ones — and a
   // cancel or a completion moves a row from the first to the second, so
   // refreshing only `posted` would leave the row visible in Active and
   // absent from History until a reload.
   await wrap(async () => {
     await fetchPage(tab, null)
     if (tab === 'posted') await fetchPage('postedClosed', null)
   })
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
        // Posted always; the supporter lists only for a supporter. The
        // endpoints would answer with empty arrays, but three round trips on
        // every dashboard load for lists that cannot render is three too many.
        await Promise.all([
          fetchPage('posted', null),
          fetchPage('postedClosed', null),
          ...(isApprovedSupporter
            ? [fetchPage('available', null), fetchPage('assigned', null), fetchPage('done', null)]
            : []),
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
    // Only a tab this account actually has. A stale bookmark or a link from
    // before approval was revoked would otherwise select a tab with no button
    // to leave it by.
    if (qTab && tabs.some(t => t.key === qTab)) setTab(qTab)
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
    if (e?.body?.error === 'not available') {
      // Lost the race — someone else accepted first. Refresh so the task drops
      // out of the Available list.
      toast('This task was just accepted by someone else.')
      await wrap(() => fetchPage('available', null))
    } else {
      toast(e?.body?.error || e.message || 'Failed to accept')
    }
  }
}



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
        which === 'posted'       ? 'tasks/posted'        :
        which === 'postedClosed' ? 'tasks/posted/closed'  :
        which === 'assigned'     ? 'tasks/assigned'       :
        which === 'done'         ? 'tasks/done'           :
                                   'tasks/available'
      const res = await api(`/${endpoint}?${params.toString()}`)
      const items = Array.isArray(res?.items) ? res.items : (Array.isArray(res) ? res : [])
      const next  = res?.next ?? null
      setLists(prev => ({
        ...prev,
        [which]: {
          items: cursor ? [...prev[which].items, ...items] : items,
          next,
          // The whole-history count, where the endpoint sends one. Kept across
          // pages: it describes the history, not the page just fetched.
          total: typeof res?.total === 'number' ? res.total : (prev[which].total ?? 0),
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

  // ── The posted split ─────────────────────────────────────────────────────
  //
  // Active comes from /tasks/posted with the finished rows filtered out (that
  // endpoint has no status filter); history comes from /tasks/posted/closed,
  // which is authoritative for it and carries the count.
  const POSTED_HISTORY_PREVIEW = 3
  const CLOSED = new Set(['completed', 'cancelled', 'removed'])
  const postedActive = (lists.posted.items || []).filter(t => !CLOSED.has(t.status))
  const postedHistory = lists.postedClosed.items || []
  const postedHistoryTotal = lists.postedClosed.total || postedHistory.length
  const postedHistoryShown = postedHistoryExpanded
    ? postedHistory
    : postedHistory.slice(0, POSTED_HISTORY_PREVIEW)

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
        {/* Active and Completed count the SUPPORTER's work — tasks assigned to
            them and tasks they were paid for. Both read zero forever on a
            requester-only account, and a zero is a statement: it says "you
            have done none of this", which is not true of somebody the feature
            does not apply to. Posted is theirs and stays. */}
        <div className="flex items-center gap-2 flex-wrap">
          {isApprovedSupporter && (
            <span className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-white/5 border border-white/15 text-xs">
              <span className="w-2 h-2 rounded-full bg-amber-400 shrink-0" />
              Active&nbsp;<span className="font-semibold">{lists.assigned.items.length}{lists.assigned.next ? '+' : ''}</span>
            </span>
          )}
          <span className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-white/5 border border-white/15 text-xs">
            <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: '#9aab3a' }} />
            Posted&nbsp;<span className="font-semibold">{lists.posted.items.length}{lists.posted.next ? '+' : ''}</span>
          </span>
          {isApprovedSupporter && (
            <span className="flex items-center gap-1.5 px-3 py-1 rounded-full bg-white/5 border border-white/15 text-xs">
              <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: '#5dcaa5' }} />
              Completed&nbsp;<span className="font-semibold">{lists.done.items.length}{lists.done.next ? '+' : ''}</span>
            </span>
          )}
        </div>
      </div>

     <NotificationFeed key={String(loc.state?.refreshAt ?? 'static')} />

      <ThinCard>
        {/* Tabs：手機可滑動、桌機正常；Loading 位置做 RWD */}
        <div className={`${tabs.length > 1 ? 'border-b border-white/10 pb-2 mb-3' : 'mb-1'}`}>
          <div className="flex items-center gap-2">
            {/* 可水平滑動的容器（mobile） */}
            <div className="flex-1 overflow-x-auto md:overflow-visible whitespace-nowrap md:whitespace-normal -mx-1 px-1">
              <div className="inline-flex gap-2">
                {/* A single tab renders nothing: the heading above already
                    says these are their tasks. */}
                {tabs.length > 1 && tabs.map(t => (
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
            <SupporterStatusBanner
              status={profile?.supporter_status || 'none'}
              onApply={() => nav('/become-supporter')}
            />
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

        {/* POSTED SPLITS INTO LIVE AND FINISHED. GET /tasks/posted has no
            status filter (server/main.go listMyTasks), so this one list held
            both — and the live tasks, which are the whole reason somebody
            opens this page, were pushed further down it the longer they had
            been using HO:RA.

            Active stays whole. Finished shows the three most recent with a
            way to the rest, which is exactly what mobile does. */}
        {tab === 'posted' && (
          <>
            <TaskList
              items={postedActive}
              next={lists.posted.next}
              loading={lists.posted.loading}
              variant="posted"
              onAfterChange={refreshLists}
              onLoadMore={() => fetchPage('posted', lists.posted.next)}
            />
            {postedHistory.length > 0 && (
              <div className="mt-6 border-t border-white/10 pt-4">
                <div className="mb-3 flex items-center justify-between">
                  <div className="text-sm text-white/70">History</div>
                  {/* The NUMBER, on purpose: "See all" alone makes a reader
                      guess whether there are four or four hundred. */}
                  {postedHistoryTotal > POSTED_HISTORY_PREVIEW && (
                    <button
                      type="button"
                      onClick={() => setPostedHistoryExpanded(v => !v)}
                      className="text-xs text-white/60 underline hover:text-white"
                    >
                      {postedHistoryExpanded ? 'Show less' : `See all (${postedHistoryTotal})`}
                    </button>
                  )}
                </div>
                <TaskList
                  items={postedHistoryShown}
                  next={postedHistoryExpanded ? lists.postedClosed.next : null}
                  loading={lists.postedClosed.loading}
                  variant="posted"
                  onAfterChange={refreshLists}
                  onLoadMore={() => fetchPage('postedClosed', lists.postedClosed.next)}
                />
              </div>
            )}
          </>
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

