import { useState } from "react";
import { Text, View } from "react-native";
import { useRouter } from "expo-router";
import { Button, Screen } from "../../components/ui";
import { ApiError, updateProfile } from "../../lib/api";
import { BETA_NOTICE_COPY } from "../../lib/beta-notice";
import { needsProfileCompletion } from "../../lib/onboarding";
import { useAuthState } from "../_layout";

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

      <Text className="text-display text-ink">{BETA_NOTICE_COPY.heading}</Text>

      {/* Terms come from lib/beta-notice so this screen and the Post Task
          notice can never describe different rounds. */}
      {BETA_NOTICE_COPY.intro.map((paragraph, i) => (
        <Text key={paragraph} className={i === 0 ? "mt-4 text-body text-muted" : "mt-3 text-body text-muted"}>
          {paragraph}
        </Text>
      ))}

      <View className="mt-6 gap-3">
        {BETA_NOTICE_COPY.points.map((point) => (
          <View key={point} className="flex-row gap-3">
            <View className="mt-2 h-1 w-1 rounded-pill bg-muted" />
            <Text className="flex-1 text-body text-ink">{point}</Text>
          </View>
        ))}
      </View>

      <Text className="mt-6 text-caption text-muted">{BETA_NOTICE_COPY.finePrint}</Text>

      {error ? <Text className="mt-4 text-caption text-danger">{error}</Text> : null}

      <View className="mb-8 mt-8">
        <Button label="I understand, continue" onPress={handleContinue} loading={submitting} />
      </View>
    </Screen>
  );
}
