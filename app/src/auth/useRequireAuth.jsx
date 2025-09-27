import { useEffect } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useAuth } from './AuthContext'

export function useRequireAuth() {
  const { user, loading } = useAuth()
  const nav = useNavigate()
  const loc = useLocation()

  useEffect(() => {
    if (!loading && !user) {
      nav('/login', { replace: true, state: { from: loc.pathname } })
    }
  }, [loading, user, nav, loc])

  return { user, loading }
}