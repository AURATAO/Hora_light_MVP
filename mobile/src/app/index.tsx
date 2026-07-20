import { View, Text, ActivityIndicator } from "react-native";
import { Redirect } from "expo-router";
import { useAuthState } from "./_layout";
import { needsBetaAccept, needsProfileCompletion } from "../lib/onboarding";
import { Button } from "../components/ui";
import { color } from "../theme/tokens";

// The post-login gate. Runs on every launch and reads the fresh server
// profile, so a user who closes the app mid-onboarding resumes at the right
// step: not beta-accepted → beta welcome; missing required fields → complete
// profile; otherwise into the tabs. Returning, complete users pass straight
// through and never see either onboarding screen.
export default function Index() {
  const { loading, authenticated, profile, refresh } = useAuthState();

  if (loading) {
    // Full-screen boot is the one exception to "never a centered spinner" (DESIGN.md §4).
    return (
      <View className="flex-1 items-center justify-center bg-page">
        <ActivityIndicator color={color.ink} />
      </View>
    );
  }

  if (!authenticated) {
    return <Redirect href="/(auth)/login" />;
  }

  // Authenticated but the profile fetch failed — we can't decide the gate
  // without it, so offer a retry instead of guessing.
  if (!profile) {
    return (
      <View className="flex-1 items-center justify-center gap-4 bg-page px-6">
        <Text className="text-center text-body text-muted">
          We couldn't load your account. Check your connection and try again.
        </Text>
        <Button label="Retry" variant="secondary" onPress={() => refresh()} />
      </View>
    );
  }

  if (needsBetaAccept(profile)) {
    return <Redirect href="/(onboarding)/beta" />;
  }

  if (needsProfileCompletion(profile)) {
    return <Redirect href="/(onboarding)/complete-profile" />;
  }

  return <Redirect href="/(tabs)/home" />;
}
