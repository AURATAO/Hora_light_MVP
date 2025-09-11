import React, { createContext, useContext, useEffect, useMemo, useState } from 'react'
import { AuthAPI } from '../api/client.js'


const AuthCtx = createContext(null)


export function AuthProvider({ children }) {
    const [token, setToken] = useState(() => localStorage.getItem('hora_token'))
    const [user, setUser] = useState(() => {
    const raw = localStorage.getItem('hora_user')
    return raw ? JSON.parse(raw) : null
    })
    const [loading, setLoading] = useState(false)


    useEffect(() => {
        if (!token) return
        // Optional: refresh user profile on mount
        AuthAPI.me().then(setUser).catch(() => {}).finally(() => {})
    }, [token])


    function saveSession({ token, user }) {
        if (token) {
            localStorage.setItem('hora_token', token)
            setToken(token)
        }
        if (user) {
            localStorage.setItem('hora_user', JSON.stringify(user))
            setUser(user)
        }
    }
    async function loginWithOtp(email, code) {
        setLoading(true)
        try {
            const data = await AuthAPI.verifyOtp(email, code)
         // expected shape: { token: '...', user: { id, email, name } }
            saveSession(data)
            return data
        } finally {
            setLoading(false)
        }
    }


    function logout() {
        localStorage.removeItem('hora_token')
        localStorage.removeItem('hora_user')
        setToken(null)
        setUser(null)
    }


    const value = useMemo(() => ({ token, user, loading, loginWithOtp, logout, setUser }), [token, user, loading])


    return <AuthCtx.Provider value={value}>{children}</AuthCtx.Provider>
    }


    export function useAuth() {
        const ctx = useContext(AuthCtx)
        if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
        return ctx
}

