// app/src/hooks/useProfileById.js
import { useEffect, useRef, useState, useDebugValue } from "react";
import { api } from "../api/client";

// 極簡快取 & 併發去重
const cache = new Map(); // key(id:string) -> profile | null
const inflight = new Map(); // key(id:string) -> Promise<profile | null>

export function useProfileById(id) {
  const idStr = id ? String(id) : ""; // ① 總是用字串 key
  const [data, setData] = useState(idStr ? cache.get(idStr) ?? null : null);
  const [loading, setLoading] = useState(Boolean(idStr) && !cache.has(idStr));
  const [error, setError] = useState(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    if (!idStr) {
      setData(null);
      setLoading(false);
      setError(null);
      return;
    }

    // 命中快取
    if (cache.has(idStr)) {
      setData(cache.get(idStr));
      setLoading(false);
      setError(null);
      return;
    }

    // 併發去重
    let p = inflight.get(idStr);
    if (!p) {
      p = api(`/profiles/${idStr}`)
        .then((res) => {
          cache.set(idStr, res ?? null);
          return res ?? null;
        })
        .catch((e) => {
          // ② 404 視為無資料，落地避免重打；其他錯保留錯誤
          const status = e?.status || e?.response?.status;
          if (status === 404) {
            cache.set(idStr, null);
            return null;
          }
          throw e;
        })
        .finally(() => inflight.delete(idStr));
      inflight.set(idStr, p);
    }

    setLoading(true);
    setError(null);

    let cancelled = false;
    p.then((res) => {
      if (!cancelled && mounted.current) {
        setData(res);
        setLoading(false);
      }
    }).catch((e) => {
      if (!cancelled && mounted.current) {
        setError(e?.message ?? String(e));
        setLoading(false);
      }
    });

    return () => {
      cancelled = true;
    };
  }, [idStr]);

  // ③ 對外工具
  const refetch = async () => {
    if (!idStr) return;
    setLoading(true);
    setError(null);
    try {
      const res = await api(`/profiles/${idStr}`);
      cache.set(idStr, res ?? null);
      if (mounted.current) setData(res ?? null);
    } catch (e) {
      const status = e?.status || e?.response?.status;
      if (status === 404) {
        cache.set(idStr, null);
        if (mounted.current) setData(null);
      } else if (mounted.current) {
        setError(e?.message ?? String(e));
      }
    } finally {
      if (mounted.current) setLoading(false);
    }
  };

  useDebugValue({ id: idStr, loading, hasCache: cache.has(idStr) });

  return { data, loading, error, refetch };
}

// 供外部在「更新個人檔案成功」後直接同步快取
export function primeProfileCache(id, profile) {
  const idStr = id ? String(id) : "";
  if (!idStr) return;
  cache.set(idStr, profile ?? null);
}
export function invalidateProfileCache(id) {
  const idStr = id ? String(id) : "";
  if (!idStr) return;
  cache.delete(idStr);
  inflight.delete(idStr);
}
