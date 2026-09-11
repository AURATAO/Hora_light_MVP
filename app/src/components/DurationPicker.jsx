import { useState } from 'react'

// One preset list, shared with mobile's QUICK_MINUTES (mobile/src/components/
// TaskForm.tsx). The two used to disagree — web offered 15/30/60/120, mobile
// 30/60/90/120 — so the same product suggested different durations depending
// on the device.
//
// 15 is gone. The first 15 minutes are now inside the base fee, so a "15 min"
// package is not a package at all: it is the floor, priced identically to a
// 1-minute task, and offering it as a choice implies a cheaper tier that does
// not exist.
//
// The category coupling is gone too. These presets used to set the task's
// category as a side effect (15 → quick_errand, everything else → standard),
// which silently overwrote the category the user had picked — and since
// category is half of what decides the base fee, a duration click could change
// the price of a companionship task. Duration is duration; the user chooses
// the category.
const PRESETS = [30, 60, 90, 120]

/**
 * DurationPicker
 *
 * Deliberately shows no prices. It used to print `minutes × $0.50` on each
 * tile, which was never the actual total (it omitted the base fee) and is now
 * doubly wrong (it omits the included 15 minutes). Fetching four real quotes
 * to decorate four buttons would be four round trips for a number the itemized
 * estimate summary directly below already shows, correctly and for the actual
 * task being posted. So: durations here, one authoritative price there.
 *
 * Props:
 *   value    – current estimated_minutes value
 *   onChange – (minutes: number) => void
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
          We charge based on actual time logged. The base fee covers the first 15 minutes;
          longer tasks add the per-minute rate from there. Finish early and you are not
          charged for time nobody worked.
        </div>
      )}

      {/* 2×2 preset grid */}
      <div className="grid grid-cols-2 gap-2 mb-3">
        {PRESETS.map((preset) => {
          const selected = Number(value) === preset
          const label = preset >= 60 && preset % 60 === 0
            ? `${preset / 60} hour${preset === 60 ? '' : 's'}`
            : `${preset} min`
          return (
            <button
              key={preset}
              type="button"
              onClick={() => onChange(preset)}
              className={`rounded-xl p-3 text-left border transition-all ${
                selected
                  ? 'border-[#9aab3a] bg-[#9aab3a]/10'
                  : 'border-white/10 bg-white/5 hover:border-white/20'
              }`}
            >
              <div className={`text-sm font-semibold ${selected ? 'text-[#9aab3a]' : 'text-white'}`}>
                {label}
              </div>
              <div className="text-xs text-white/50 mt-0.5">{preset} min</div>
            </button>
          )
        })}
      </div>

      {/* Info pills */}
      <div className="flex flex-wrap gap-2">
        <span className="inline-flex items-center px-2.5 py-1 rounded-full
                         bg-green-500/10 border border-green-500/20 text-xs text-green-400">
          Finish early → unused time not charged
        </span>
        <span className="inline-flex items-center px-2.5 py-1 rounded-full
                         bg-amber-500/10 border border-amber-500/20 text-xs text-amber-400">
          Need more time → extra time charged
        </span>
      </div>
    </div>
  )
}
