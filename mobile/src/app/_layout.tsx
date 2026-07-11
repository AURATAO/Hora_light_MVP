import "../global.css";
import { createContext, useContext, useEffect, useState } from "react";
import { Slot } from "expo-router";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { supabase } from "../lib/supabase";
import { apiFetch } from "../lib/api";

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
        const me = await apiFetch("/auth/me");
        if (mounted) setState({ loading: false, authenticated: Boolean(me?.auth) });
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
    <SafeAreaProvider>
      <AuthContext.Provider value={state}>
        <Slot />
      </AuthContext.Provider>
    </SafeAreaProvider>
  );
}
