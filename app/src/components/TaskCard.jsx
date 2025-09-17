import { Link } from 'react-router-dom'

export default function TaskCard({ task, variant, onAccept, className = '' }) {
  const whenText = task.is_immediate
    ? 'ASAP'
    : (task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : '—')
  const advance = (task.prepay_amount_cents || 0) / 100

  return (
    <div className={`border border-white/15 rounded-lg p-3 bg-white/5 ${className}`}>
      <div className="flex items-start gap-3">
        <div className="flex-1">
          <Link to={`/tasks/${task.id}`} className="font-medium hover:underline">
            {task.title}
          </Link>
          <div className="text-xs text-white/70">
            {whenText}<span className="mx-2">•</span>{task.estimated_minutes} min
            {advance > 0 && <><span className="mx-2">•</span>Advance {advance.toFixed(2)} EUR</>}
          </div>
        </div>

        {/* 右側動作：依分頁 variant 切換 */}
        {variant === 'available' && !task.assigned_to && (
          <button
            onClick={() => onAccept?.(task.id)}
            className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
          >
            Accept
          </button>
        )}
        {variant === 'assigned' && (
          <Link
            to={`/tasks/${task.id}`}
            className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
          >
            Open
          </Link>
        )}
        {variant === 'posted' && (
          <Link
            to={`/tasks/${task.id}`}
            className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
          >
            Manage
          </Link>
        )}
      </div>
    </div>
  )
}