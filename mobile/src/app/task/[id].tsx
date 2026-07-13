import { Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import { ChevronLeft, ClipboardList } from "lucide-react-native";
import { EmptyState, PressableScale, Screen } from "../../components/ui";
import { color, size } from "../../theme/tokens";

export default function TaskDetail() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const id = Array.isArray(params.id) ? params.id[0] : params.id;

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
        <Text className="ml-1 text-title font-semibold text-ink">Task</Text>
      </View>
      <Text className="mb-6 text-caption text-muted">Task ID: {id}</Text>
      <EmptyState
        icon={ClipboardList}
        title="Task details coming soon"
        caption="Coming in Build phase"
      />
    </Screen>
  );
}
