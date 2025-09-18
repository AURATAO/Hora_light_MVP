// app/src/components/TaskCard.jsx
import { useNavigate } from 'react-router-dom'

export default function TaskCard({ task, variant, onAccept, className = '' }) {
  const navigate = useNavigate()
  const whenText = task.is_immediate
    ? 'ASAP'
    : (task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : '—')
  const advance = (task.prepay_amount_cents || 0) / 100

  function openDetail() {
    navigate(`/tasks/${task.id}`)
  }
  function onKey(e) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      openDetail()
    }
  }

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={openDetail}
      onKeyDown={onKey}
      className={`cursor-pointer border border-white/15 rounded-lg p-3 bg-white/5 hover:border-white/40 transition ${className}`}
      aria-label={`Open task ${task.title}`}
    >
      <div className="flex items-start gap-3">
        <div className="flex-1">
          <div className="font-medium underline-offset-2 group-hover:underline">{task.title}</div>
          <div className="text-xs text-white/70">
            {whenText}<span className="mx-2">•</span>{task.estimated_minutes} min
            {advance > 0 && <><span className="mx-2">•</span>Advance {advance.toFixed(2)} EUR</>}
          </div>
        </div>

        {/* 右上角動作 */}
        {variant === 'available' && !task.assigned_to && (
          <div className="flex items-center gap-2">
            {/* 可選：提供一顆 Open，但整卡已可點，其實不一定需要
            <button
              onClick={(e) => { e.stopPropagation(); openDetail() }}
              className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
            >
              Open
            </button> */}
            <button
              onClick={(e) => { e.stopPropagation(); onAccept?.(task.id) }}
              className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
            >
              Accept
            </button>
          </div>
        )}

        {variant === 'assigned' && (
          <button
            onClick={(e) => { e.stopPropagation(); openDetail() }}
            className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
          >
            Open
          </button>
        )}

        {variant === 'posted' && (
          <button
            onClick={(e) => { e.stopPropagation(); openDetail() }}
            className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
          >
            Manage
          </button>
        )}
      </div>
    </div>
  )
}