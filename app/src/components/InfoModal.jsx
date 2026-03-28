import { useState, useRef, useEffect } from 'react'

/**
 * InfoModal — label with a "!" button that toggles an inline info panel.
 * Does NOT block the screen or lock scroll — tapping outside closes it.
 *
 * Props:
 * - label: string        — the field label text
 * - required?: boolean   — show red asterisk next to label
 * - title: string        — panel heading
 * - body: ReactNode      — panel explanation content
 * - children: ReactNode  — the field input(s) rendered below the label
 */
export default function InfoModal({ label, required, title, body, children }) {
  const [open, setOpen] = useState(false)
  const panelRef = useRef(null)
  const triggerRef = useRef(null)

  // Close when clicking outside
  useEffect(() => {
    if (!open) return
    function handle(e) {
      if (
        panelRef.current && !panelRef.current.contains(e.target) &&
        triggerRef.current && !triggerRef.current.contains(e.target)
      ) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handle)
    document.addEventListener('touchstart', handle)
    return () => {
      document.removeEventListener('mousedown', handle)
      document.removeEventListener('touchstart', handle)
    }
  }, [open])

  return (
    <div className="grid gap-1">
      <label className="text-sm flex items-center gap-1">
        {label}
        {required && <span className="text-red-500">*</span>}
        <button
          ref={triggerRef}
          type="button"
          onClick={() => setOpen(v => !v)}
          className="ml-1 w-4 h-4 rounded-full border border-white/40 text-[10px] leading-none flex items-center justify-center hover:border-white/70"
          aria-label={`Info about ${label}`}
          aria-expanded={open}
        >
          !
        </button>
      </label>

      {open && (
        <div
          ref={panelRef}
          className="rounded-xl border border-white/20 bg-[#1a1a2e] px-4 py-3 text-sm text-white/80 space-y-2"
        >
          {title && <p className="font-semibold text-white">{title}</p>}
          {body}
          <div className="pt-1">
            <button
              type="button"
              onClick={() => setOpen(false)}
              className="text-xs text-white/50 hover:text-white/80 underline"
            >
              Close
            </button>
          </div>
        </div>
      )}

      {children}
    </div>
  )
}
