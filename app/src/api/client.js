import { supabase } from "../lib/supabaseClient";

const API_BASE = import.meta.env.VITE_API_BASE_URL || "http://localhost:8080";

// 只拿“現有”的 access token；沒有就當成未登入
async function getAccessTokenStrict() {
  const {
    data: { session },
  } = await supabase.auth.getSession();
  if (!session?.access_token) throw new Error("Not authenticated");
  return session.access_token;
}

export async function api(path, { method = "GET", body, headers = {} } = {}) {
  const accessToken = await getAccessTokenStrict();
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${accessToken}`,
      ...headers,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok)
    throw new Error(await res.text().catch(() => `HTTP ${res.status}`));
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

// ▶️ Supabase 版 AuthAPI：直接打 Supabase，不再走你自己的 /auth/*
export const AuthAPI = {
  // 發送 6 碼 OTP（不存在的 email 也會自動建帳）
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

  // 驗證 6 碼 OTP（type 要寫 'email'）
  async verifyOtp(email, code) {
    const { data, error } = await supabase.auth.verifyOtp({
      email,
      token: code,
      type: "email",
    });
    if (error) throw error;
    return data; // data.session, data.user
  },

  // 取得目前登入者（可供 /me 頁面用）
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
