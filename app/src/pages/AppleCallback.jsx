// Where Supabase sends the browser back after Sign in with Apple
// (Login.jsx loginWithApple sets this page as redirectTo).
//
// Public route, outside ProtectLayout: the person is by definition not yet
// signed in when they land here. The page turns the ?code= in the URL into a
// Supabase session (PKCE — the verifier was written to localStorage by
// signInWithOAuth on the login page, so this must be the same browser), then
// does exactly what Login.jsx's verifyOtp does after a code is accepted:
// /auth/exchange for the hora_session cookie, /auth/me for the user, setUser,
// and on to the app. The consent tick was parked in localStorage by the login
// checkbox (lib/termsConsent) and is flushed by ProtectLayout once `user`
// is set — nothing to do for it here.
import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { supabase } from '../lib/supabaseClient.js'
import { AuthAPI } from '../api/client.js'
import { useAuth } from '../auth/AuthContext.jsx'

export default function AppleCallback() {
  const nav = useNavigate()
  const { setUser } = useAuth()
  const [error, setError] = useState(null)

  useEffect(() => {
    let alive = true
    ;(async () => {
      try {
        const params = new URLSearchParams(window.location.search)
        // Supabase reports a provider refusal in the query string rather than
        // handing over a code. Surface it instead of a bare "no session".
        const providerError = params.get('error_description') || params.get('error')
        if (providerError) throw new Error(providerError)

        // The client is built with detectSessionInUrl + pkce, and auth-js
        // (2.76) exchanges a `?code=` itself during client initialisation —
        // getSession() awaits that, so on a normal landing the session is
        // simply there and the code has already been spent. The explicit
        // exchange below is the fallback for when it was not (a client
        // built with detection off, or a code that survived the boot), not
        // the primary path: run first, it would fail on the consumed code.
        let { data: { session } } = await supabase.auth.getSession()
        const code = params.get('code')
        if (!session && code) {
          const { data, error: exchangeError } = await supabase.auth.exchangeCodeForSession(code)
          if (exchangeError) throw exchangeError
          session = data.session
        }
        if (!session?.access_token) throw new Error('Apple sign-in returned no session')

        await AuthAPI.exchangeToken(session.access_token)
        const me = await AuthAPI.me()
        if (!me) throw new Error('Could not load your account')
        if (!alive) return
        setUser(me)
        nav('/', { replace: true })
      } catch (e) {
        if (alive) setError(e.message || 'Apple sign-in failed')
      }
    })()
    return () => { alive = false }
  }, [nav, setUser])

  return (
    <div className="min-h-screen flex items-center justify-center px-4">
      <div className="w-full max-w-sm rounded-2xl border border-slate-200 bg-white/80 backdrop-blur p-6 shadow-sm text-center">
        {error ? (
          <>
            <p className="text-sm text-slate-900">Apple sign-in did not complete.</p>
            <p className="mt-2 text-sm text-slate-600 break-words">{error}</p>
            <Link to="/login" className="mt-4 inline-block text-sm underline underline-offset-4 text-slate-900">
              Back to sign in
            </Link>
          </>
        ) : (
          <p className="text-sm text-slate-600">Signing you in…</p>
        )}
      </div>
    </div>
  )
}
