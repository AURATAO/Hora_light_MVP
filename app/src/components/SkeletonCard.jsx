export default function SkeletonCard({ count = 3 }) {
  return (
    <ul className="space-y-2">
      {Array.from({ length: count }).map((_, i) => (
        <li
          key={i}
          className="border border-white/15 rounded-lg p-3 bg-white/5 animate-pulse"
        >
          <div className="flex items-start gap-3">
            <div className="flex-1 space-y-2">
              <div className="h-4 rounded bg-white/20 w-3/5" />
              <div className="h-3 rounded bg-white/10 w-2/5" />
            </div>
            <div className="h-6 w-16 rounded-md bg-white/10" />
          </div>
        </li>
      ))}
    </ul>
  )
}
