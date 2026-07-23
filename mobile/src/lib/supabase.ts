// Import order matters: both polyfills install globals that the Supabase
// client reads at construction / first-use time. `crypto-polyfill` gives
// supabase-js the WebCrypto surface it needs to use an s256 PKCE code
// challenge instead of silently downgrading to `plain`.
import "react-native-url-polyfill/auto";
import "./crypto-polyfill";
import { createClient } from "@supabase/supabase-js";
import * as SecureStore from "expo-secure-store";

const SecureStoreAdapter = {
  getItem: (key: string) => SecureStore.getItemAsync(key),
  setItem: (key: string, value: string) => SecureStore.setItemAsync(key, value),
  removeItem: (key: string) => SecureStore.deleteItemAsync(key),
};

const supabaseUrl = process.env.EXPO_PUBLIC_SUPABASE_URL;
const supabaseAnonKey = process.env.EXPO_PUBLIC_SUPABASE_ANON_KEY;

if (!supabaseUrl || !supabaseAnonKey) {
  throw new Error(
    "Missing EXPO_PUBLIC_SUPABASE_URL or EXPO_PUBLIC_SUPABASE_ANON_KEY. Copy .env.example to .env and fill them in."
  );
}

// PKCE so magic-link/OAuth deep links return a `?code=` param instead of a
// URL fragment, which is straightforward to parse with expo-linking.
export const supabase = createClient(supabaseUrl, supabaseAnonKey, {
  auth: {
    storage: SecureStoreAdapter,
    autoRefreshToken: true,
    persistSession: true,
    detectSessionInUrl: false,
    flowType: "pkce",
  },
});
