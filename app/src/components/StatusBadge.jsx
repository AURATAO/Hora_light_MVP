const STYLES = {
  open:        'bg-secondary/15 text-secondary border-secondary/30',
  accepted:    'bg-blue-400/15 text-blue-300 border-blue-400/30',
  completed:   'bg-white/10 text-white/50 border-white/20',
  cancelled:   'bg-red-400/10 text-red-400/60 border-red-400/20',
  in_progress: 'bg-amber-400/15 text-amber-300 border-amber-400/30',
  // Platform takedown, not a requester cancellation — same red family, its own label.
  removed:     'bg-red-400/10 text-red-400/60 border-red-400/20',
  // Stripe Phase 2a: a task row that exists only so a card hold can name it,
  // before the 3DS challenge completes. The server filters it out of every
  // list and feed, and mobile leaves it out of TaskStatus entirely — but the
  // requester CAN still open the detail page by id, and this badge is what
  // they see when they do.
  pending_payment: 'bg-amber-400/15 text-amber-300 border-amber-400/30',
}

const LABELS = {
  open:        'Open',
  accepted:    'Accepted',
  completed:   'Completed',
  cancelled:   'Cancelled',
  in_progress: 'In Progress',
  removed:     'Removed',
  pending_payment: 'Payment Pending',
}

// A status with no entry above must still not print its own slug at somebody:
// "PENDING_PAYMENT" is a database value, not a word. Humanise rather than pass
// through, so the next status added server-side degrades to "Some New State"
// instead of shouting an enum.
function humanise(status) {
  return String(status || '')
    .split('_')
    .filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join(' ')
}

export default function StatusBadge({ status }) {
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-secondary font-medium ${STYLES[status] || 'bg-white/10 text-white/50 border-white/20'}`}
    >
      {LABELS[status] || humanise(status)}
    </span>
  )
}
