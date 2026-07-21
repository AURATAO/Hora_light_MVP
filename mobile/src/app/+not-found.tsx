import { View } from "react-native";
import { router } from "expo-router";
import { Compass } from "lucide-react-native";
import { Screen } from "../components/ui/Screen";
import { EmptyState } from "../components/ui/EmptyState";

// Expo Router renders this for any URL that doesn't match a route — a bad deep
// link, or a stale push target after a screen is renamed. The default is a bare
// dev-only screen; instead we speak the same EmptyState language as every other
// empty/error surface and offer one way out: back to the gate (index.tsx),
// which re-routes to login / onboarding / tabs as appropriate.
export default function NotFound() {
  return (
    <Screen scroll={false}>
      <View className="flex-1 items-center justify-center">
        <EmptyState
          icon={Compass}
          title="Page not found"
          caption="That page doesn't exist or may have moved."
          actionLabel="Go home"
          onAction={() => router.replace("/")}
        />
      </View>
    </Screen>
  );
}
