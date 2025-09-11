import React from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App.jsx'
import { AuthProvider } from './auth/AuthContext.jsx'
import './index.css'


createRoot(document.getElementById('root')).render(
<React.StrictMode>
{/* The app is mounted at /app (see vite base) */}
<BrowserRouter basename="/app">
<AuthProvider>
<App />
</AuthProvider>
</BrowserRouter>
</React.StrictMode>
)