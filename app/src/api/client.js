// src/api/client.js
import { supabase } from "../lib/supabaseClient";

export const API_BASE =
  (
    import.meta.env.VITE_API_BASE_URL ??
    import.meta.env.VITE_API_BASE ??
    "/api"
  ).replace(/\/+$/, "") || "/api";

const appBase = (import.meta.env.BASE_URL || "/").replace(/\/?$/, "/");

console.log("[client.js] API_BASE=", API_BASE);

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

// ✅ 包一層可取消 + 逾時
function fetchWithTimeout(input, init = {}, timeoutMs = 8000) {
  const ctrl = new AbortController();
  const id = setTimeout(() => ctrl.abort("timeout"), timeoutMs);
  return fetch(input, { ...init, signal: ctrl.signal }).finally(() =>
    clearTimeout(id)
  );
}

// 「雙送」api：預設帶 cookie；若有 token 也帶 Bearer；加逾時避免卡住
export async function api(path, opts = {}) {
  const token = await maybeGetSupabaseToken();
  const url = path.startsWith("http") ? path : `${API_BASE}${path}`;

  const headers = new Headers(opts.headers || {});
  headers.set(
    "Content-Type",
    headers.get("Content-Type") || "application/json"
  );
  if (token) headers.set("Authorization", `Bearer ${token}`);

  let res;
  try {
    res = await fetchWithTimeout(
      url,
      {
        method: opts.method || "GET",
        credentials: "include",
        cache: "no-store",
        redirect: "follow",
        headers,
        body: opts.body ? JSON.stringify(opts.body) : undefined,
      },
      opts.timeoutMs ?? 8000
    );
  } catch (e) {
    // 逾時或被中止
    console.error("[api] fetch error:", e);
    // 讓呼叫端可進 finally，避免 UI 卡住
    const err = new Error(e?.message || "Network error");
    err.status = 0;
    throw err;
  }

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

// AuthAPI 維持不變
// export const AuthAPI = {
//   async me() {
//     const res = await api("/auth/me", { timeoutMs: 8000 }).catch((e) => {
//       console.warn("[AuthAPI.me] failed:", e);
//       return null;
//     });
//     return res && res.auth
//       ? { id: res.id, email: res.email, name: res.name || res.email }
//       : null;
//   },

//   async logout() {
//     await api("/auth/logout", { method: "POST", timeoutMs: 8000 }).catch(
//       () => {}
//     );
//     await supabase.auth.signOut().catch(() => {});
//     return true;
//   },

//   async requestOtp(email) {
//     const { error } = await supabase.auth.signInWithOtp({
//       email,
//       options: {
//         shouldCreateUser: true,
//         emailRedirectTo: `${window.location.origin}${
//           import.meta.env.BASE_URL || "/"
//         }`,
//       },
//     });
//     if (error) {
//       console.error("[OTP][error]", {
//         name: error.name,
//         message: error.message,
//         status: error.status,
//         code: error.code,
//       });
//       alert(`${error.message} (code: ${error.code || "n/a"})`);
//       throw error;
//     }
//     return true;
//   },

//   async verifyOtp(email, code) {
//     const { data, error } = await supabase.auth.verifyOtp({
//       email,
//       token: code,
//       type: "email",
//     });
//     if (error) throw error;
//     return data;
//   },

//   loginWithGoogle(next = "/") {
//     const baseURL = new URL(appBase, window.location.origin);
//     const nextAbs = new URL(next, baseURL).toString();
//     window.location.href = `${API_BASE}/auth/login?next=${encodeURIComponent(
//       nextAbs
//     )}`;
//   },
// };

export const AuthAPI = {
  async me() {
    const res = await api("/auth/me", { timeoutMs: 8000 }).catch((e) => {
      console.warn("[AuthAPI.me] failed:", e);
      return null;
    });
    return res && res.auth
      ? { id: res.id, email: res.email, name: res.name || res.email }
      : null;
  },

  async logout() {
    await api("/auth/logout", { method: "POST", timeoutMs: 8000 }).catch(
      () => {}
    );
    await supabase.auth.signOut().catch(() => {});
    return true;
  },

  async requestOtp(email) {
    const { error } = await supabase.auth.signInWithOtp({
      email,
      options: {
        shouldCreateUser: true,
        emailRedirectTo: `${window.location.origin}${
          import.meta.env.BASE_URL || "/"
        }`,
      },
    });
    if (error) {
      console.error("[OTP][error]", {
        name: error.name,
        message: error.message,
        status: error.status,
        code: error.code,
      });
      alert(`${error.message} (code: ${error.code || "n/a"})`);
      throw error;
    }
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

  loginWithGoogle(next = "/") {
    const baseURL = new URL(appBase, window.location.origin);
    const nextAbs = new URL(next, baseURL).toString();
    window.location.href = `${API_BASE}/auth/login?next=${encodeURIComponent(
      nextAbs
    )}`;
  },
};
