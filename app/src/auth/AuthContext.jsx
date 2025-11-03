// // src/auth/AuthContext.jsx
// import React, { createContext, useContext, useEffect, useMemo, useState } from 'react'
// import { supabase } from '../lib/supabaseClient'
// import { AuthAPI } from '../api/client'

// const MODE = import.meta.env.VITE_AUTH_MODE || 'cookie'
// const AuthCtx = createContext(null)

// export function AuthProvider({ children }) {
//   const [user, setUser] = useState(null)
//   const [token, setToken] = useState(null)
//   const [loading, setLoading] = useState(true)

//   useEffect(() => {
//     let live = true

//     // Cookie 模式：打 /auth/me 拿使用者；聚焦時再驗一次
//     const initCookie = async () => {
//       try {
//         const me = await AuthAPI.me()
//         if (!live) return
//         setUser(me)
//       } finally {
//         if (live) setLoading(false)
//       }
//     }

//     // Bearer 模式：不用 async，回傳同步 cleanup（給 useEffect）
//     function initBearer() {
//       // 初始抓一次 session
//       supabase.auth.getSession().then(({ data: { session } }) => {
//         if (!live) return
//         setUser(session?.user ?? null)
//         setToken(session?.access_token ?? null)
//         setLoading(false)
//       })

//       // 監聽登入狀態
//       const { data: sub } = supabase.auth.onAuthStateChange((_event, s) => {
//         setUser(s?.user ?? null)
//         setToken(s?.access_token ?? null)
//       })

//       // 自動刷新 token
//       supabase.auth.startAutoRefresh()

//       // 視窗回來時刷新一次
//       const onVisible = async () => {
//         if (document.visibilityState === 'visible') await supabase.auth.getSession()
//       }
//       const onFocus = async () => { await supabase.auth.getSession() }
//       window.addEventListener('visibilitychange', onVisible)
//       window.addEventListener('focus', onFocus)

//       // ★ 同步回傳清理函式
//       return () => {
//         sub?.subscription?.unsubscribe?.()
//         window.removeEventListener('visibilitychange', onVisible)
//         window.removeEventListener('focus', onFocus)
//         supabase.auth.stopAutoRefresh()
//       }
//     }

//     if (MODE === 'cookie') {
//       initCookie()

//       // 視窗聚焦時，重新確認 /auth/me
//       const onFocus = async () => {
//         try {
//           const me = await AuthAPI.me()
//           setUser(me)
//         } catch {
//           setUser(null)
//         }
//       }
//       window.addEventListener('focus', onFocus)

//       // 監聽 Supabase OTP 完成事件（讓 /auth/me 能用 Bearer 成功）
//        const { data: sub } = supabase.auth.onAuthStateChange(async (_event, _session) => {
//           try {
//             const me = await AuthAPI.me()
//             setUser(me)
//           } catch {
//             setUser(null)
//           }
//         })


//       return () => {
//         live = false
//         window.removeEventListener('focus', onFocus)
//         sub?.subscription?.unsubscribe?.()
//       }
//     } else {
//       const cleanup = initBearer()
//       return () => { live = false; cleanup && cleanup() }
//     }
//   }, [])

//   // 永遠用 AuthAPI.logout(): 清 server cookie + supabase
//   async function logout() {
//     try {
//       await AuthAPI.logout()
//     } finally {
//       setUser(null)
//       setToken(null)
//     }
//   }

//   const value = useMemo(
//     () => ({ user, token, loading, logout, setUser }),
//     [user, token, loading, logout]
//   )

//   return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>
// }

// export function useAuth() {
//   const ctx = useContext(AuthCtx)
//   if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
//   return ctx
// }
import { createContext, useContext, useEffect, useMemo, useState } from "react";
import { AuthAPI } from "../api/client";

const AuthCtx = createContext(null);

export function AuthProvider({ children }) {
  const [user, setUser] = useState(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let live = true;

    // 兜底：就算 /auth/me 掛住也結束 loading，避免整站卡住
    const safety = setTimeout(() => {
      if (!live) return;
      console.warn("[Auth] safety timeout → loading=false");
      setLoading(false);
    }, 5000);

    (async () => {
      try {
        console.log("[Auth] calling /auth/me …");
        const me = await AuthAPI.me(); // 內建逾時/錯誤處理
        if (!live) return;
        console.log("[Auth] /auth/me result:", me);
        setUser(me); // me 可能為 null（未登入）
      } catch (e) {
        console.warn("[Auth] /auth/me failed:", e);
        if (live) setUser(null);
      } finally {
        if (live) setLoading(false);
        clearTimeout(safety);
      }
    })();

    // 視窗聚焦時再補拉一次（session 更新、OTP 完成後可同步）
    const onFocus = async () => {
      try {
        const me = await AuthAPI.me();
        if (live) setUser(me);
      } catch {
        if (live) setUser(null);
      }
    };
    window.addEventListener("focus", onFocus);

    // 除錯：快速檢視目前狀態
    if (typeof window !== "undefined") {
      window.__auth = () => ({ user, loading });
    }

    return () => {
      live = false;
      clearTimeout(safety);
      window.removeEventListener("focus", onFocus);
    };
  }, []);

  async function logout() {
    await AuthAPI.logout().catch(() => {});
    setUser(null);
  }

  const value = useMemo(
    () => ({ user, loading, setUser, logout }),
    [user, loading]
  );

  console.log("[AuthProvider] render:", { user, loading });
  return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>;
}

export function useAuth() {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}