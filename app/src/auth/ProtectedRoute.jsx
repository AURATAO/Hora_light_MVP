import { useEffect, useState } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { isAuthed } from '../api/client'

export default function ProtectedRoute({ children }) {
  const [ok, setOk] = useState(null) // null=檢查中
  const loc = useLocation()

  useEffect(() => {
    let live = true
    console.debug('[PR] checking...', loc.pathname)
    ;(async () => {
      try {
        const pass = await isAuthed()
        if (!live) return
        console.debug('[PR] result=', pass, 'path=', loc.pathname)
        setOk(pass)
      } catch (e) {
        if (!live) return
        console.debug('[PR] error=', e)
        setOk(false)
      }
    })()
    return () => { live = false }
  }, [loc.pathname])

  if (ok === null) return null
  if (!ok) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
  return children
}