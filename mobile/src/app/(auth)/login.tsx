import { useState } from "react";
import { View, Text, ActivityIndicator, Alert } from "react-native";
import { useRouter } from "expo-router";
import * as WebBrowser from "expo-web-browser";
import * as Linking from "expo-linking";
import * as SecureStore from "expo-secure-store";
import { makeRedirectUri } from "expo-auth-session";
import { supabase } from "../../lib/supabase";
import { apiFetch } from "../../lib/api";
import { Screen, Input, PressableScale, Logo, Checkbox } from "../../components/ui";
import { LEGAL_URLS } from "../../lib/constants";
import { color } from "../../theme/tokens";
import { useAuthState } from "../_layout";

WebBrowser.maybeCompleteAuthSession();

// Google's callback comes back as a `?code=` param (PKCE) via WebBrowser's
// result URL, which we exchange for a Supabase session directly.
async function completeSessionFromUrl(url: string) {
  const { queryParams } = Linking.parse(url);
  const code = typeof queryParams?.code === "string" ? queryParams.code : undefined;
  if (!code) return null;

  const { data, error } = await supabase.auth.exchangeCodeForSession(code);
  if (error) throw error;
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

  async function finishLogin(accessToken: string) {
    const me = await apiFetch<{ id?: string; email?: string }>("/auth/exchange", {
      method: "POST",
      body: { access_token: accessToken },
    });
    if (me?.id) await SecureStore.setItemAsync("hora_user_id", String(me.id));
    if (me?.email) await SecureStore.setItemAsync("hora_user_email", String(me.email));
    // Route through the root gate (index) rather than straight to the tabs, so
    // a new / incomplete user lands in onboarding. refresh() populates the
    // cached profile first, so the gate reads it without a bounce back here.
    await refresh();
    router.replace("/");
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
      const redirectTo = makeRedirectUri({ scheme: "hora" });
      const { data, error: oauthError } = await supabase.auth.signInWithOAuth({
        provider: "google",
        options: { redirectTo, skipBrowserRedirect: true },
      });
      if (oauthError || !data?.url) {
        throw oauthError ?? new Error("Could not start Google sign-in");
      }

      const result = await WebBrowser.openAuthSessionAsync(data.url, redirectTo);
      if (result.type !== "success" || !result.url) return;

      const session = await completeSessionFromUrl(result.url);
      if (session?.access_token) await finishLogin(session.access_token);
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
      if (verifyError) throw verifyError;
      if (data.session?.access_token) await finishLogin(data.session.access_token);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Invalid or expired code");
    } finally {
      setLoadingVerifyCode(false);
    }
  }

  return (
    <Screen scroll={false} className="justify-center">
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
