import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useNotifications } from '../hooks/useNotifications'
import { NOTIFICATIONS_PAGE_SIZE, hasMoreAfter } from '../lib/notificationsPage'

function timeAgo(dateStr) {
  const diff = Math.floor((Date.now() - new Date(dateStr)) / 1000)
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

export default function NotificationFeed({ limit = NOTIFICATIONS_PAGE_SIZE, className = '' }) {
  const [showAll, setShowAll] = useState(false)
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const {
    items, loading, fetchList, fetchMore,
    markRead, markAllRead,
    remove, clearRead,
  } = useNotifications()

  // Load, and leave the read state alone. This used to call mark-read-all
  // before fetching, so simply arriving on /my cleared the unread state of
  // everything — a failed payment or a budget request the user had not seen
  // yet included — behind a collapsed one-line bar. A notification is read
  // when it is opened or marked, as on mobile.
  useEffect(() => {
    let alive = true
    fetchList({ limit })
      .then((page) => { if (alive) setHasMore(hasMoreAfter(page, limit)) })
      .catch(() => {})
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [limit])

  async function loadMore() {
    if (loadingMore || !hasMore) return
    setLoadingMore(true)
    try {
      const page = await fetchMore({ limit })
      setHasMore(hasMoreAfter(page, limit))
    } catch {
      // Leave the button: a failed page is a reason to try again, not the end.
    } finally {
      setLoadingMore(false)
    }
  }

  const hasAny = items.length > 0
  const hasRead = items.some(n => !n.unread)
  const hasUnread = items.some(n => n.unread)

  if (loading || !hasAny) return null

  const mostRecent = items.find(n => n.unread) ?? items[0]

  return (
    <div className={className}>
      {/* Compact notification bar */}
      <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-white/5 border border-white/15">
        {/* The dot means unread. It used to be drawn unconditionally. */}
        {hasUnread && <span className="w-2 h-2 rounded-full bg-red-500 shrink-0" aria-label="Unread notifications" />}
        <span className="text-sm flex-1 truncate min-w-0">{mostRecent.title}</span>
        <span className="text-xs opacity-60 shrink-0 whitespace-nowrap">{timeAgo(mostRecent.created_at)}</span>
        <button
          className="text-xs underline opacity-80 hover:opacity-100 shrink-0 whitespace-nowrap"
          onClick={() => setShowAll(v => !v)}
        >
          {showAll ? 'Close' : `See all (${items.length}${hasMore ? '+' : ''})`}
        </button>
      </div>

      {/* Expanded full list */}
      {showAll && (
        <div className="mt-2 border border-white/20 rounded-lg p-3 bg-white/5">
          <div className="flex items-center gap-2 mb-2">
            <div className="font-semibold text-sm">Notifications</div>
            <div className="ml-auto flex items-center gap-3">
              {hasUnread && (
                <button
                  className="text-xs underline opacity-80 hover:opacity-100"
                  onClick={markAllRead}
                >
                  Mark all read
                </button>
              )}
              {hasRead && (
                <button
                  className="text-xs underline opacity-80 hover:opacity-100"
                  onClick={clearRead}
                >
                  Clear read
                </button>
              )}
            </div>
          </div>
          <ul className="space-y-2">
            {items.map((n) => (
              <li key={n.id} className={`rounded p-2 ${n.unread ? 'bg-white/10' : 'bg-white/5'}`}>
                <div className="flex justify-between gap-3">
                  <div className="min-w-0">
                    <div className="text-sm font-medium truncate">{n.title}</div>
                    {n.body && <div className="text-sm opacity-80 wrap-break-words">{n.body}</div>}
                    <div className="text-xs opacity-60 mt-1">
                      {new Date(n.created_at).toLocaleString()}
                    </div>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    {n.task_id && (
                      <Link
                        to={`/tasks/${n.task_id}`}
                        // Opening it is reading it.
                        onClick={() => { if (n.unread) markRead(n.id).catch(() => {}) }}
                        className="text-xs underline opacity-80 hover:opacity-100"
                      >
                        Open
                      </Link>
                    )}
                    {n.unread && (
                      <button
                        className="text-xs underline opacity-80 hover:opacity-100"
                        onClick={() => markRead(n.id)}
                      >
                        Read
                      </button>
                    )}
                    <button
                      className="text-xs underline opacity-80 hover:opacity-100"
                      onClick={() => remove(n.id)}
                      title="Delete this notification"
                    >
                      Delete
                    </button>
                  </div>
                </div>
              </li>
            ))}
          </ul>
          {hasMore && (
            <button
              className="mt-3 w-full rounded-md border border-white/20 py-1.5 text-xs hover:border-white/40 disabled:opacity-50"
              onClick={loadMore}
              disabled={loadingMore}
            >
              {loadingMore ? 'Loading…' : 'Load more'}
            </button>
          )}
        </div>
      )}
    </div>
  )
}
