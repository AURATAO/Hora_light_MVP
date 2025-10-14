// src/auth/ProtectedLayout.jsx
import { Navigate, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from './AuthContext.jsx'

export default function ProtectedLayout() {
  const { user, loading } = useAuth()
  const loc = useLocation()
  console.log('[CTX] user=', user, 'loading=', loading)
  console.log('[PL] user=', user, 'loading=', loading)
  if (loading) return null
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
  return <Outlet />
}