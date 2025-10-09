import { useEffect, useRef, useState, useCallback } from "react";
import { api } from "../api/client";

export function useNotifications() {
  const [items, setItems] = useState([]);
  const [loading, setLoading] = useState(false);
  const [unreadExists, setUnreadExists] = useState(false);
  const pollRef = useRef(null);

  const fetchList = useCallback(
    async ({ unread = false, limit = 20, before } = {}) => {
      setLoading(true);
      try {
        const qs = new URLSearchParams();
        if (unread) qs.set("unread", "true");
        if (limit) qs.set("limit", String(limit));
        if (before) qs.set("before", before); // RFC3339 字串
        const res = await api(`/notifications?${qs.toString()}`);
        setItems(res);
        // ✅ 立即同步紅點狀態
        setUnreadExists(Array.isArray(res) && res.some((n) => n.unread));
        return res;
      } finally {
        setLoading(false);
      }
    },
    []
  );

  const refreshUnreadFlag = useCallback(async () => {
    try {
      // 省流量：只抓一筆判斷有無
      const res = await api("/notifications?unread=true&limit=1");
      setUnreadExists(Array.isArray(res) && res.length > 0);
    } catch {
      /* ignore */
    }
  }, []);

  const markRead = useCallback(async (id) => {
    await api(`/notifications/${id}/read`, { method: "PATCH" });
    setItems((prev) => {
      const next = prev.map((n) => (n.id === id ? { ...n, unread: false } : n));
      setUnreadExists(next.some((n) => n.unread));
      return next;
    });
  }, []);

  const markAllRead = useCallback(async () => {
    await api("/notifications/mark-read-all", { method: "POST" });
    setItems((prev) => prev.map((n) => ({ ...n, unread: false })));
    setUnreadExists(false);
  }, []);

  // 刪除單筆
  const remove = useCallback(async (id) => {
    await api(`/notifications/${id}`, { method: "DELETE" });
    setItems((prev) => {
      const next = prev.filter((n) => n.id !== id);
      setUnreadExists(next.some((n) => n.unread));
      return next;
    });
  }, []);

  // 清除全部已讀
  const clearRead = useCallback(async () => {
    await api(`/notifications?read=true`, { method: "DELETE" });
    setItems((prev) => {
      const next = prev.filter((n) => n.unread);
      setUnreadExists(next.some((n) => n.unread));
      return next;
    });
  }, []);

  useEffect(() => {
    refreshUnreadFlag();
    const id = setInterval(refreshUnreadFlag, 30_000);
    pollRef.current = id;
    return () => {
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
