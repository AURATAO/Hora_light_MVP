// useNotifications.js
import { useEffect, useRef, useState, useCallback } from "react";
import { api } from "../api/client";

function emitUnread(has) {
  // 讓 Nav 立即知道 unread 狀態變化
  window.dispatchEvent(new CustomEvent("notif:unread", { detail: { has } }));
}

export function useNotifications() {
  const [items, setItems] = useState([]);
  const [loading, setLoading] = useState(false);
  const [unreadExists, setUnreadExists] = useState(false);
  const pollRef = useRef(null);

  const setUnread = (has) => {
    setUnreadExists(has);
    emitUnread(has);
  };

  const fetchList = useCallback(
    async ({ unread = false, limit = 20, before } = {}) => {
      setLoading(true);
      try {
        const qs = new URLSearchParams();
        if (unread) qs.set("unread", "true");
        if (limit) qs.set("limit", String(limit));
        if (before)
          qs.set(
            "before",
            before instanceof Date ? before.toISOString() : String(before)
          );
        const res = await api(`/notifications?${qs.toString()}`);
        const arr = Array.isArray(res) ? res : [];
        setItems(arr);
        setUnread(arr.some((n) => n.unread)); // ← 立即同步紅點
        return arr;
      } finally {
        setLoading(false);
      }
    },
    []
  );

  const refreshUnreadFlag = useCallback(async () => {
    try {
      const res = await api("/notifications?unread=true&limit=1");
      setUnread(Array.isArray(res) && res.length > 0);
    } catch {
      /* ignore */
    }
  }, []);

  const markRead = useCallback(async (id) => {
    await api(`/notifications/${id}/read`, { method: "PATCH" });
    setItems((prev) => {
      const next = prev.map((n) => (n.id === id ? { ...n, unread: false } : n));
      setUnread(next.some((n) => n.unread)); // ← 廣播變化
      return next;
    });
  }, []);

  const markAllRead = useCallback(async () => {
    await api("/notifications/mark-read-all", { method: "POST" });
    setItems((prev) => prev.map((n) => ({ ...n, unread: false })));
    setUnread(false); // ← 廣播變化
  }, []);

  const remove = useCallback(async (id) => {
    await api(`/notifications/${id}`, { method: "DELETE" });
    setItems((prev) => {
      const next = prev.filter((n) => n.id !== id);
      setUnread(next.some((n) => n.unread)); // ← 廣播變化
      return next;
    });
  }, []);

  const clearRead = useCallback(async () => {
    await api(`/notifications?read=true`, { method: "DELETE" });
    setItems((prev) => {
      const next = prev.filter((n) => n.unread);
      setUnread(next.some((n) => n.unread)); // ← 廣播變化
      return next;
    });
  }, []);

  useEffect(() => {
    // 進入頁面先刷一次
    refreshUnreadFlag();

    // 背景時不輪詢，回到可見時立刻刷一次
    const onVis = () => {
      if (document.visibilityState === "visible") {
        refreshUnreadFlag();
        if (!pollRef.current) {
          pollRef.current = setInterval(refreshUnreadFlag, 30_000);
        }
      } else {
        if (pollRef.current) {
          clearInterval(pollRef.current);
          pollRef.current = null;
        }
      }
    };
    document.addEventListener("visibilitychange", onVis);

    // 啟動輪詢
    pollRef.current = setInterval(refreshUnreadFlag, 30_000);

    return () => {
      document.removeEventListener("visibilitychange", onVis);
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [refreshUnreadFlag]);

  return {
    items,
    setItems,
    loading,
    unreadExists,
    fetchList,
    markRead,
    markAllRead,
    refreshUnreadFlag,
    remove,
    clearRead,
  };
}
