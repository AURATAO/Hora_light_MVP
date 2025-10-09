import { supabase } from "../lib/supabaseClient";

const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:8080";

// 取得最新可用的 access token（若沒有先 refresh 一次）
async function getFreshAccessToken() {
  let {
    data: { session },
  } = await supabase.auth.getSession();
  if (session?.access_token) return session.access_token;

  const { data, error } = await supabase.auth.refreshSession();
  if (!error && data.session?.access_token) {
    return data.session.access_token;
  }
  throw new Error("Not authenticated");
}

async function rawFetch(
  path,
  { method = "GET", body, headers = {} } = {},
  token
) {
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
      ...headers,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  return res;
}

// 高階 api：自帶 token，若 401 會 refresh 後重試一次
export async function api(path, opts = {}) {
  let token = await getFreshAccessToken();
  let res = await rawFetch(path, opts, token);

  if (res.status === 401) {
    const { data, error } = await supabase.auth.refreshSession();
    if (!error && data.session?.access_token) {
      token = data.session.access_token;
      res = await rawFetch(path, opts, token);
    }
  }

  if (!res.ok) {
    const text = await res.text().catch(() => `HTTP ${res.status}`);
    throw new Error(text);
  }

  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

// 直接使用 Supabase 的 Auth 流程
export const AuthAPI = {
  async requestOtp(email) {
    const { error } = await supabase.auth.signInWithOtp({
      email,
      options: {
        shouldCreateUser: true,
        emailRedirectTo: "http://localhost:5173/auth/callback",
      },
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
    return data; // data.session, data.user
  },

  async me() {
    const {
      data: { user },
    } = await supabase.auth.getUser();
    return user;
  },

  async signOut() {
    await supabase.auth.signOut();
  },
};
