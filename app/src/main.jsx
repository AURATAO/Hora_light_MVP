// import React from 'react'
// import { createRoot } from 'react-dom/client'
// import { BrowserRouter } from 'react-router-dom'
// import App from './App.jsx'
// import { AuthProvider } from './auth/AuthContext.jsx'
// import './index.css'
// import loadingIcon from './assets/Loading_logo.png'

// import { LoaderProvider } from './providers/LoaderProvider.jsx'
// import OverlayLoader from './components/OverlayLoader.jsx'

// import { supabase } from './lib/supabaseClient'
// supabase.auth.getSession().catch(() => {})

// createRoot(document.getElementById('root')).render(
// <React.StrictMode>
//     {/* The app is mounted at /app (see vite base) */}
//     <BrowserRouter basename="/app">
//         <LoaderProvider minMs={400}>
//             <AuthProvider>
//                 <App />
//                  {/* 全域透明 Loading，放一次即可 */}
//                  <OverlayLoader mask="light" blur logoSrc={loadingIcon} size={64} speedMs={1200}/>
//             </AuthProvider>
//         </LoaderProvider>
//     </BrowserRouter>
// </React.StrictMode>
// )
// app/src/main.jsx
import React from "react";
import { createRoot } from "react-dom/client";

const el = document.getElementById("root");
console.log("[boot] main loaded, root=", el);

const root = createRoot(el);
root.render(
  <div style={{ padding: 24, background: "#fffbe6", fontSize: 18 }}>
    App booted ✅
  </div>
);