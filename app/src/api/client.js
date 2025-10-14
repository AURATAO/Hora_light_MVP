import { supabase } from "../lib/supabaseClient";

export const API_BASE =
  (
    import.meta.env.VITE_API_BASE_URL ??
    import.meta.env.VITE_API_BASE ??
    "/api"
  ).replace(/\/+$/, "") || "/api";

console.log("[client.js] API_BASE=", API_BASE);

// 取 token（拿不到就回 null，不丟錯）
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

// 「雙送」的 fetch：永遠帶 cookie；若有 token 也帶 Bearer
export async function api(path, opts = {}) {
  const token = await maybeGetSupabaseToken();

  const res = await fetch(`${API_BASE}${path}`, {
    method: opts.method || "GET",
    credentials: "include", // ← 永遠帶 cookie
    cache: "no-store",
    redirect: "follow",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}), // ← 有就帶
      ...(opts.headers || {}),
    },
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });

  if (!res.ok) {
    throw new Error((await res.text().catch(() => "")) || `HTTP ${res.status}`);
  }
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

/** Auth 封裝 */
export const AuthAPI = {
  async me() {
    // 後端的 /auth/me 已用 tryAuth：能吃 cookie 或 bearer
    const res = await api("/auth/me");
    if (res && res.auth)
      return { id: res.id, email: res.email, name: res.name || res.email };
    return null;
  },

  // ——— 如果你現在 OTP 用 Supabase（6 碼） ———
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
    return data; // 成功後 supabase 會有 session，api() 就會自動帶 Bearer
  },

  async signOut() {
    // 登出兩邊都清一下：cookie + supabase
    await fetch(`${API_BASE}/auth/logout`, {
      method: "POST",
      credentials: "include",
    }).catch(() => {});
    await supabase.auth.signOut().catch(() => {});
  },

  loginWithGoogle(next = "/") {
    const appBase = import.meta.env.BASE_URL || "/";
    const baseURL = window.location.origin + appBase.replace(/\/$/, "");
    const nextAbs = /^https?:\/\//i.test(next)
      ? next
      : baseURL + (next.startsWith("/") ? "" : "/") + next;
    window.location.href = `${API_BASE}/auth/login?next=${encodeURIComponent(
      nextAbs
    )}`;
  },
};
