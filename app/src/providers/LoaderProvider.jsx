import React, { createContext, useContext, useMemo, useRef, useState } from "react";

const LoaderCtx = createContext(null);

/** 全域 Loading 管理（支援多重並發 + 最短顯示時間） */
export function LoaderProvider({ children, minMs = 400 }) {
  const [open, setOpen] = useState(false);
  const counterRef = useRef(0);
  const shownAtRef = useRef(0);
  const hideTimerRef = useRef();

  const reallyShow = () => {
    if (!open) {
      setOpen(true);
      shownAtRef.current = performance.now();
    }
  };
  const reallyHide = () => {
    const elapsed = performance.now() - shownAtRef.current;
    const wait = Math.max(0, minMs - elapsed);
    clearTimeout(hideTimerRef.current);
    hideTimerRef.current = setTimeout(() => setOpen(false), wait);
  };

  const show = () => { counterRef.current += 1; if (import.meta.env.DEV) console.log('[loader] show ->', counterRef.current); reallyShow(); };
  const hide = () => {
    counterRef.current = Math.max(0, counterRef.current - 1);
    if (import.meta.env.DEV) console.log('[loader] hide ->', counterRef.current);
    if (counterRef.current === 0) reallyHide();
  };
  if (typeof window !== 'undefined') {
   window.__loader = () => ({ open, count: counterRef.current });
 }

  const wrap = async (fnOrPromise) => {
    show();
    try {
      const p = typeof fnOrPromise === "function" ? fnOrPromise() : fnOrPromise;
      return await p;
    } finally {
      hide();
    }
  };

  const value = useMemo(() => ({ open, show, hide, wrap }), [open]);
  return <LoaderCtx.Provider value={value}>{children}</LoaderCtx.Provider>;
}

export const useLoader = () => {
  const ctx = useContext(LoaderCtx);
  if (!ctx) throw new Error("useLoader must be used within LoaderProvider");
  return ctx;
};