// src/api/client.js
import { supabase } from "../lib/supabaseClient";

// 1) API_BASE（容錯多環境變數）
export const API_BASE =
  (
    import.meta.env.VITE_API_BASE_URL ??
    import.meta.env.VITE_API_BASE ??
    "/api"
  ).replace(/\/+$/, "") || "/api";

const appBase = (import.meta.env.BASE_URL || "/").replace(/\/?$/, "/");

console.log("[client.js] API_BASE=", API_BASE);

// 2) 可選 Bearer 的 token 取得
async function maybeGetSupabaseToken() {
  try {
    const {
      data: { session },
    } = await supabase.auth.getSession();
    return session?.access_token || null;
  } catch {
    return null;
  }
}

// 3) 「雙送」api：預設帶 cookie；若有 token 也帶 Bearer
export async function api(path, opts = {}) {
  const token = await maybeGetSupabaseToken();
  const url = path.startsWith("http") ? path : `${API_BASE}${path}`;

  const headers = new Headers(opts.headers || {});
  headers.set(
    "Content-Type",
    headers.get("Content-Type") || "application/json"
  );
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const res = await fetch(url, {
    method: opts.method || "GET",
    credentials: "include", // 關鍵：永遠帶 cookie
    cache: "no-store",
    redirect: "follow",
    headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });

  const ct = res.headers.get("content-type") || "";
  const body = ct.includes("application/json")
    ? await res.json()
    : await res.text();

  if (!res.ok) {
    const err = new Error("HTTP " + res.status);
    err.status = res.status;
    err.body = body;
    throw err;
  }
  return body;
}

// 4) AuthAPI（統一出口）
export const AuthAPI = {
  async me() {
    const res = await api("/auth/me"); // 自帶 credentials
    return res && res.auth
      ? { id: res.id, email: res.email, name: res.name || res.email }
      : null;
  },

  // 單一登出：清 server cookie + supabase
  async logout() {
    await api("/auth/logout", { method: "POST" }).catch(() => {});
    await supabase.auth.signOut().catch(() => {});
    return true;
  },

  async requestOtp(email) {
    const { error } = await supabase.auth.signInWithOtp({
      email,
      options: { shouldCreateUser: true },
    });
    if (error) throw error;
    return true;
  },

  async verifyOtp(email, code) {
    const { data, error } = await supabase.auth.verifyOtp({
      email,
      token: code,
      type: "email",
    });
    if (error) throw error;
    return data;
  },

  // Exchange a Supabase access token for an internal hora_session cookie.
  async exchangeToken(accessToken) {
    return api("/auth/exchange", {
      method: "POST",
      body: { access_token: accessToken },
    });
  },

  loginWithGoogle(next = "/") {
    // 正確組成 base 下的絕對 next
    const baseURL = new URL(appBase, window.location.origin);
    const nextAbs = new URL(next, baseURL).toString();
    window.location.href = `${API_BASE}/auth/login?next=${encodeURIComponent(
      nextAbs
    )}`;
  },
};
