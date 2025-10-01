import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";

export function useNotifications() {
  const [items, setItems] = useState([]);
  const [loading, setLoading] = useState(false);
  const [unreadExists, setUnreadExists] = useState(false);
  const pollRef = useRef(null);

  async function fetchList({ unread = false, limit = 20, before } = {}) {
    setLoading(true);
    try {
      const qs = new URLSearchParams();
      if (unread) qs.set("unread", "true");
      if (limit) qs.set("limit", String(limit));
      if (before) qs.set("before", before);
      const res = await api(`/notifications?${qs.toString()}`);
      setItems(res);
      return res;
    } finally {
      setLoading(false);
    }
  }

  async function refreshUnreadFlag() {
    try {
      // 為省流量：只抓 1 筆未讀，判斷有/無
      const res = await api("/notifications?unread=true&limit=1");
      setUnreadExists(Array.isArray(res) && res.length > 0);
    } catch {
      /* ignore */
    }
  }

  async function markRead(id) {
    await api(`/notifications/${id}/read`, { method: "PATCH" });
    setItems((prev) =>
      prev.map((n) => (n.id === id ? { ...n, unread: false } : n))
    );
    refreshUnreadFlag();
  }

  async function markAllRead() {
    await api("/notifications/mark-read-all", { method: "POST" });
    setItems((prev) => prev.map((n) => ({ ...n, unread: false })));
    setUnreadExists(false);
  }

  useEffect(() => {
    refreshUnreadFlag();
    pollRef.current = setInterval(refreshUnreadFlag, 30_000); // 每 30s 輕量輪詢
    return () => clearInterval(pollRef.current);
  }, []);

  return {
    items,
    setItems,
    loading,
    unreadExists,
    fetchList,
    markRead,
    markAllRead,
    refreshUnreadFlag,
  };
}
