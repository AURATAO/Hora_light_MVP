import { Link } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext.jsx'

export default function Dashboard() {
  const { user } = useAuth()
  return (
    <div style={{ display: 'grid', gap: 16 }}>
      <h2>Hello{user?.name ? `, ${user.name}` : ''} 👋</h2>
      <p>Quick actions</p>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <Link to="/tasks/new" style={tile}>Post a Task</Link>
        <Link to="/my" style={tile}>My Tasks</Link>
      </div>
    </div>
  )
}

const tile = {
  padding: '16px 20px',
  border: '1px solid #eee',
  borderRadius: 12,
  background: '#fff',
}