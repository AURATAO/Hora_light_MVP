import { ScrollView, Text, View } from "react-native";
import { useRouter } from "expo-router";
import { ArrowRight, Bell, ClipboardList } from "lucide-react-native";
import { EmptyState, Pill, PressableScale, Screen } from "../../components/ui";
import { getCategoryMeta } from "../../lib/categories";
import type { TaskCategory } from "../../lib/types";
import { color, size } from "../../theme/tokens";

// Fast-access subset of TaskCategory for Home's shortcut row — the full set
// lives behind "What do you need?" once task creation is built.
const HOME_CATEGORIES: TaskCategory[] = [
  "grocery",
  "delivery",
  "laundry",
  "queue",
  "companionship",
  "quick_errand",
];

export default function Home() {
  const router = useRouter();

  function goToPostTask(category?: TaskCategory) {
    if (category) {
      router.push({ pathname: "/post-task", params: { category } });
    } else {
      router.push("/post-task");
    }
  }

  return (
    <Screen>
      <View className="mb-6 mt-4 flex-row items-center justify-between">
        <Text className="text-display text-ink">Hi there</Text>
        <PressableScale
          onPress={() => router.push("/notifications")}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <Bell color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
      </View>

      <PressableScale
        onPress={() => goToPostTask()}
        className="mb-6 flex-row items-center justify-between rounded-card bg-ink p-6"
      >
        <Text className="text-title font-semibold text-white">What do you need?</Text>
        <ArrowRight color={color.white} size={20} strokeWidth={size.iconStroke} />
      </PressableScale>

      <ScrollView horizontal showsHorizontalScrollIndicator={false} className="-mx-6 mb-8 px-6">
        <View className="flex-row gap-2">
          {HOME_CATEGORIES.map((value) => (
            <Pill
              key={value}
              label={getCategoryMeta(value).label}
              onPress={() => goToPostTask(value)}
            />
          ))}
        </View>
      </ScrollView>

      <Text className="mb-3 text-title font-semibold text-ink">Your tasks</Text>
      <EmptyState
        icon={ClipboardList}
        title="No tasks yet"
        caption="Tasks you post will show up here."
        actionLabel="Post a task"
        onAction={() => goToPostTask()}
      />
    </Screen>
  );
}
