import { Link, useLocation } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext.jsx'


export default function Nav() {
const { user, logout } = useAuth()
const loc = useLocation()
const hideOnLogin = loc.pathname.endsWith('/login')


if (hideOnLogin) return null


return (
    <header className='sticky top-0 inset-x-0 z-50 bg-gradient-to-br from-primary to-primary/50 text-accent font-secondary' >
        <nav className='max-w-4xl m-auto justify-center items-center px-4 py-3 flex gap-4' >
            <Link to="/" className='flex justify-center text-3xl'><img src="../Logo.svg"  className='h-7 w-30'/></Link>
            <div className='flex justify-center gap-4 ml-auto'>
            <Link to="/tasks/new">Post a Task</Link>
            <Link to="/my">My</Link>
            {user ? (
            <button onClick={logout} >Logout</button>
            ) : (
            <Link to="/login">Login</Link>
            )}
            </div>
        </nav>
    </header>
    )
}