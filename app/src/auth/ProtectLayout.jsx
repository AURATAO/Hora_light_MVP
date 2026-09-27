import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useAuth } from "./AuthContext.jsx";
import { useEffect, useState } from "react";
import BetaModal from "../components/BetaModal.jsx";
import { useProfileGate } from "../hooks/useProfileGate.js";
import WhatsAppFloat from "../components/WhatsAppFloat.jsx";
import { api } from "../api/client";
import { flushTermsConsent } from "../lib/termsConsent";
import NamePrompt from "../components/NamePrompt.jsx";

export default function ProtectedLayout() {
  const { user, loading } = useAuth();
  const loc = useLocation();
  const [timeoutHit, setTimeoutHit] = useState(false);
  const { checking } = useProfileGate();

  // Record the sign-in consent checkbox once a session exists (lib/termsConsent).
  useEffect(() => {
    if (user) flushTermsConsent(api);
  }, [user]);

  // 最多等 8 秒就不再顯示空白
  useEffect(() => {
    if (!loading) return;
    const t = setTimeout(() => setTimeoutHit(true), 8000);
    return () => clearTimeout(t);
  }, [loading]);

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

  if (checking) return null

  return (
    <>
      <BetaModal />
      <NamePrompt />
      <Outlet />
      <WhatsAppFloat />
    </>
  )
}
