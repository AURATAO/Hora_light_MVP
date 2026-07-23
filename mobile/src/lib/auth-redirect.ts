import { makeRedirectUri } from "expo-auth-session";

/**
 * The OAuth redirect target, and the value that must be whitelisted verbatim
 * under Supabase → Authentication → URL Configuration → Redirect URLs.
 *
 * Why it is hardcoded rather than left to `makeRedirectUri({ scheme })`:
 * `makeRedirectUri` delegates to `Linking.createURL()`, which prefixes the
 * dev-server host when `Constants.expoConfig.hostUri` is set. In a development
 * build that yields `hora://192.168.x.x:8081` — a different URL on every
 * network, none of which can be whitelisted — while a store build yields
 * plain `hora://`. Passing `native` pins both to the same string: dev builds
 * report `ExecutionEnvironment.Bare`, which is exactly when `makeRedirectUri`
 * honours `native` and skips `createURL` entirely.
 *
 * The `auth/callback` path is deliberate too: Supabase appends the auth code
 * to this URL, and a hostless `hora://?code=...` is parsed inconsistently
 * (empty authority) across platforms. `hora://auth/callback?code=...` is not.
 */
export const AUTH_REDIRECT_URL = "hora://auth/callback";

export function getAuthRedirectUrl(): string {
  return makeRedirectUri({
    native: AUTH_REDIRECT_URL,
    scheme: "hora",
    path: "auth/callback",
  });
}
