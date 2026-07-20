import { useState } from "react";
import { Text, View } from "react-native";
import { useRouter } from "expo-router";
import { Button, Screen } from "../../components/ui";
import { ApiError, updateProfile } from "../../lib/api";
import { needsProfileCompletion } from "../../lib/onboarding";
import { useAuthState } from "../_layout";

// Verbatim from web (app/src/components/BetaModal.jsx) so both platforms show
// the same beta terms. The dashes here are en/em-dashes, matching web exactly.
const BETA_POINTS = [
  "Service is 100% free during beta — no platform fees",
  "Coverage hours: 11am–2pm and 7pm–10pm",
  "Up to 3 tasks per day (subject to runner availability)",
  "Every task is manually reviewed by the HO:RA team before a runner is dispatched",
  "Reimbursement only: you cover actual item costs (e.g. a $5 coffee), nothing else",
  "Purchase cap: $30 max per task",
];

// Screen 1 of onboarding. Accepting sets beta_accepted, then hands off to the
// profile step if it's still incomplete, otherwise into the tabs.
export default function BetaWelcome() {
  const router = useRouter();
  const { refresh } = useAuthState();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleContinue() {
    setSubmitting(true);
    setError(null);
    try {
      const updated = await updateProfile({ beta_accepted: true });
      await refresh();
      router.replace(
        needsProfileCompletion(updated) ? "/(onboarding)/complete-profile" : "/(tabs)/home"
      );
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setError(e instanceof Error ? e.message : "Something went wrong. Try again.");
      setSubmitting(false);
    }
  }

  return (
    <Screen>
      {/* Logo slot — the colon is the one sanctioned gold on this screen (DESIGN.md §1). */}
      <View className="mb-8 mt-4 items-center">
        <Text className="text-title text-brand">
          HO<Text className="text-gold">:</Text>RA
        </Text>
      </View>

      <Text className="text-display text-ink">You're in. Welcome to HO:RA Beta.</Text>

      <Text className="mt-4 text-body text-muted">
        HO:RA is a minute-billing platform for urban support — real people helping with real tasks in
        Midtown West, NYC.
      </Text>
      <Text className="mt-3 text-body text-muted">
        This is a closed beta pilot (April 2026). Here's what to know:
      </Text>

      <View className="mt-6 gap-3">
        {BETA_POINTS.map((point) => (
          <View key={point} className="flex-row gap-3">
            <View className="mt-2 h-1 w-1 rounded-pill bg-muted" />
            <Text className="flex-1 text-body text-ink">{point}</Text>
          </View>
        ))}
      </View>

      <Text className="mt-6 text-caption text-muted">
        By continuing, you agree to use this platform responsibly and understand this is an
        early-stage test.
      </Text>

      {error ? <Text className="mt-4 text-caption text-danger">{error}</Text> : null}

      <View className="mb-8 mt-8">
        <Button label="I understand, continue" onPress={handleContinue} loading={submitting} />
      </View>
    </Screen>
  );
}
