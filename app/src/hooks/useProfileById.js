import { useEffect, useState, useRef } from "react";
import { api } from "../api/client";

// 超簡單快取 & 併發去重
const cache = new Map(); // id -> profile
const inflight = new Map(); // id -> Promise

export function useProfileById(id) {
  const [data, setData] = useState(id ? cache.get(id) ?? null : null);
  const [loading, setLoading] = useState(!!id && !cache.has(id));
  const [error, setError] = useState(null);
  const mounted = useRef(true);

  useEffect(() => {
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    if (!id) {
      setData(null);
      setLoading(false);
      setError(null);
      return;
    }
    if (cache.has(id)) {
      setData(cache.get(id));
      setLoading(false);
      setError(null);
      return;
    }

    let p = inflight.get(id);
    if (!p) {
      p = api(`/profiles/${id}`)
        .then((res) => {
          cache.set(id, res);
          return res;
        })
        .finally(() => inflight.delete(id));
      inflight.set(id, p);
    }

    setLoading(true);
    setError(null);
    p.then((res) => {
      if (mounted.current) {
        setData(res);
        setLoading(false);
      }
    }).catch((e) => {
      if (mounted.current) {
        setError(e);
        setLoading(false);
      }
    });
  }, [id]);

  const refetch = async () => {
    if (!id) return;
    setLoading(true);
    setError(null);
    try {
      const res = await api(`/profiles/${id}`);
      cache.set(id, res);
      if (mounted.current) setData(res);
    } catch (e) {
      if (mounted.current) setError(e);
    } finally {
      if (mounted.current) setLoading(false);
    }
  };

  return { data, loading, error, refetch };
}
