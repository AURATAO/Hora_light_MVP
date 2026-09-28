import { useEffect, useState } from "react";
import { View, Text, ActivityIndicator, Alert, Platform } from "react-native";
import { useRouter } from "expo-router";
import * as WebBrowser from "expo-web-browser";
import * as SecureStore from "expo-secure-store";
import * as AppleAuthentication from "expo-apple-authentication";
import * as Crypto from "expo-crypto";
import { supabase } from "../../lib/supabase";
import { hasWebCryptoSupport } from "../../lib/crypto-polyfill";
import { getAuthRedirectUrl } from "../../lib/auth-redirect";
import {
  apiFetch,
  getProfile,
  reviewLogin,
  updateProfile,
  type SessionIdentity,
} from "../../lib/api";
import {
  Screen,
  Input,
  PressableScale,
  Logo,
  Checkbox,
  ProviderButton,
  GoogleLogo,
} from "../../components/ui";
import { LEGAL_URLS } from "../../lib/constants";
import { radius, size } from "../../theme/tokens";
import { RESEND_COOLDOWN_MS, resendLabel, resendSecondsLeft } from "../../lib/otp-resend";
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

// Sign in with Apple binds the identity token to a nonce: the RAW nonce goes
// to Supabase (which checks it against the token), the SHA-256 of it goes to
// Apple (which embeds it in the token). supabase-js 2.110 has no
// generateRawNonce(), so the raw value is 32 random bytes from expo-crypto,
// hex-encoded.
async function makeAppleNonce() {
  const bytes = await Crypto.getRandomBytesAsync(32);
  const rawNonce = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  const hashedNonce = await Crypto.digestStringAsync(
    Crypto.CryptoDigestAlgorithm.SHA256,
    rawNonce
  );
  return { rawNonce, hashedNonce };
}

// Apple only hands the name over on the very FIRST authorization for this
// app; every later sign-in returns null parts. Joined and trimmed so a
// given-name-only account does not end up as "Ada " (or " Lovelace").
function appleDisplayName(fullName: AppleAuthentication.AppleAuthenticationFullName | null) {
  if (!fullName) return "";
  return [fullName.givenName, fullName.familyName]
    .filter((part): part is string => !!part && part.trim().length > 0)
    .map((part) => part.trim())
    .join(" ");
}

// The user backed out of the Apple sheet (or the system dismissed it): not an
// error, nothing to show.
function isAppleCancel(e: unknown) {
  return (
    typeof e === "object" && e !== null && (e as { code?: unknown }).code === "ERR_REQUEST_CANCELED"
  );
}

export default function Login() {
  const router = useRouter();
  const { refresh } = useAuthState();
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [codeSent, setCodeSent] = useState(false);
  // The native Apple button is only rendered where Apple sign-in can actually
  // complete: iOS 13+ with the capability on. Android and Expo Go never see it.
  const [appleAvailable, setAppleAvailable] = useState(false);
  const [loadingApple, setLoadingApple] = useState(false);
  const [loadingGoogle, setLoadingGoogle] = useState(false);
  const [loadingSendCode, setLoadingSendCode] = useState(false);
  const [loadingVerifyCode, setLoadingVerifyCode] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Consent is deliberately screen-local, not persisted: the user re-confirms
  // on every login, which is the standard pattern for this gate.
  const [consented, setConsented] = useState(false);
  // When "Resend code" becomes tappable again (ms epoch); 0 = never sent.
  // Ticked once a second only while a countdown is on screen.
  const [resendReadyAt, setResendReadyAt] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  const resendWait = resendSecondsLeft(resendReadyAt, now);

  useEffect(() => {
    if (!codeSent || resendWait === 0) return undefined;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [codeSent, resendWait]);

  useEffect(() => {
    if (Platform.OS !== "ios") return undefined;
    let alive = true;
    AppleAuthentication.isAvailableAsync()
      .then((available) => {
        if (alive) setAppleAvailable(available);
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  // Every login path converges here once the backend has set the hora_session
  // cookie. Routes through the root gate (index) rather than straight to the
  // tabs, so a new / incomplete user lands in onboarding. refresh() populates
  // the cached profile first, so the gate reads it without a bounce back here.
  async function enterApp(me: SessionIdentity) {
    if (me?.id) await SecureStore.setItemAsync("hora_user_id", String(me.id));
    if (me?.email) await SecureStore.setItemAsync("hora_user_email", String(me.email));
    // Every path here is behind the consent checkbox (both sign-in buttons
    // are disabled until it is ticked), so this records an answer the person
    // has just given. Best-effort: the server keeps the FIRST acceptance, and
    // a failed write must not keep anybody out of the app.
    await updateProfile({ terms_accepted: true }).catch(() => undefined);
    await refresh();
    router.replace("/");
  }

  // `beforeEnter` runs once the hora_session cookie exists and before the
  // profile is cached + the gate routes — the one place a login path can
  // patch the profile and have enterApp's refresh() pick the change up.
  async function finishLogin(accessToken: string, beforeEnter?: () => Promise<void>) {
    const me = await apiFetch<SessionIdentity>("/auth/exchange", {
      method: "POST",
      body: { access_token: accessToken },
    });
    if (beforeEnter) await beforeEnter();
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

  // Native Sign in with Apple (App Store Guideline 4.8). No browser round
  // trip: the system sheet hands back an identity token which Supabase
  // verifies directly, so unlike Google there is no PKCE verifier to keep
  // alive across a redirect.
  async function handleAppleLogin() {
    // The native button has no disabled prop, so the consent gate is enforced
    // here as well as by the pointerEvents wrapper around it.
    if (!consented || loadingApple) return;
    setError(null);
    setLoadingApple(true);
    try {
      const { rawNonce, hashedNonce } = await makeAppleNonce();
      const credential = await AppleAuthentication.signInAsync({
        requestedScopes: [
          AppleAuthentication.AppleAuthenticationScope.FULL_NAME,
          AppleAuthentication.AppleAuthenticationScope.EMAIL,
        ],
        nonce: hashedNonce,
      });
      if (!credential.identityToken) throw new Error("Apple sign-in returned no identity token");

      const { data, error: idTokenError } = await supabase.auth.signInWithIdToken({
        provider: "apple",
        token: credential.identityToken,
        nonce: rawNonce,
      });
      if (idTokenError) throw idTokenError;
      if (!data.session?.access_token) throw new Error("Apple sign-in returned no session");

      // Apple sends the name exactly once (first authorization), and Supabase
      // does not carry it into user metadata for id-token sign-ins, so it is
      // written to the profile here — only when the profile has no name yet,
      // and best-effort: the login must not fail on a profile write.
      const name = appleDisplayName(credential.fullName);
      await finishLogin(data.session.access_token, async () => {
        if (!name) return;
        try {
          const profile = await getProfile();
          if (profile.name?.trim()) return;
          await updateProfile({ name });
        } catch {
          // Name backfill is a nicety; the onboarding gate asks anyway.
        }
      });
    } catch (e) {
      if (isAppleCancel(e)) return;
      setError(e instanceof Error ? e.message : "Apple sign-in failed");
    } finally {
      setLoadingApple(false);
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

  // Sends (or re-sends) the code to the address in the field. A fresh send
  // always clears whatever was typed in the code box: the old code is dead
  // the moment a new one is issued, and six stale digits left in place is how
  // "Invalid code" gets reported against a working address.
  async function handleSendCode() {
    if (loadingSendCode) return;
    setError(null);
    setLoadingSendCode(true);
    try {
      const { error: otpError } = await supabase.auth.signInWithOtp({
        email,
        options: { shouldCreateUser: true },
      });
      if (otpError) throw otpError;
      setCode("");
      setCodeSent(true);
      setResendReadyAt(Date.now() + RESEND_COOLDOWN_MS);
      setNow(Date.now());
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not send code");
      // The code field is shown anyway. A send that Supabase refused (its
      // email rate limit, most often) is still worth a code the person
      // already holds — a previous send's, or the App Review account's fixed
      // one, which no send ever issues (server/review_account.go). Without
      // this the reviewer could be locked out of the app by an email quota.
      setCode("");
      setCodeSent(true);
    } finally {
      setLoadingSendCode(false);
    }
  }

  // Back to the email step with the address still in the field and
  // editable. Everything about the pending code goes — the digits, the
  // cooldown, the error — because the next send may be for a different
  // address and must not inherit a countdown that belonged to the old one.
  function useDifferentEmail() {
    setCode("");
    setError(null);
    setResendReadyAt(0);
    setCodeSent(false);
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

      {/* The provider pair. Both keep their official logo + text form (Apple
          HIG; App Store Guideline 4.8 wants Sign in with Apple no less
          prominent than the other options), and the Google button is built
          to the native Apple button's geometry — same height, radius, type
          size, logo size — so the two read as one pair. Apple first, on the
          same 4.8 grounds. */}
      {appleAvailable && (
        // Apple's own button (Guideline 4.8 wants the system-rendered one,
        // not a look-alike). It takes no `disabled` and no className, so the
        // consent gate is a pointerEvents wrapper at the same 40% opacity as
        // ProviderButton's, and the height is an inline style — the native
        // view ignores NativeWind's h-[...]. The `cornerRadius` prop is the
        // button's own radius (pill); `style` must not carry borderRadius or
        // backgroundColor.
        <View
          className={`mb-3 ${consented ? "" : "opacity-40"}`}
          pointerEvents={consented && !loadingApple ? "auto" : "none"}
          accessibilityState={{ disabled: !consented || loadingApple }}
        >
          <AppleAuthentication.AppleAuthenticationButton
            buttonType={AppleAuthentication.AppleAuthenticationButtonType.CONTINUE}
            buttonStyle={AppleAuthentication.AppleAuthenticationButtonStyle.BLACK}
            cornerRadius={radius.pill}
            style={{ height: size.providerButtonHeight }}
            onPress={handleAppleLogin}
          />
        </View>
      )}

      <ProviderButton
        label="Continue with Google"
        logo={<GoogleLogo size={size.providerLogo} />}
        onPress={handleGoogleLogin}
        disabled={!consented}
        loading={loadingGoogle}
      />

      <View className="my-6 h-px bg-line" />

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

          {/* The two ways out of a code screen that used to have none: a
              mistyped address goes back to an editable field, and a lost
              email is re-sent to the same one behind a 30-second cooldown so
              a tap-tap-tap does not get the address rate-limited. Text
              actions, not a second solid button (DESIGN.md §1). */}
          <View className="mt-4 flex-row items-center justify-between">
            <PressableScale
              onPress={useDifferentEmail}
              disabled={loadingVerifyCode || loadingSendCode}
              hitSlop={8}
              className="min-h-11 justify-center"
              accessibilityRole="button"
              accessibilityLabel="Use a different email"
            >
              <Text className="text-caption font-semibold text-brand">Use a different email</Text>
            </PressableScale>
            <PressableScale
              onPress={handleSendCode}
              disabled={loadingSendCode || loadingVerifyCode || resendWait > 0}
              hitSlop={8}
              className="min-h-11 justify-center"
              accessibilityRole="button"
              accessibilityLabel={resendLabel(resendReadyAt, now)}
              accessibilityLiveRegion="polite"
            >
              <Text
                className={`text-caption font-semibold ${resendWait > 0 ? "text-muted" : "text-brand"}`}
              >
                {loadingSendCode ? "Sending…" : resendLabel(resendReadyAt, now)}
              </Text>
            </PressableScale>
          </View>
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
