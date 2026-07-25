import "../global.css";
import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { StyleSheet, View } from "react-native";
import { Stack, router } from "expo-router";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";
import * as SplashScreen from "expo-splash-screen";
import * as Notifications from "expo-notifications";
import { supabase } from "../lib/supabase";
import { getMe, getProfile } from "../lib/api";
// Importing ./push registers the app's single notification handler at boot.
import { registerForPushNotifications, taskIdFromNotificationResponse } from "../lib/push";
import type { Profile } from "../lib/types";
import SplashCollision from "../components/SplashCollision";
import { ErrorBoundary } from "../components/ErrorBoundary";
import { OfflineBanner } from "../components/OfflineBanner";

// Hold the native splash (solid brand green with the two static gold dots)
// up until <SplashCollision> has mounted, so its static frame hands off
// seamlessly to frame 0 of the animation — no flash, no jump. See item 5.
SplashScreen.preventAutoHideAsync().catch(() => {});

type AuthState = {
  loading: boolean;
  authenticated: boolean;
  // Own profile, loaded once authenticated, so the root gate (index.tsx) can
  // route a signed-in user to onboarding vs (tabs) from `beta_accepted` and
  // the required fields. null while loading, when signed out, or if the
  // profile fetch failed while authenticated.
  profile: Profile | null;
};

type AuthContextValue = AuthState & {
  // Re-run the session + profile check. Awaited by the login flow (so the gate
  // sees a fresh profile before routing) and by the onboarding screens after
  // they save, keeping the cached profile in step with the server.
  refresh: () => Promise<void>;
};

const AuthContext = createContext<AuthContextValue>({
  loading: true,
  authenticated: false,
  profile: null,
  refresh: async () => {},
});

export function useAuthState() {
  return useContext(AuthContext);
}

export default function RootLayout() {
  const [state, setState] = useState<AuthState>({
    loading: true,
    authenticated: false,
    profile: null,
  });
  const [splashDone, setSplashDone] = useState(false);

  // Fired once the splash overlay has laid out — i.e. frame 0 of the
  // animation now covers the screen at the exact same image the native
  // splash was showing. Only then do we drop the native splash.
  const onSplashLayout = useCallback(() => {
    SplashScreen.hideAsync().catch(() => {});
  }, []);

  // Resolve auth from GET /auth/me, then (if authed) load the profile so the
  // gate can read beta_accepted + required fields. A profile-fetch failure
  // keeps the user authenticated with a null profile — index.tsx shows a retry
  // rather than bouncing them to login. This is the root layout and never
  // unmounts, so no mounted-guard is needed.
  const checkSession = useCallback(async () => {
    try {
      const me = await getMe();
      if (!me.auth) {
        setState({ loading: false, authenticated: false, profile: null });
        return;
      }
      // Register this device for push once we know the user is signed in. This
      // runs on app-start-when-authenticated AND after login (finishLogin's
      // refresh() and supabase onAuthStateChange both route through here). The
      // server upsert makes repeat calls harmless. Fire-and-forget — it never
      // throws and must not gate auth resolution.
      registerForPushNotifications();
      try {
        const profile = await getProfile();
        setState({ loading: false, authenticated: true, profile });
      } catch {
        setState({ loading: false, authenticated: true, profile: null });
      }
    } catch {
      setState({ loading: false, authenticated: false, profile: null });
    }
  }, []);

  useEffect(() => {
    checkSession();

    const { data: authListener } = supabase.auth.onAuthStateChange(() => {
      checkSession();
    });

    return () => {
      authListener.subscription.unsubscribe();
    };
  }, [checkSession]);

  // Deep-link a tapped task push to /task/[id]. Covers all three states:
  // - warm/backgrounded: the response listener fires on tap;
  // - cold start (app launched by the tap): getLastNotificationResponseAsync
  //   returns the launching response. This root layout mounts once and never
  //   unmounts, so the cold-start check runs exactly once per process.
  // If the target isn't the signed-in user's task, the task screen's own auth
  // guard handles the redirect — navigation here stays dumb on purpose.
  useEffect(() => {
    Notifications.getLastNotificationResponseAsync().then((response) => {
      const taskId = taskIdFromNotificationResponse(response);
      if (taskId) router.push(`/task/${taskId}`);
    });

    const sub = Notifications.addNotificationResponseReceivedListener((response) => {
      const taskId = taskIdFromNotificationResponse(response);
      if (taskId) router.push(`/task/${taskId}`);
    });
    return () => sub.remove();
  }, []);

  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <SafeAreaProvider>
        {/* Crash guard: any render/lifecycle error below this line shows a
            friendly full-screen fallback with Restart instead of a blank
            white screen. Sits under SafeAreaProvider so the fallback can use
            the safe-area insets. */}
        <ErrorBoundary>
          <AuthContext.Provider value={{ ...state, refresh: checkSession }}>
            {/* Every screen builds its own header (Screen's headline, or a custom
                back/close row) rather than native Stack chrome — consistent with
                every existing screen in the app. */}
            <Stack screenOptions={{ headerShown: false }}>
              <Stack.Screen name="post-task" options={{ presentation: "modal" }} />
            </Stack>
            {/* App-wide offline bar — non-blocking overlay above every screen,
                below the splash. */}
            <OfflineBanner />
            {/* Opening animation, overlaid above all app content. Unmounts
                itself once onFinish fires (the circular reveal completes). */}
            {!splashDone && (
              <View style={StyleSheet.absoluteFill} onLayout={onSplashLayout}>
                <SplashCollision onFinish={() => setSplashDone(true)} />
              </View>
            )}
          </AuthContext.Provider>
        </ErrorBoundary>
      </SafeAreaProvider>
    </GestureHandlerRootView>
  );
}
