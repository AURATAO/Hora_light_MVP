import { useState } from 'react'

const PRESETS = [
  { label: '15 min',  minutes: 15,  category: 'quick_errand' },
  { label: '30 min',  minutes: 30,  category: 'standard' },
  { label: '1 hour',  minutes: 60,  category: 'standard' },
  { label: '2 hours', minutes: 120, category: 'standard' },
]

/**
 * DurationPicker
 * Props:
 *   value    – current estimated_minutes value
 *   onChange – (minutes: number, category: string) => void
 */
export default function DurationPicker({ value, onChange }) {
  const [showTooltip, setShowTooltip] = useState(false)

  return (
    <div>
      {/* Label + info button */}
      <div className="flex items-center gap-2 mb-3">
        <span className="text-sm text-white/70">Expected duration <span className="text-red-500">*</span></span>
        <button
          type="button"
          onClick={() => setShowTooltip(v => !v)}
          className="w-5 h-5 rounded-full border border-white/30 text-xs text-white/50
                     hover:border-white/60 hover:text-white/80 transition-colors
                     flex items-center justify-center shrink-0"
        >
          ?
        </button>
      </div>

      {/* Tooltip */}
      {showTooltip && (
        <div className="mb-3 p-3 rounded-lg bg-white/5 border border-white/10 text-sm text-white/60 leading-relaxed">
          We charge based on actual time logged. Finish early? Unused time is refunded.
          Need more time? Extra time is charged at the same hourly rate.
        </div>
      )}

      {/* 2×2 preset grid */}
      <div className="grid grid-cols-2 gap-2 mb-3">
        {PRESETS.map((preset) => {
          const selected = value === preset.minutes
          return (
            <button
              key={preset.category}
              type="button"
              onClick={() => onChange(preset.minutes, preset.category)}
              className={`rounded-xl p-3 text-left border transition-all ${
                selected
                  ? 'border-[#9aab3a] bg-[#9aab3a]/10'
                  : 'border-white/10 bg-white/5 hover:border-white/20'
              }`}
            >
              <div className={`text-sm font-semibold ${selected ? 'text-[#9aab3a]' : 'text-white'}`}>
                {preset.label}
              </div>
              <div className="text-xs text-white/50 mt-0.5">{preset.minutes} min</div>
              <div className="text-xs text-[#9aab3a] mt-1">${(preset.minutes * 0.50).toFixed(2)}</div>
            </button>
          )
        })}
      </div>

      {/* Info pills */}
      <div className="flex flex-wrap gap-2">
        <span className="inline-flex items-center px-2.5 py-1 rounded-full
                         bg-green-500/10 border border-green-500/20 text-xs text-green-400">
          Finish early → unused time refunded
        </span>
        <span className="inline-flex items-center px-2.5 py-1 rounded-full
                         bg-amber-500/10 border border-amber-500/20 text-xs text-amber-400">
          Need more time → extra time charged
        </span>
      </div>
    </div>
  )
}
