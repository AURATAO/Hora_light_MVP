import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext.jsx'
import { api } from '../api/client'

// ── Stat card ─────────────────────────────────────────────────────────────────

function StatCard({ label, value, icon, loading }) {
  return (
    <div className="rounded-2xl border border-secondary/20 bg-surface p-4 flex flex-col gap-2">
      <span className="text-secondary text-xl">{icon}</span>
      <span className="text-2xl font-secondary font-bold text-white">
        {loading ? <span className="opacity-30">—</span> : value}
      </span>
      <span className="text-xs text-white/50 tracking-wide uppercase font-secondary">{label}</span>
    </div>
  )
}

// ── Quick action card ──────────────────────────────────────────────────────────

function ActionCard({ to, icon, title, subtitle }) {
  return (
    <Link
      to={to}
      className="rounded-2xl border border-secondary/20 bg-surface p-5 flex flex-col gap-3
                 hover:border-secondary/50 hover:bg-white/5 transition-colors group"
    >
      <span className="text-secondary text-2xl">{icon}</span>
      <div>
        <div className="font-secondary font-semibold text-white group-hover:text-secondary transition-colors">
          {title}
        </div>
        <div className="text-xs text-white/40 mt-0.5">{subtitle}</div>
      </div>
    </Link>
  )
}

// ── Main ───────────────────────────────────────────────────────────────────────

export default function Dashboard() {
  const { user } = useAuth()
  const [profile, setProfile] = useState(null)
  const [stats, setStats] = useState({ assigned: null, posted: null, done: null })
  const [activeTask, setActiveTask] = useState(undefined) // undefined = loading
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let alive = true
    async function load() {
      try {
        const [p, assigned, posted, done] = await Promise.all([
          api('/profile'),
          api('/tasks/assigned?limit=20'),
          api('/tasks/posted?limit=20'),
          api('/tasks/done?limit=20'),
        ])
        if (!alive) return

        const toItems = (r) => Array.isArray(r) ? r : (r?.items ?? [])
        const toCount = (r) => {
          const items = toItems(r)
          const hasMore = !Array.isArray(r) && r?.next
          return hasMore ? `${items.length}+` : items.length
        }

        const assignedItems = toItems(assigned)

        setProfile(p)
        setActiveTask(assignedItems[0] ?? null)
        setStats({
          assigned: toCount(assigned),
          posted:   toCount(posted),
          done:     toCount(done),
        })
      } catch (e) {
        console.error('[Dashboard] load error', e)
        if (alive) setActiveTask(null)
      } finally {
        if (alive) setLoading(false)
      }
    }
    load()
    return () => { alive = false }
  }, [])

  const displayName = profile?.name || user?.email?.split('@')[0] || 'there'

  return (
    <div className="min-h-screen bg-primary text-accent py-10 px-4">
      <div className="max-w-2xl mx-auto space-y-6">

        {/* ── Greeting ── */}
        <div>
          <h1 className="font-heading text-3xl text-white">
            Hello, {displayName} 👋
          </h1>
          <p className="text-white/40 mt-1 text-sm font-secondary">
            Here's what's happening today.
          </p>
        </div>

        {/* ── Stats row ── */}
        <div className="grid grid-cols-3 gap-3">
          <StatCard label="Active"    value={stats.assigned} icon="⚡" loading={loading} />
          <StatCard label="Posted"    value={stats.posted}   icon="📋" loading={loading} />
          <StatCard label="Completed" value={stats.done}     icon="✓"  loading={loading} />
        </div>

        {/* ── Active task hero ── */}
        <div className="rounded-2xl border border-brand/40 bg-surface p-6">
          {activeTask === undefined ? (
            /* skeleton */
            <div className="animate-pulse space-y-3">
              <div className="h-3 bg-white/10 rounded w-1/4" />
              <div className="h-5 bg-white/10 rounded w-3/5" />
              <div className="h-3 bg-white/10 rounded w-2/5" />
              <div className="h-9 bg-white/10 rounded-xl w-32 mt-4" />
            </div>
          ) : activeTask ? (
            /* has active task */
            <div className="space-y-4">
              <div className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-gold animate-pulse" />
                <span className="text-xs font-secondary text-gold uppercase tracking-widest">
                  Active Task
                </span>
              </div>
              <h2 className="font-heading text-xl text-white leading-snug">
                {activeTask.title}
              </h2>
              <p className="text-sm text-white/50">
                {activeTask.estimated_minutes} min estimated
                {activeTask.location_text ? ` · ${activeTask.location_text}` : ''}
              </p>
              <Link
                to={`/tasks/${activeTask.id}`}
                className="inline-block rounded-xl bg-brand px-5 py-2.5 text-sm font-secondary
                           font-semibold text-white hover:brightness-110 transition-all"
              >
                Open Task →
              </Link>
            </div>
          ) : (
            /* no active task */
            <div className="text-center py-4 space-y-3">
              <div className="text-4xl">🌿</div>
              <h2 className="font-heading text-xl text-white">Ready to start?</h2>
              <p className="text-sm text-white/50">You have no active tasks right now.</p>
              <Link
                to="/my?tab=available"
                className="inline-block rounded-xl bg-brand px-5 py-2.5 text-sm font-secondary
                           font-semibold text-white hover:brightness-110 transition-all"
              >
                Browse Available Tasks
              </Link>
            </div>
          )}
        </div>

        {/* ── Quick actions ── */}
        <div className="grid grid-cols-2 gap-4">
          <ActionCard
            to="/tasks/new"
            icon="✦"
            title="Post a Task"
            subtitle="Request help now"
          />
          <ActionCard
            to="/my"
            icon="☰"
            title="My Tasks"
            subtitle="View all your tasks"
          />
        </div>

      </div>
    </div>
  )
}
