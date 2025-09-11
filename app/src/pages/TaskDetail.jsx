import { useParams } from 'react-router-dom'
export default function TaskDetail() {
  const { id } = useParams()
  return (
    <div>
      <h2>Task #{id}</h2>
      <p>Details & chat go here.</p>
    </div>
  )
}