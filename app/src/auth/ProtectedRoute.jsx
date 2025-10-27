import { useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { isAuthed } from '../api/client'
import { useLoader } from '../providers/LoaderProvider.jsx'

export default function ProtectedRoute({ children }) {
  const [ok, setOk] = useState(null) // null=檢查中
  const loc = useLocation()
  const { wrap } = useLoader()   

 useEffect(() => {
    let live = true
    wrap(async () => {
      const pass = await isAuthed().catch(() => false)
      if (!live) return
      setOk(pass)
    })
    return () => { live = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loc.pathname])

  if (ok === null) return null     // 畫面交給 Overlay 遮住
  if (!ok) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
  return children
}