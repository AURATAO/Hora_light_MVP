import { useState } from 'react'
import Modal from './Modal'

/**
 * InfoModal — wraps a field label with an "!" button that opens an info modal.
 *
 * Props:
 * - label: string        — the field label text
 * - required?: boolean   — show red asterisk next to label
 * - title: string        — modal heading
 * - body: string         — modal explanation text
 * - children: ReactNode  — the field input(s) rendered below the label
 */
export default function InfoModal({ label, required, title, body, children }) {
  const [open, setOpen] = useState(false)

  return (
    <div className="grid gap-1">
      <label className="text-sm flex items-center gap-1">
        {label}
        {required && <span className="text-red-500">*</span>}
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="ml-1 w-4 h-4 rounded-full border border-white/40 text-[10px] leading-none flex items-center justify-center hover:border-white/70"
          aria-label={`Info about ${label}`}
        >
          !
        </button>
      </label>

      {children}

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title={title}
        actions={
          <button
            className="rounded-md px-4 py-2 bg-white text-black"
            onClick={() => setOpen(false)}
          >
            Got it
          </button>
        }
      >
        {body}
      </Modal>
    </div>
  )
}
