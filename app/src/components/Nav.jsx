import { Link, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext.jsx'
import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'

export default function Nav() {
  const { user, logout } = useAuth()
  const loc = useLocation()
  const hideOnLogin = loc.pathname.endsWith('/login')

  const [hasUnread, setHasUnread] = useState(false)
  const pollRef = useRef(null)

  useEffect(() => {
    if (!user) {
      setHasUnread(false)
      if (pollRef.current) clearInterval(pollRef.current)
      return
    }

    const check = async () => {
      try {
        const res = await api('/notifications?unread=true&limit=1')
        setHasUnread(Array.isArray(res) && res.length > 0)
      } catch {
        // 靜默失敗即可
      }
    }

    // 先查一次，之後每 30 秒查一次；回到前景也查
    check()
    pollRef.current = setInterval(check, 30_000)
    const onVis = () => { if (document.visibilityState === 'visible') check() }
    document.addEventListener('visibilitychange', onVis)

    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [user])

  if (hideOnLogin) return null

  return (
    <header className='sticky top-0 inset-x-0 z-50 bg-gradient-to-br from-primary to-primary/50 text-accent font-secondary'>
      <nav className='max-w-4xl m-auto justify-center items-center px-4 py-3 flex gap-4'>
        <Link to="/" className='flex justify-center text-3xl'>
          <img src="/app/Logo.svg" className='h-7 w-30' alt="Hora" />
        </Link>
        <div className='flex justify-center gap-4 ml-auto items-center'>
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

          {user ? (
            <button onClick={logout}>Logout</button>
          ) : (
            <Link to="/login">Login</Link>
          )}
        </div>
      </nav>
    </header>
  )
}