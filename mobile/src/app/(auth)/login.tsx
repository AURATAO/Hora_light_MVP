import { useEffect, useState } from "react";
import { View, Text, TextInput, Pressable, ActivityIndicator } from "react-native";
import { useRouter } from "expo-router";
import * as WebBrowser from "expo-web-browser";
import * as Linking from "expo-linking";
import * as SecureStore from "expo-secure-store";
import { makeRedirectUri } from "expo-auth-session";
import { supabase } from "../../lib/supabase";
import { apiFetch } from "../../lib/api";

WebBrowser.maybeCompleteAuthSession();

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
  const [email, setEmail] = useState("");
  const [magicLinkSent, setMagicLinkSent] = useState(false);
  const [loadingGoogle, setLoadingGoogle] = useState(false);
  const [loadingEmail, setLoadingEmail] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function finishLogin(accessToken: string) {
    const me = await apiFetch("/auth/exchange", {
      method: "POST",
      body: { access_token: accessToken },
    });
    if (me?.id) await SecureStore.setItemAsync("hora_user_id", String(me.id));
    if (me?.email) await SecureStore.setItemAsync("hora_user_email", String(me.email));
    router.replace("/(tabs)/home");
  }

  // Magic link returns to the app via the `hora://` deep link; catch it both
  // on cold start and while the app is already open.
  useEffect(() => {
    async function handleUrl(url: string | null) {
      if (!url) return;
      try {
        const session = await completeSessionFromUrl(url);
        if (session?.access_token) await finishLogin(session.access_token);
      } catch (e) {
        setError(e instanceof Error ? e.message : "Could not complete sign-in");
      }
    }

    const subscription = Linking.addEventListener("url", ({ url }) => handleUrl(url));
    Linking.getInitialURL().then(handleUrl);

    return () => subscription.remove();
  }, []);

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

  async function handleSendMagicLink() {
    setError(null);
    setLoadingEmail(true);
    try {
      const redirectTo = makeRedirectUri({ scheme: "hora" });
      const { error: otpError } = await supabase.auth.signInWithOtp({
        email,
        options: { emailRedirectTo: redirectTo },
      });
      if (otpError) throw otpError;
      setMagicLinkSent(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not send magic link");
    } finally {
      setLoadingEmail(false);
    }
  }

  return (
    <View className="flex-1 justify-center bg-neutralbg px-6">
      <Text className="mb-8 text-center text-3xl font-bold text-primary">HO:RA</Text>

      {error && <Text className="mb-4 text-center text-danger">{error}</Text>}

      <Pressable
        onPress={handleGoogleLogin}
        disabled={loadingGoogle}
        className="mb-4 items-center rounded-lg bg-primary py-3"
      >
        {loadingGoogle ? (
          <ActivityIndicator color="#F9FAFB" />
        ) : (
          <Text className="font-semibold text-neutralbg">Continue with Google</Text>
        )}
      </Pressable>

      <View className="my-4 h-px bg-border" />

      {magicLinkSent ? (
        <Text className="text-center text-secondary">
          Check {email} for a sign-in link.
        </Text>
      ) : (
        <>
          <TextInput
            value={email}
            onChangeText={setEmail}
            placeholder="you@example.com"
            autoCapitalize="none"
            keyboardType="email-address"
            className="mb-4 rounded-lg border border-border px-4 py-3"
          />
          <Pressable
            onPress={handleSendMagicLink}
            disabled={loadingEmail || !email}
            className="items-center rounded-lg border border-primary py-3"
          >
            {loadingEmail ? (
              <ActivityIndicator />
            ) : (
              <Text className="font-semibold text-primary">Send magic link</Text>
            )}
          </Pressable>
        </>
      )}
    </View>
  );
}
