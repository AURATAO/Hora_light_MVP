import { useState } from "react";
import { View, Text, ActivityIndicator, Alert } from "react-native";
import { useRouter } from "expo-router";
import * as WebBrowser from "expo-web-browser";
import * as SecureStore from "expo-secure-store";
import { supabase } from "../../lib/supabase";
import { hasWebCryptoSupport } from "../../lib/crypto-polyfill";
import { getAuthRedirectUrl } from "../../lib/auth-redirect";
import { apiFetch, reviewLogin, type SessionIdentity } from "../../lib/api";
import { Screen, Input, PressableScale, Logo, Checkbox } from "../../components/ui";
import { LEGAL_URLS } from "../../lib/constants";
import { color } from "../../theme/tokens";
import { useAuthState } from "../_layout";

WebBrowser.maybeCompleteAuthSession();

// The callback lands on AUTH_REDIRECT_URL carrying either `?code=` (PKCE
// success) or `?error=&error_description=` (provider or Supabase refusal).
// Some Supabase errors arrive in the fragment rather than the query string,
// so read both.
function parseCallbackUrl(url: string) {
  const parsed = new URL(url);
  const params = new URLSearchParams(parsed.search);
  const fragment = new URLSearchParams(parsed.hash.replace(/^#/, ""));
  const read = (key: string) => params.get(key) ?? fragment.get(key) ?? undefined;
  return {
    code: read("code"),
    error: read("error") ?? read("error_code"),
    errorDescription: read("error_description"),
  };
}

// Exchanges the PKCE auth code for a session. This must run in the SAME JS
// session that called signInWithOAuth: that call wrote the code verifier to
// SecureStore, and Supabase matches it against the challenge it recorded when
// the flow started. A reload in between (or a deep-link listener picking the
// callback up after a relaunch) loses the pairing.
async function completeSessionFromUrl(url: string) {
  const { code, error, errorDescription } = parseCallbackUrl(url);

  if (__DEV__) {
    console.log("[auth] callback url:", url);
    console.log("[auth] callback code:", code ? `${code.slice(0, 8)}…` : "(none)");
  }

  if (error) throw new Error(errorDescription ?? error);
  if (!code) return null;

  const { data, error: exchangeError } = await supabase.auth.exchangeCodeForSession(code);
  if (exchangeError) throw exchangeError;
  return data.session;
}

export default function Login() {
  const router = useRouter();
  const { refresh } = useAuthState();
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [codeSent, setCodeSent] = useState(false);
  const [loadingGoogle, setLoadingGoogle] = useState(false);
  const [loadingSendCode, setLoadingSendCode] = useState(false);
  const [loadingVerifyCode, setLoadingVerifyCode] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Consent is deliberately screen-local, not persisted: the user re-confirms
  // on every login, which is the standard pattern for this gate.
  const [consented, setConsented] = useState(false);

  // Every login path converges here once the backend has set the hora_session
  // cookie. Routes through the root gate (index) rather than straight to the
  // tabs, so a new / incomplete user lands in onboarding. refresh() populates
  // the cached profile first, so the gate reads it without a bounce back here.
  async function enterApp(me: SessionIdentity) {
    if (me?.id) await SecureStore.setItemAsync("hora_user_id", String(me.id));
    if (me?.email) await SecureStore.setItemAsync("hora_user_email", String(me.email));
    await refresh();
    router.replace("/");
  }

  async function finishLogin(accessToken: string) {
    const me = await apiFetch<SessionIdentity>("/auth/exchange", {
      method: "POST",
      body: { access_token: accessToken },
    });
    await enterApp(me);
  }

  // Same in-app browser pattern as Profile's legal rows.
  async function openLegalPage(url: string) {
    try {
      await WebBrowser.openBrowserAsync(url);
    } catch {
      Alert.alert("Page temporarily unavailable", "Try again later.");
    }
  }

  async function handleGoogleLogin() {
    setError(null);
    setLoadingGoogle(true);
    try {
      const redirectTo = getAuthRedirectUrl();

      if (__DEV__) {
        // Must match a Redirect URL in Supabase → Authentication → URL
        // Configuration exactly (or via a `hora://**` wildcard). A mismatch
        // makes Supabase fall back to the Site URL, so the code never reaches
        // the app — or reaches it detached from this flow.
        console.log("[auth] redirectTo:", redirectTo);
        console.log("[auth] pkce challenge method:", hasWebCryptoSupport() ? "s256" : "plain");
      }

      // skipBrowserRedirect keeps supabase-js from navigating anything itself:
      // it writes the code verifier, hands back the provider URL, and we drive
      // the browser. openAuthSessionAsync then captures the redirect back to
      // `redirectTo` and returns it to this same function — no deep-link
      // listener, no relaunch, so the verifier written above is still the one
      // in SecureStore when we exchange below.
      const { data, error: oauthError } = await supabase.auth.signInWithOAuth({
        provider: "google",
        options: { redirectTo, skipBrowserRedirect: true },
      });
      if (oauthError || !data?.url) {
        throw oauthError ?? new Error("Could not start Google sign-in");
      }

      if (__DEV__) console.log("[auth] authorize url:", data.url);

      const result = await WebBrowser.openAuthSessionAsync(data.url, redirectTo);
      if (__DEV__) console.log("[auth] browser result:", result.type);
      // cancel / dismiss are user-initiated: back out quietly.
      if (result.type !== "success" || !result.url) return;

      const session = await completeSessionFromUrl(result.url);
      if (!session?.access_token) throw new Error("Google sign-in returned no session");
      await finishLogin(session.access_token);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Google sign-in failed");
    } finally {
      setLoadingGoogle(false);
    }
  }

  async function handleSendCode() {
    setError(null);
    setLoadingSendCode(true);
    try {
      const { error: otpError } = await supabase.auth.signInWithOtp({
        email,
        options: { shouldCreateUser: true },
      });
      if (otpError) throw otpError;
      setCodeSent(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not send code");
    } finally {
      setLoadingSendCode(false);
    }
  }

  async function handleVerifyCode() {
    setError(null);
    setLoadingVerifyCode(true);
    try {
      const { data, error: verifyError } = await supabase.auth.verifyOtp({
        email,
        token: code,
        type: "email",
      });
      if (!verifyError && data.session?.access_token) {
        await finishLogin(data.session.access_token);
        return;
      }

      // Supabase turned the code down. Usually that's a typo — but it is also
      // what the App Review account always gets, because its code is a fixed
      // one the backend checks itself (server/review_account.go) rather than
      // an OTP Supabase ever issued. Asking the backend here is what keeps the
      // review email out of the app bundle: it answers 401 for every address
      // but the one it is configured with, so for real users this is a wasted
      // round trip and nothing more.
      try {
        await enterApp(await reviewLogin(email, code));
        return;
      } catch {
        // Not the review account — fall through to the real OTP error.
      }
      throw verifyError ?? new Error("Invalid or expired code");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Invalid or expired code");
    } finally {
      setLoadingVerifyCode(false);
    }
  }

  return (
    // Scrolls (rather than a fixed View) so the code step still reaches its
    // Verify button on short devices once the keyboard has taken half the screen.
    <Screen avoidKeyboard center>
      <View className="mb-8 items-center">
        <Logo height={40} />
      </View>

      {error && <Text className="mb-4 text-center text-caption text-danger">{error}</Text>}

      <View className="mb-4 flex-row items-center">
        <Checkbox
          checked={consented}
          onChange={setConsented}
          accessibilityLabel="I agree to the Terms of Use and Privacy Policy"
        />
        <Text className="ml-1 flex-1 text-caption text-muted">
          I agree to the{" "}
          <Text className="text-brand underline" onPress={() => openLegalPage(LEGAL_URLS.terms)}>
            Terms of Use
          </Text>{" "}
          and{" "}
          <Text className="text-brand underline" onPress={() => openLegalPage(LEGAL_URLS.privacy)}>
            Privacy Policy
          </Text>
        </Text>
      </View>

      <PressableScale
        onPress={handleGoogleLogin}
        disabled={loadingGoogle || !consented}
        className={`mb-4 h-[52px] flex-row items-center justify-center rounded-pill bg-ink ${
          consented ? "" : "opacity-40"
        }`}
      >
        {loadingGoogle ? (
          <ActivityIndicator color={color.white} />
        ) : (
          <Text className="text-body font-semibold text-white">Continue with Google</Text>
        )}
      </PressableScale>

      <View className="my-4 h-px bg-line" />

      {codeSent ? (
        <>
          <Text className="mb-4 text-center text-caption text-muted">
            Enter the code sent to {email}
          </Text>
          <Input
            value={code}
            onChangeText={setCode}
            placeholder="123456"
            keyboardType="number-pad"
            maxLength={6}
            className="mb-4 text-center tracking-widest"
          />
          <PressableScale
            onPress={handleVerifyCode}
            disabled={loadingVerifyCode || code.length !== 6}
            className="h-[52px] flex-row items-center justify-center rounded-pill border border-line"
          >
            {loadingVerifyCode ? (
              <ActivityIndicator />
            ) : (
              <Text className="text-body font-semibold text-ink">Verify code</Text>
            )}
          </PressableScale>
        </>
      ) : (
        <>
          <Input
            value={email}
            onChangeText={setEmail}
            placeholder="you@example.com"
            autoCapitalize="none"
            keyboardType="email-address"
            className="mb-4"
          />
          <PressableScale
            onPress={handleSendCode}
            disabled={loadingSendCode || !email || !consented}
            className={`h-[52px] flex-row items-center justify-center rounded-pill border border-line ${
              consented ? "" : "opacity-40"
            }`}
          >
            {loadingSendCode ? (
              <ActivityIndicator />
            ) : (
              <Text className="text-body font-semibold text-ink">Send code</Text>
            )}
          </PressableScale>
        </>
      )}
    </Screen>
  );
}
