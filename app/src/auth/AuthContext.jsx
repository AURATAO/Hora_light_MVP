import React, { createContext, useContext, useEffect, useMemo, useState } from 'react'
import { supabase } from '../lib/supabaseClient'
import { AuthAPI } from '../api/client' // 內部會用 supabase.verifyOtp / signOut


const AuthCtx = createContext(null)

export function AuthProvider({ children }) {
  const [user, setUser] = useState(null)
  const [token, setToken] = useState(null) // 給既有程式相容
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let mounted = true

    // ① 啟動時恢復 session & user（這一步也能觸發 refresh）
    supabase.auth.getSession().then(({ data: { session } }) => {
      if (!mounted) return
      setUser(session?.user ?? null)
      setToken(session?.access_token ?? null)
      setLoading(false)
    })

    // ② 監聽登入 / 續期 / 登出
    const { data: sub } = supabase.auth.onAuthStateChange((_event, session) => {
      setUser(session?.user ?? null)
      setToken(session?.access_token ?? null)
    })

    // ③ 啟動自動續期（開發期間/HMR 有時需要手動叫起來最穩）
    supabase.auth.startAutoRefresh()

    // ④ 分頁回來時主動檢查（會在過期時自動換新 token）
    const onVisible = async () => {
      if (document.visibilityState === 'visible') {
        await supabase.auth.getSession()
      }
    }
    const onFocus = async () => {
      await supabase.auth.getSession()
    }
    window.addEventListener('visibilitychange', onVisible)
    window.addEventListener('focus', onFocus)

    return () => {
      mounted = false
      sub.subscription.unsubscribe()
      window.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('focus', onFocus)
      supabase.auth.stopAutoRefresh()
    }
  }, [])

  // 以「email + 6 碼」登入（verifyOtp 成功後，onAuthStateChange 會更新 user/token）
  async function loginWithOtp(email, code) {
    setLoading(true)
    try {
      const data = await AuthAPI.verifyOtp(email, code)
      // 保險：馬上同步一次 session（避免少數瀏覽器延遲）
      const { data: s } = await supabase.auth.getSession()
      setUser(s.session?.user ?? null)
      setToken(s.session?.access_token ?? null)
      return data
    } finally {
      setLoading(false)
    }
  }

  // 登出（注意：不要清整個 localStorage，以免清掉 Supabase 的 session 儲存）
  async function logout() {
    await AuthAPI.signOut()
    setUser(null)
    setToken(null)
  }

  const value = useMemo(
    () => ({ user, token, loading, loginWithOtp, logout, setUser }),
    [user, token, loading]
  )

  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthCtx)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}