import { Text, View } from "react-native";
import { useRouter } from "expo-router";
import { Bell, ChevronLeft } from "lucide-react-native";
import { EmptyState, PressableScale, Screen } from "../components/ui";
import { color, size } from "../theme/tokens";

export default function Notifications() {
  const router = useRouter();

  return (
    <Screen scroll={false}>
      <View className="mb-6 mt-4 flex-row items-center">
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="ml-1 text-title font-semibold text-ink">Notifications</Text>
      </View>
      <EmptyState icon={Bell} title="No notifications yet" caption="Coming in Build phase" />
    </Screen>
  );
}
