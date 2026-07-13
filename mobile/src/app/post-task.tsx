import { Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import { ClipboardList, X } from "lucide-react-native";
import { EmptyState, PressableScale, Screen } from "../components/ui";
import { CATEGORIES, getCategoryMeta } from "../lib/categories";
import type { TaskCategory } from "../lib/types";
import { color, size } from "../theme/tokens";

function parseCategory(raw: string | string[] | undefined): TaskCategory | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return CATEGORIES.some((c) => c.value === value) ? (value as TaskCategory) : undefined;
}

export default function PostTask() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const category = parseCategory(params.category);

  const caption = category
    ? `Selected category: ${getCategoryMeta(category).label}`
    : "Coming in Build phase";

  return (
    <Screen scroll={false}>
      <View className="mb-6 mt-4 flex-row items-center justify-between">
        <Text className="text-title font-semibold text-ink">Post a task</Text>
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <X color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
      </View>
      <EmptyState icon={ClipboardList} title="Task details coming soon" caption={caption} />
    </Screen>
  );
}
