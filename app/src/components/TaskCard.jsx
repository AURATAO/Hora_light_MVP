// app/src/components/TaskCard.jsx
import { useNavigate } from 'react-router-dom'
import CancelTaskButton from './CancelTaskButton'
import StatusBadge from './StatusBadge'

export default function TaskCard({ task, variant, onAccept, className = '', onAfterChange }) {
  const navigate = useNavigate()
  const whenText = task.is_immediate
    ? 'ASAP'
    : (task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : '—')
  const advance = (task.prepay_amount_cents || 0) / 100

  function openDetail() {
    navigate(`/tasks/${task.id}`)
  }
  function onKey(e) {
    // 只有當焦點在「卡片容器本身」時才處理快捷鍵
    if (e.target !== e.currentTarget) return
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
      className={`group cursor-pointer border border-white/15 rounded-lg p-3 bg-white/5 hover:border-white/30 hover:bg-white/8 transition-colors ${className}`}
      aria-label={`Open task ${task.title}`}
    >
      <div className="flex items-start gap-3">
        <div className="flex-1">
          <div className="font-medium underline-offset-2 group-hover:underline">{task.title}</div>
          <div className="text-xs text-white/70">
            {whenText}<span className="mx-2">•</span>{task.estimated_minutes} min
            {advance > 0 && <><span className="mx-2">•</span>Advance ${advance.toFixed(2)}</>}
          </div>
          <div className="mt-1.5 flex items-center gap-1.5 flex-wrap">
            <StatusBadge status={task.status} />
            {task.category && (
              <span className="text-xs px-2 py-0.5 rounded-full bg-white/10 text-white/60 border border-white/10">
                {task.category.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase())}
              </span>
            )}
          </div>
        </div>

        {/* 右上角動作 */}
        {variant === 'available' && !task.assigned_to_id && task.status === 'open' && (
          <div className="flex items-center gap-2">
            <button
              onClick={(e) => { e.stopPropagation(); onAccept?.(task.id) }}
              className="rounded-full text-white px-3 py-1 text-xs font-semibold transition-all hover:brightness-110"
              style={{ backgroundColor: '#9aab3a' }}
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
          <div className="flex items-center gap-2">
            <button
              onClick={(e) => { e.stopPropagation(); openDetail() }}
              className="rounded-md border border-white/20 px-2 py-1 text-xs hover:border-white/40"
            >
              Manage
            </button>

            {task.status === 'open' && (
              <div onClick={(e) => e.stopPropagation()}>
                <CancelTaskButton taskId={task.id} onDone={onAfterChange} />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}