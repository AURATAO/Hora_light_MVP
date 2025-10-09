// app/src/auth/useRequireAuth.ts
import { useEffect, useState } from 'react'
import { supabase } from '../lib/supabaseClient'
import { useNavigate, useLocation } from 'react-router-dom'

export function useRequireAuth({ redirectTo = '/login', require = true } = {}) {
  const [user, setUser] = useState(null)
  const [loading, setLoading] = useState(true)
  const nav = useNavigate()
  const loc = useLocation()

  useEffect(() => {
    let mounted = true

    // 先拿一次快照
    supabase.auth.getSession().then(({ data: { session } }) => {
      if (!mounted) return
      setUser(session?.user ?? null)
      setLoading(false)
    })

    // 之後靠訂閱更新
    const { data: sub } = supabase.auth.onAuthStateChange((_e, session) => {
      if (!mounted) return
      setUser(session?.user ?? null)
    })

    return () => {
      mounted = false
      sub.subscription.unsubscribe()
    }
  }, [])

  // 只有在「不是 loading + require=true + 沒 user」時才導走
  useEffect(() => {
    if (!loading && require && !user) {
      nav(redirectTo + `?next=${encodeURIComponent(loc.pathname + loc.search)}`, { replace: true })
    }
  }, [loading, require, user, redirectTo, nav, loc])

  return { user, loading }
}