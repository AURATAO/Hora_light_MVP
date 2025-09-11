import { useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { AuthAPI } from '../api/client.js'
import { useAuth } from '../auth/AuthContext.jsx'


export default function Login() {
const [email, setEmail] = useState('')
const [step, setStep] = useState('email') // 'email' | 'code'
const [code, setCode] = useState('')
const [sending, setSending] = useState(false)
const { loginWithOtp } = useAuth()
const nav = useNavigate()
const loc = useLocation()
const from = loc.state?.from || '/'


async function handleSend() {
    if (!email) return
        setSending(true)
            try {
            await AuthAPI.requestOtp(email)
            setStep('code')
        } catch (e) {
            alert(e.message || 'Failed to send code')
        } finally {
            setSending(false)
    }
}



async function handleVerify(e) {
    e.preventDefault()
        if (!email || !code) return
        try {
        await loginWithOtp(email, code)
    nav(from, { replace: true })
    } catch (e) {
        alert(e.message || 'Invalid code')
    }
}


return (
<div className=" bg-gradient-to-br from-primary to-primary/30 min-h-screen py-[100px] px-4">
<h1 className=' font-bold'>Welcome to Hora</h1>
<p >Login with your email. We'll send you a one-time code.</p>


{step === 'email' && (
    <div >
    <label>
        <div>Email</div>
        <input
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        placeholder="you@example.com"
        style={{ width: '100%', padding: '10px 12px', borderRadius: 8, border: '1px solid #ddd' }}
        />
    </label>
    <button onClick={handleSend} disabled={sending || !email} style={{ padding: '10px 12px', borderRadius: 8 }}>
    {sending ? 'Sending…' : 'Send code'}
    </button>
    {/* Optional: magic link mode — enable when backend ready */}
    {/* <button onClick={() => AuthAPI.requestMagicLink(email)} disabled={!email}>Send magic link</button> */}
    </div>
)}


{step === 'code' && (
    <form onSubmit={handleVerify} style={{ display: 'grid', gap: 12 }}>
        <div style={{ color: '#444' }}>We sent a 6-digit code to <b>{email}</b></div>
            <label>
            <div>Code</div>
            <input
            inputMode="numeric"
            pattern="[0-9]*"
            maxLength={6}
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            placeholder="123456"
            style={{ letterSpacing: 6, width: '100%', padding: '10px 12px', borderRadius: 8, border: '1px solid #ddd' }}
            />
            </label>
        <button type="submit" disabled={!code || code.length < 6} style={{ padding: '10px 12px', borderRadius: 8 }}>Verify & Continue</button>
        <button type="button" onClick={() => setStep('email')} style={{ background: 'transparent', border: 'none', color: '#666', textDecoration: 'underline' }}>Change email</button>
    </form>
)}
</div>
)
}
