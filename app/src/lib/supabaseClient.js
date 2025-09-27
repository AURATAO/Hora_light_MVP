import { createClient } from "@supabase/supabase-js";

const url = import.meta.env.VITE_SUPABASE_URL;
const anon = import.meta.env.VITE_SUPABASE_ANON_KEY;
if (!url || !anon)
  console.warn("Missing Supabase env", { url, hasKey: !!anon });

// 🔒 用 globalThis 做單例，避免 Vite HMR 反覆重建 client 導致 auto-refresh 中斷
console.log("[sb] url=", url, "anon.len=", anon?.length);

export const supabase =
  globalThis.__sb__ ||
  createClient(url, anon, {
    auth: {
      persistSession: true, // ✅ 將 session 存在 localStorage
      autoRefreshToken: true, // ✅ 自動用 refresh token 續期
      detectSessionInUrl: true, // ✅ 處理魔法連結 redirect
      flowType: "pkce", // 建議
      storageKey: "hora.auth", // （可選）自訂 key，避免被其它專案覆蓋
    },
  });

globalThis.__sb__ = supabase;
