import { useEffect, useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { api } from '../api/client'
import { useAuth } from '../auth/AuthContext'

/**
 * Checks if the logged-in user has completed their profile
 * (phone + avatar_url both filled).
 *
 * Uses the backend API (cookie auth) so it works in all auth modes.
 * Never redirects if already on /profile.
 *
 * Returns { checking } — while true, caller should suppress render
 * to avoid flashing the wrong page.
 */
export function useProfileGate() {
  const { user } = useAuth()
  const navigate = useNavigate()
  const loc = useLocation()
  const [checking, setChecking] = useState(true)

  useEffect(() => {
    if (!user?.id) {
      setChecking(false)
      return
    }

    let alive = true
    async function check() {
      try {
        const profile = await api('/profile')
        if (!alive) return

        const complete =
          !!(profile?.phone?.trim()) && !!(profile?.avatar_url?.trim())

        setChecking(false)

        if (!complete && loc.pathname !== '/profile') {
          navigate('/profile', { replace: true })
        }
      } catch {
        if (alive) setChecking(false)
      }
    }

    check()
    return () => { alive = false }
  // re-run when pathname changes so navigating away from /profile re-checks
  }, [user?.id, loc.pathname]) // eslint-disable-line react-hooks/exhaustive-deps

  return { checking }
}
