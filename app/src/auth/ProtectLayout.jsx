// import { Navigate, Outlet, useLocation } from "react-router-dom";
// import { useAuth } from "./AuthContext.jsx";
// import { useEffect, useState } from "react";

// export default function ProtectedLayout() {
//   console.log('[Guard]', { loading, user, timeoutHit: timeoutHit })
//   const { user, loading: authLoading } = useAuth();
//   const loc = useLocation();
//   const [timeoutHit, setTimeoutHit] = useState(false);

//   // 最多等 8 秒，避免長時間空白
//   useEffect(() => {
//     if (!authLoading) return;
//     const t = setTimeout(() => setTimeoutHit(true), 8000);
//     return () => clearTimeout(t);
//   }, [authLoading]);

//   if (authLoading && !timeoutHit) {
//     return <div style={{ padding: 24 }}>Loading…</div>;
//   }

//   if (!user) {
//     return (
//       <Navigate
//         to="/login"
//         replace
//         state={{ from: loc.pathname + loc.search }}
//       />
//     );
//   }

//   return <Outlet />;
// }

import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "./AuthContext.jsx";

export default function ProtectedLayout() {
  const { user, loading } = useAuth();
  const loc = useLocation();

  // 首屏等 /auth/me 結束，避免空白或亂跳
  if (loading) {
    return <div style={{ padding: 16 }}>Loading…</div>;
  }

  if (!user) {
    return (
      <Navigate
        to="/login"
        replace
        state={{ from: loc.pathname + loc.search }}
      />
    );
  }

  return <Outlet />;
}