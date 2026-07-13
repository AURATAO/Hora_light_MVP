import "../global.css";
import { createContext, useContext, useEffect, useState } from "react";
import { Stack } from "expo-router";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { supabase } from "../lib/supabase";
import { getMe } from "../lib/api";

type AuthState = {
  loading: boolean;
  authenticated: boolean;
};

const AuthContext = createContext<AuthState>({ loading: true, authenticated: false });

export function useAuthState() {
  return useContext(AuthContext);
}

export default function RootLayout() {
  const [state, setState] = useState<AuthState>({ loading: true, authenticated: false });

  useEffect(() => {
    let mounted = true;

    async function checkSession() {
      try {
        const me = await getMe();
        if (mounted) setState({ loading: false, authenticated: me.auth });
      } catch {
        if (mounted) setState({ loading: false, authenticated: false });
      }
    }

    checkSession();

    const { data: authListener } = supabase.auth.onAuthStateChange(() => {
      checkSession();
    });

    return () => {
      mounted = false;
      authListener.subscription.unsubscribe();
    };
  }, []);

  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <SafeAreaProvider>
        <AuthContext.Provider value={state}>
          {/* Every screen builds its own header (Screen's headline, or a custom
              back/close row) rather than native Stack chrome — consistent with
              every existing screen in the app. */}
          <Stack screenOptions={{ headerShown: false }}>
            <Stack.Screen name="post-task" options={{ presentation: "modal" }} />
          </Stack>
        </AuthContext.Provider>
      </SafeAreaProvider>
    </GestureHandlerRootView>
  );
}
