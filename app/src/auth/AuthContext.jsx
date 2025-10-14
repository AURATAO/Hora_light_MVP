// src/auth/AuthContext.jsx
import React, { createContext, useContext, useEffect, useMemo, useState } from 'react'
import { supabase } from '../lib/supabaseClient'
import { AuthAPI } from '../api/client'   // ← NEW


const MODE = import.meta.env.VITE_AUTH_MODE || 'cookie'
const AuthCtx = createContext(null)

export function AuthProvider({ children }) {
  const [user, setUser] = useState(null)
  const [token, setToken] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let live = true

    const initCookie = async () => {
      try {
        const me = await AuthAPI.me()          // ← 必打 /auth/me（會帶 credentials）
        if (!live) return
        setUser(me)                            // me 可能是 null 或 {id,email,name}
      } finally {
        if (live) setLoading(false)
      }
    }

    const initBearer = async () => {
      const { data: { session } } = await supabase.auth.getSession()
      if (!live) return
      setUser(session?.user ?? null)
      setToken(session?.access_token ?? null)
      setLoading(false)
      const { data: sub } = supabase.auth.onAuthStateChange((_event, s) => {
        setUser(s?.user ?? null)
        setToken(s?.access_token ?? null)
      })
      supabase.auth.startAutoRefresh()
      const onVisible = async () => { if (document.visibilityState === 'visible') await supabase.auth.getSession() }
      const onFocus = async () => { await supabase.auth.getSession() }
      window.addEventListener('visibilitychange', onVisible)
      window.addEventListener('focus', onFocus)
      return () => {
        sub.subscription.unsubscribe()
        window.removeEventListener('visibilitychange', onVisible)
        window.removeEventListener('focus', onFocus)
        supabase.auth.stopAutoRefresh()
      }
    }

    if (MODE === 'cookie') {
      initCookie()
      const onFocus = async () => {
        try { const me = await AuthAPI.me(); setUser(me) } catch { setUser(null) }
      }
      window.addEventListener('focus', onFocus)
      return () => { live = false; window.removeEventListener('focus', onFocus) }
    } else {
      const cleanup = initBearer()
      return () => { live = false; cleanup && cleanup() }
    }
  }, [])

  async function logout() {
    if (MODE !== 'cookie') await supabase.auth.signOut().catch(() => {})
    setUser(null); setToken(null)
  }

  const value = useMemo(() => ({ user, token, loading, logout, setUser }), [user, token, loading])
  console.log('[CTX] user=', user, 'loading=', loading)
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthCtx)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}