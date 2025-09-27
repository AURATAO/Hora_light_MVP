import { Link } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext.jsx'

export default function Dashboard() {
  const { user, loading } = useAuth()
  return (
    <div className=' w-full flex justify-center flex-col items-center min-h-screen bg-gradient-to-br from-primary to-primary/30 text-accent'>
      <div className='max-w-7xl flex flex-col justify-center items-center text-center p-4'>
      <h2 className='font-semibold text-3xl pb-4'>Hello{user?.name ? `, ${user.name}` : ''} 👋</h2>
        <p className='text-4xl'>QuickRequest</p>
        <div className='flex gap-4 mt-4 flex-wrap'>
          <Link to="/tasks/new" className='button-tech '>Post a Task</Link>
          <Link to="/my" className='button-tech' >My Tasks</Link>
        </div>
      </div>
    </div>
  )
}

