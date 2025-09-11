import { Link, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext.jsx'


export default function Nav() {
const { user, logout } = useAuth()
const loc = useLocation()
const hideOnLogin = loc.pathname.endsWith('/login')


if (hideOnLogin) return null


return (
    <header style={{ borderBottom: '1px solid #eee' }}>
        <nav style={{ maxWidth: 960, margin: '0 auto', padding: '12px 24px', display: 'flex', gap: 16, alignItems: 'center' }}>
            <Link to="/">Hora</Link>
            <span style={{ flex: 1 }} />
            <Link to="/tasks/new">Post a Task</Link>
            <Link to="/my">My</Link>
            {user ? (
            <button onClick={logout} style={{ marginLeft: 8 }}>Logout</button>
            ) : (
            <Link to="/login">Login</Link>
            )}
        </nav>
    </header>
    )
}