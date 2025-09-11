import React from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useAuth } from './AuthContext.jsx'


export default function ProtectedRoute({ children }) {
const { token } = useAuth()
const loc = useLocation()
    if (!token) {
    return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
    }
    return children
}