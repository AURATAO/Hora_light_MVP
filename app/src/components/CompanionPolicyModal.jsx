import { useEffect, useState } from 'react'
import Modal from './Modal'

const COMP_POLICY_TEXT = `Companionship (Accompaniment) Policy

What it IS:
• Public-route accompaniment only (e.g., walking together in public areas, escorting someone to a nearby appointment, accompanying someone to a subway/bus stop).
• Non-medical, non-caregiving, and strictly for general presence/support in public.

What it is NOT:
• No medical or personal care (no medication handling, bathing, lifting, or physical assistance).
• No services involving minors.
• No intimate/sexual services, no dating positioning.
• No overnight stays.
• No staying inside a private residence.

Safety & boundaries:
• Keep the route in public places; either party can end the task anytime if uncomfortable.
• If a request involves restricted activities, it must be declined and reported to the platform.
`

/**
 * The companionship policy and its acknowledgement. Shared by Post Task and
 * the task edit form so both ask in the same words; when to ask is the
 * caller's (lib/companionship.js needsCompanionPolicy).
 *
 * The checkbox starts unticked every time the modal opens.
 */
export default function CompanionPolicyModal({ open, onConfirm, onCancel }) {
  const [checked, setChecked] = useState(false)
  useEffect(() => {
    if (open) setChecked(false)
  }, [open])

  return (
    <Modal
      open={open}
      onClose={onCancel}
      title="Companionship Policy"
      actions={
        <>
          <button
            className="rounded-md px-4 py-2 border border-white/20 hover:border-white/40"
            onClick={onCancel}
          >
            Cancel
          </button>
          <button
            className="rounded-md px-4 py-2 bg-white text-black disabled:opacity-50"
            disabled={!checked}
            onClick={onConfirm}
          >
            Confirm & Continue
          </button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="whitespace-pre-wrap rounded-md border border-white/20 p-3 max-h-[45vh] overflow-auto">
          {COMP_POLICY_TEXT}
        </div>
        <label className="flex items-start gap-2 cursor-pointer">
          <input
            type="checkbox"
            checked={checked}
            onChange={(e) => setChecked(e.target.checked)}
            className="mt-1"
          />
          <span>I have read and agree to the companionship policy.</span>
        </label>
      </div>
    </Modal>
  )
}
