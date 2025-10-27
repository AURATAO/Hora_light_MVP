import { useEffect } from 'react'
import { Link } from 'react-router-dom'
import { useNotifications } from '../hooks/useNotifications'

export default function NotificationFeed({ limit = 50, className = '' }) {
  const {
    items, setItems, loading, fetchList,
    markRead, markAllRead,
    remove, clearRead,             // ← 新增
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

  return (
    <div className={`border border-white/20 rounded-lg p-3 bg-white/5 ${className}`}>
      <div className="flex items-center gap-2 mb-2">
        <div className="font-semibold">Notifications</div>
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

      {loading ? (
        <div className="text-white/70">Loading…</div>
      ) : !hasAny ? (
        <div className="text-white/70">No notifications yet.</div>
      ) : (
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
                  {/* ✅ 新增：刪除單筆 */}
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
      )}
    </div>
  )
}