import { useEffect } from "react";
import { Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import * as SplashScreen from "expo-splash-screen";
import { TriangleAlert } from "lucide-react-native";
import { color, size } from "../theme/tokens";

export interface ConfigErrorProps {
  /** Names of the EXPO_PUBLIC_* variables that came back undefined. */
  missing: readonly string[];
}

// Shown instead of the app when a build shipped without its EXPO_PUBLIC_*
// environment (see missingEnvVars in lib/supabase). This is a build defect, not
// a user error and not a transient one — there is nothing to retry, so unlike
// ErrorBoundary this screen offers no action. It names the missing variables on
// purpose: the only people who can ever see it are whoever installed a
// misconfigured internal build, and that name is the whole diagnosis.
export function ConfigError({ missing }: ConfigErrorProps) {
  // The root layout calls preventAutoHideAsync() at module scope and normally
  // hides the splash once SplashCollision lays out. That never mounts on this
  // path, so without this the native splash would sit on top of this screen
  // forever and the build would still look like a silent hang.
  useEffect(() => {
    SplashScreen.hideAsync().catch(() => {});
  }, []);

  return (
    <SafeAreaView className="flex-1 items-center justify-center gap-4 bg-page px-6">
      <TriangleAlert color={color.danger} size={32} strokeWidth={size.iconStroke} />
      <Text className="text-title font-semibold text-ink">Build misconfigured</Text>
      <Text className="text-center text-body text-muted">
        This build was compiled without its environment settings, so it can&rsquo;t start. Reinstall
        a correctly configured build — updating or reopening this one won&rsquo;t help.
      </Text>
      <View className="items-center gap-1">
        <Text className="text-caption text-muted">Missing at build time</Text>
        {missing.map((name) => (
          <Text key={name} className="text-caption text-ink">
            {name}
          </Text>
        ))}
      </View>
    </SafeAreaView>
  );
}
