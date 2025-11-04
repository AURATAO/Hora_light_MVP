import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "./AuthContext.jsx";
import { useEffect, useState } from "react";

export default function ProtectedLayout() {
  const { user, loading } = useAuth();
  const loc = useLocation();
  const [timeoutHit, setTimeoutHit] = useState(false);

  // 最多等 8 秒就不再顯示空白
  useEffect(() => {
    if (!loading) return;
    const t = setTimeout(() => setTimeoutHit(true), 8000);
    return () => clearTimeout(t);
  }, [loading]);

  // ✅ 放到這裡（宣告之後）
  console.log("[Guard]", { loading, user, timeoutHit });

  if (loading && !timeoutHit) {
    return <div style={{ padding: 24 }}>Loading…</div>;
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
