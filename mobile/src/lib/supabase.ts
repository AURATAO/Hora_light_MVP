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

// EXPO_PUBLIC_* values are inlined at bundle time, so a cloud build whose
// environment is unset ships `undefined` here with no local symptom at all.
// Throwing at module scope is what that used to do, and because the root layout
// imports this file, the throw landed during module evaluation — before React
// mounted — so it surfaced as a native launch crash with no readable cause.
// Instead we record what is missing and let the root layout render ConfigError.
export const missingEnvVars = (
  [
    ["EXPO_PUBLIC_SUPABASE_URL", supabaseUrl],
    ["EXPO_PUBLIC_SUPABASE_ANON_KEY", supabaseAnonKey],
  ] as const
)
  .filter(([, value]) => !value)
  .map(([name]) => name);

// Only reached when missingEnvVars is non-empty, in which case the app renders
// ConfigError and never issues a request. createClient rejects an empty URL, so
// the placeholder has to be syntactically valid rather than "".
const PLACEHOLDER_URL = "https://unconfigured.invalid";
const PLACEHOLDER_KEY = "unconfigured";

// PKCE so magic-link/OAuth deep links return a `?code=` param instead of a
// URL fragment, which is straightforward to parse with expo-linking.
export const supabase = createClient(supabaseUrl ?? PLACEHOLDER_URL, supabaseAnonKey ?? PLACEHOLDER_KEY, {
  auth: {
    storage: SecureStoreAdapter,
    autoRefreshToken: true,
    persistSession: true,
    detectSessionInUrl: false,
    flowType: "pkce",
  },
});
