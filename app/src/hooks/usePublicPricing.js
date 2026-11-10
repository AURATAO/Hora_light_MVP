import { useEffect, useState } from 'react'
import { API_BASE } from '../api/client'

// GET /pricing (server/pricing.go): the live price schedule, public. Read
// once per page view by the screens that quote a price in words; null until
// it arrives or when it can't be read, and the copy then falls back to the
// schedule the build shipped with (lib/traction.js pricingLineFor).
export function usePublicPricing() {
  const [pricing, setPricing] = useState(null)
  useEffect(() => {
    let alive = true
    fetch(`${API_BASE}/pricing`)
      .then(r => (r.ok ? r.json() : null))
      .then(p => { if (alive) setPricing(p) })
      .catch(() => {})
    return () => { alive = false }
  }, [])
  return pricing
}
