import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useNotifications } from '../hooks/useNotifications'

function timeAgo(dateStr) {
  const diff = Math.floor((Date.now() - new Date(dateStr)) / 1000)
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

export default function NotificationFeed({ limit = 50, className = '' }) {
  const [showAll, setShowAll] = useState(false)
  const {
    items, setItems, loading, fetchList,
    markRead, markAllRead,
    remove, clearRead,
  } = useNotifications()

   useEffect(() => {
    (async () => {
      try {
        await markAllRead();               // 進到 /my 就全數設已讀（會廣播 notif:unread=false）
      } finally {
        const data = await fetchList({ limit });
        setItems(data);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [limit]);

  const hasAny = items.length > 0
  const hasRead = items.some(n => !n.unread)

  if (loading || !hasAny) return null

  const mostRecent = items.find(n => n.unread) ?? items[0]

  return (
    <div className={className}>
      {/* Compact notification bar */}
      <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-white/5 border border-white/15">
        <span className="w-2 h-2 rounded-full bg-red-500 shrink-0" />
        <span className="text-sm flex-1 truncate min-w-0">{mostRecent.title}</span>
        <span className="text-xs opacity-60 shrink-0 whitespace-nowrap">{timeAgo(mostRecent.created_at)}</span>
        <button
          className="text-xs underline opacity-80 hover:opacity-100 shrink-0 whitespace-nowrap"
          onClick={() => setShowAll(v => !v)}
        >
          {showAll ? 'Close' : `See all (${items.length})`}
        </button>
      </div>

      {/* Expanded full list */}
      {showAll && (
        <div className="mt-2 border border-white/20 rounded-lg p-3 bg-white/5">
          <div className="flex items-center gap-2 mb-2">
            <div className="font-semibold text-sm">Notifications</div>
            <div className="ml-auto flex items-center gap-3">
              {hasAny && (
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
        </div>
      )}
    </div>
  )
}
