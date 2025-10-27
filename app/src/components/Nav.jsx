import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useEffect, useRef, useState } from 'react'
import { useAuth } from '../auth/AuthContext.jsx'
import { api } from '../api/client' // ← 不再匯入 isAuthed / AuthAPI



export default function Nav() {
  const { user, logout } = useAuth()
  const nav = useNavigate()
  const loc = useLocation()

  const hideOnLogin = loc.pathname.endsWith('/login')
  const authed = !!user

  const [hasUnread, setHasUnread] = useState(false)
  const pollRef = useRef(null)

  useEffect(() => {
    let mounted = true

    const stopPolling = () => {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
    }

    if (hideOnLogin || !authed) {
      stopPolling()
      setHasUnread(false)
      return
    }

    const check = async () => {
      try {
        const res = await api('/notifications?unread=true&limit=1')
        if (!mounted) return
        setHasUnread(Array.isArray(res) && res.length > 0)
      } catch { /* silent */ }
    }

    check()
    pollRef.current = setInterval(check, 30_000)

    const onVis = () => { if (document.visibilityState === 'visible') check() }
    document.addEventListener('visibilitychange', onVis)

    return () => {
      mounted = false
      document.removeEventListener('visibilitychange', onVis)
      stopPolling()
    }
  }, [authed, hideOnLogin, loc.key])

  async function handleLogout() {
    try {
      await logout()
    } finally {
      if (pollRef.current) {
        clearInterval(pollRef.current)
        pollRef.current = null
      }
      setHasUnread(false)
      nav('/login', { replace: true })
    }
  }

  if (hideOnLogin) return null

  return (
    <header className="sticky top-0 inset-x-0 z-50 bg-linear-to-br from-primary to-primary/50 text-accent font-secondary">
      <nav className="max-w-4xl m-auto justify-center items-center px-4 py-3 flex gap-4">
        <Link to="/" className="flex justify-center text-3xl">
          <img src={`${import.meta.env.BASE_URL}Logo.svg`} className="h-7 w-30" alt="Hora" />
        </Link>
        <div className="flex justify-center gap-4 ml-auto items-center">
          <Link to="/tasks/new">Post a Task</Link>

          <div className="relative">
            <Link to="/my">My</Link>
            {hasUnread && (
              <span
                className="absolute -top-1 -right-2 block h-2.5 w-2.5 rounded-full bg-red-500"
                aria-label="You have unread notifications"
              />
            )}
          </div>

          {authed ? (
            <button onClick={handleLogout}>Logout</button>
          ) : (
            <Link to="/login">Login</Link>
          )}
        </div>
      </nav>
    </header>
  )
}
