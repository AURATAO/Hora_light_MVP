const STYLES = {
  open:        'bg-secondary/15 text-secondary border-secondary/30',
  accepted:    'bg-blue-400/15 text-blue-300 border-blue-400/30',
  completed:   'bg-white/10 text-white/50 border-white/20',
  cancelled:   'bg-red-400/10 text-red-400/60 border-red-400/20',
  in_progress: 'bg-amber-400/15 text-amber-300 border-amber-400/30',
  // Platform takedown, not a requester cancellation — same red family, its own label.
  removed:     'bg-red-400/10 text-red-400/60 border-red-400/20',
}

const LABELS = {
  open:        'Open',
  accepted:    'Accepted',
  completed:   'Completed',
  cancelled:   'Cancelled',
  in_progress: 'In Progress',
  removed:     'Removed',
}

export default function StatusBadge({ status }) {
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-secondary font-medium ${STYLES[status] || 'bg-white/10 text-white/50 border-white/20'}`}
    >
      {LABELS[status] || status}
    </span>
  )
}
