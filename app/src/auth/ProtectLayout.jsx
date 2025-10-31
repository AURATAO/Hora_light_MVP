// src/auth/ProtectedLayout.jsx
import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "./AuthContext.jsx";
import { useEffect, useState } from "react";

export default function ProtectedLayout() {
  console.log('[Guard]', { loading, user, timeoutHit: timeoutHit })
  
  const { user, loading } = useAuth();
  const loc = useLocation();
  const [timeoutHit, setTimeoutHit] = useState(false);

  // 保險機制：最多等 8 秒就不再顯示空白
  useEffect(() => {
    if (!loading) return;
    const t = setTimeout(() => setTimeoutHit(true), 8000);
    return () => clearTimeout(t);
  }, [loading]);

  if (loading && !timeoutHit) {
    return (
      <div style={{ padding: 24 }}>
        Loading…
      </div>
    );
  }

  // 超時仍未登入 → 送去 /login（帶回跳資訊）
  if (!user) {
    return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />;
  }

  return <Outlet />;
}