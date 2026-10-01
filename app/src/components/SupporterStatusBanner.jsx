import { Clock, XCircle } from 'lucide-react'
import { supportMailto } from '../lib/support'

export default function SupporterStatusBanner({ status, onApply }) {
  if (status === 'approved') return null

  if (status === 'applied') {
    return (
      <div className="rounded-2xl border border-amber-500/30 bg-amber-500/5 p-6 flex flex-col gap-3 text-center items-center">
        <Clock size={28} className="text-amber-400" />
        <div className="space-y-1">
          <h2 className="font-heading text-xl text-white">Application under review</h2>
          <p className="text-sm text-white/60 max-w-sm">
            We've received your application and are reviewing it. This usually takes 1–3 business days.
          </p>
        </div>
      </div>
    )
  }

  if (status === 'rejected') {
    return (
      <div className="rounded-2xl border border-red-500/30 bg-red-500/5 p-6 flex flex-col gap-3 text-center items-center">
        <XCircle size={28} className="text-red-400" />
        <div className="space-y-1">
          <h2 className="font-heading text-xl text-white">Application not approved</h2>
          <p className="text-sm text-white/60 max-w-sm">
            We couldn't approve your application this time. Get in touch if you'd like to know more or
            want to apply again.
          </p>
        </div>
        <a
          href={supportMailto()}
          className="rounded-xl px-6 py-2.5 text-sm font-secondary font-semibold text-white border border-red-500/40 transition-all hover:brightness-110"
        >
          Contact support
        </a>
      </div>
    )
  }

  // status === 'none' (default)
  return (
    <div className="rounded-2xl border border-white/10 bg-surface p-6 flex flex-col gap-4 text-center items-center">
      <div className="space-y-1">
        <h2 className="font-heading text-xl text-white">Become a verified supporter</h2>
        <p className="text-sm text-white/60 max-w-sm">
          To accept tasks on HO:RA, complete a quick background check.
        </p>
      </div>
      <button
        onClick={onApply}
        className="rounded-xl px-6 py-2.5 text-sm font-secondary font-semibold text-white transition-all hover:brightness-110"
        style={{ backgroundColor: '#9aab3a' }}
      >
        Apply now
      </button>
    </div>
  )
}
