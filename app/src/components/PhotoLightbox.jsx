import { useEffect } from 'react'

/**
 * A full-screen view of one photo — the receipt behind a reimbursement, the
 * proof-of-work behind a completion.
 *
 * Deliberately minimal: the whole overlay closes it (click, tap, Escape),
 * there is no zoom or gallery, and the image is shown at its own aspect
 * ratio. The thumbnails on the task page are cropped; this is where the
 * requester actually reads the receipt.
 */
export default function PhotoLightbox({ src, alt, onClose }) {
  useEffect(() => {
    if (!src) return undefined
    const onKey = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [src, onClose])

  if (!src) return null
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={alt}
      onClick={onClose}
      className="fixed inset-0 z-[400] flex items-center justify-center bg-black/90 p-4 cursor-zoom-out"
    >
      <img src={src} alt={alt} className="max-h-full max-w-full object-contain rounded-lg" />
      <button
        type="button"
        onClick={onClose}
        aria-label="Close"
        className="absolute top-4 right-4 h-10 w-10 rounded-full bg-white/10 text-white text-lg hover:bg-white/20"
      >
        ✕
      </button>
    </div>
  )
}
