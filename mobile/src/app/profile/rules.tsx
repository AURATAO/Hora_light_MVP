import { Linking, Text } from "react-native";
import { useRouter } from "expo-router";
import { ChevronLeft } from "lucide-react-native";
import { SupporterRules } from "../../components/SupporterRules";
import { PressableScale, Screen } from "../../components/ui";
import { color, size } from "../../theme/tokens";

/** Profile → Supporter rules. Safety first: what a supporter may always do,
 *  and what they never do. */
export default function SupporterRulesScreen() {
  const router = useRouter();
  return (
    <Screen>
      <PressableScale
        onPress={() => router.back()}
        hitSlop={8}
        accessibilityRole="button"
        accessibilityLabel="Back"
        className="mb-4 mt-2 min-h-11 flex-row items-center"
      >
        <ChevronLeft color={color.ink} size={20} strokeWidth={size.iconStroke} />
        <Text className="text-body text-ink">Back</Text>
      </PressableScale>

      <Text className="mb-2 text-display text-ink">Supporter rules</Text>
      <Text className="mb-6 text-body text-muted">Your safety comes first, on every task.</Text>

      <SupporterRules />

      <Text className="mt-6 text-caption text-muted">
        To report someone, use the ⋯ menu on the task or in the chat. In an emergency, call 911.
      </Text>
      <PressableScale onPress={() => Linking.openURL("tel:911")} hitSlop={8} className="mt-2 min-h-11 justify-center">
        <Text className="text-caption font-semibold text-danger">Call 911</Text>
      </PressableScale>
    </Screen>
  );
}
