import { createClient } from "@supabase/supabase-js";

const supabaseUrl = import.meta.env.VITE_SUPABASE_URL;
const supabaseAnonKey = import.meta.env.VITE_SUPABASE_ANON_KEY;
if (!supabaseUrl || !supabaseAnonKey)
  console.warn("Missing Supabase env", {
    supabaseUrl,
    hasKey: !!supabaseAnonKey,
  });

// 🔒 用 globalThis 做單例，避免 Vite HMR 反覆重建 client 導致 auto-refresh 中斷

export const supabase =
  globalThis.__sb__ ||
  createClient(supabaseUrl, supabaseAnonKey, {
    auth: {
      persistSession: true, // ✅ 將 session 存在 localStorage
      autoRefreshToken: true, // ✅ 自動用 refresh token 續期
      detectSessionInUrl: true, // ✅ 處理魔法連結 redirect
      flowType: "pkce", // 建議
      storageKey: "hora.auth", // （可選）自訂 key，避免被其它專案覆蓋
    },
  });

globalThis.__sb__ = supabase;
