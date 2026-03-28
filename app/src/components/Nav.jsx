import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useEffect, useRef, useState } from 'react'
import { useAuth } from '../auth/AuthContext.jsx'
import { api } from '../api/client'

export default function Nav() {
  const { user, logout } = useAuth()
  const nav = useNavigate()
  const loc = useLocation()

  const hideOnLogin = loc.pathname.endsWith('/login')
  const authed = !!user

  const [hasUnread, setHasUnread] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const pollRef = useRef(null)
  const menuRef = useRef(null)

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

  useEffect(() => {
    const onUnread = (e) => setHasUnread(!!e.detail?.has)
    window.addEventListener('notif:unread', onUnread)
    return () => window.removeEventListener('notif:unread', onUnread)
  }, [])

  // Close menu when clicking outside
  useEffect(() => {
    if (!menuOpen) return
    function handle(e) {
      if (menuRef.current && !menuRef.current.contains(e.target)) {
        setMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', handle)
    document.addEventListener('touchstart', handle)
    return () => {
      document.removeEventListener('mousedown', handle)
      document.removeEventListener('touchstart', handle)
    }
  }, [menuOpen])

  // Close menu on route change
  useEffect(() => { setMenuOpen(false) }, [loc.pathname])

  async function handleLogout() {
    setMenuOpen(false)
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
      <nav className="max-w-4xl m-auto items-center px-4 py-3 flex gap-3">
        {/* Logo */}
        <Link to="/" className="flex items-center">
          <img src={`${import.meta.env.BASE_URL}Logo.svg`} className="h-7 w-30" alt="Hora" />
        </Link>

        <div className="flex items-center gap-3 ml-auto">
          {/* Post a Task — always visible */}
          <Link
            to="/tasks/new"
            className="rounded-full border border-secondary/50 text-secondary px-3 py-1 text-sm hover:bg-secondary/10 transition-colors"
          >
            Post a Task
          </Link>

          {!authed && (
            <Link to="/login" className="text-sm opacity-80 hover:opacity-100 transition-opacity">
              Login
            </Link>
          )}

          {authed && (
            <>
              {/* Desktop: show links directly */}
              <div className="hidden sm:flex items-center gap-4">
                <div className="relative">
                  <Link
                    to="/my"
                    onClick={(e) => {
                      if (loc.pathname === '/my') {
                        e.preventDefault()
                        nav('/my', { state: { refreshAt: Date.now() } })
                        window.dispatchEvent(new CustomEvent('notif:unread', { detail: { has: false } }))
                      }
                    }}
                    className="text-sm opacity-80 hover:opacity-100 transition-opacity"
                  >
                    Dashboard
                  </Link>
                  {hasUnread && (
                    <span className="absolute -top-1 -right-2 block h-2.5 w-2.5 rounded-full bg-red-500" aria-label="Unread notifications" />
                  )}
                </div>
                <button onClick={handleLogout} className="text-sm opacity-80 hover:opacity-100 transition-opacity">
                  Logout
                </button>
              </div>

              {/* Mobile: hamburger */}
              <div className="relative sm:hidden" ref={menuRef}>
                <button
                  type="button"
                  onClick={() => setMenuOpen(v => !v)}
                  className="flex flex-col gap-1.5 justify-center items-center w-8 h-8 rounded-md hover:bg-white/10 transition-colors"
                  aria-label="Menu"
                >
                  <span className={`block h-0.5 w-5 bg-accent transition-transform duration-200 ${menuOpen ? 'translate-y-2 rotate-45' : ''}`} />
                  <span className={`block h-0.5 w-5 bg-accent transition-opacity duration-200 ${menuOpen ? 'opacity-0' : ''}`} />
                  <span className={`block h-0.5 w-5 bg-accent transition-transform duration-200 ${menuOpen ? '-translate-y-2 -rotate-45' : ''}`} />
                </button>

                {menuOpen && (
                  <div className="absolute right-0 top-full mt-2 w-48 rounded-xl border border-white/10 bg-surface shadow-xl overflow-hidden">
                    <Link
                      to="/my"
                      onClick={(e) => {
                        if (loc.pathname === '/my') {
                          e.preventDefault()
                          nav('/my', { state: { refreshAt: Date.now() } })
                          window.dispatchEvent(new CustomEvent('notif:unread', { detail: { has: false } }))
                        }
                        setMenuOpen(false)
                      }}
                      className="flex items-center justify-between px-4 py-3 text-sm text-accent hover:bg-white/5 transition-colors"
                    >
                      <span>Dashboard</span>
                      {hasUnread && (
                        <span className="block h-2 w-2 rounded-full bg-red-500" aria-label="Unread notifications" />
                      )}
                    </Link>
                    <div className="border-t border-white/10" />
                    <button
                      type="button"
                      onClick={handleLogout}
                      className="w-full text-left px-4 py-3 text-sm text-accent/70 hover:bg-white/5 hover:text-accent transition-colors"
                    >
                      Logout
                    </button>
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      </nav>
    </header>
  )
}
