import { useCallback, useEffect, useState } from "react";
import { RefreshControl, ScrollView, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import * as SecureStore from "expo-secure-store";
import { ArrowRight, Bell, CircleAlert, ClipboardList, UserRound } from "lucide-react-native";
import { Badge, Card, EmptyState, Pill, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getMe, getPostedTasks } from "../../lib/api";
import { getCategoryMeta } from "../../lib/categories";
import { deriveTaskStatus, formatRelativeTime } from "../../lib/task-utils";
import type { Task, TaskCategory } from "../../lib/types";
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

const STATUS_LABEL: Record<ReturnType<typeof deriveTaskStatus>, string> = {
  open: "Open",
  assigned: "Assigned",
  completed: "Completed",
  cancelled: "Cancelled",
};

function TaskRow({ task, onPress }: { task: Task; onPress: () => void }) {
  const status = deriveTaskStatus(task);
  const meta = getCategoryMeta(task.category);
  const Icon = meta.icon;

  return (
    <Card className="mb-3" onPress={onPress}>
      <View className="flex-row items-center justify-between">
        <View className="mr-2 flex-1 flex-row items-center gap-2">
          <Icon color={color.muted} size={18} strokeWidth={size.iconStroke} />
          <Text className="flex-1 text-body font-semibold text-ink" numberOfLines={1}>
            {task.title}
          </Text>
        </View>
        {status === "assigned" ? (
          <UserRound color={color.muted} size={16} strokeWidth={size.iconStroke} />
        ) : null}
      </View>
      <View className="mt-2 flex-row items-center justify-between">
        <Text className="text-caption text-muted">
          {meta.label} · {formatRelativeTime(task.created_at)}
        </Text>
        <Badge label={STATUS_LABEL[status]} variant="success" />
      </View>
    </Card>
  );
}

export default function Home() {
  const router = useRouter();
  const [firstName, setFirstName] = useState<string | null>(null);
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  useEffect(() => {
    let mounted = true;

    async function loadGreeting() {
      try {
        const me = await getMe();
        if (!mounted) return;
        if (!me.auth) {
          router.replace("/(auth)/login");
          return;
        }
        const first = me.name.trim().split(/\s+/)[0] || "there";
        setFirstName(first);
        SecureStore.setItemAsync("hora_user_name", first).catch(() => {});
      } catch (e) {
        if (e instanceof ApiError && e.isAuthError) {
          router.replace("/(auth)/login");
          return;
        }
        const cached = await SecureStore.getItemAsync("hora_user_name").catch(() => null);
        if (mounted) setFirstName(cached ?? "there");
      }
    }

    loadGreeting();
    return () => {
      mounted = false;
    };
  }, [router]);

  const loadTasks = useCallback(async () => {
    try {
      const posted = await getPostedTasks();
      setTasks(posted);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setError(e instanceof Error ? e.message : "Couldn't load your tasks");
    }
  }, [router]);

  useFocusEffect(
    useCallback(() => {
      loadTasks();
    }, [loadTasks])
  );

  async function onRefresh() {
    setRefreshing(true);
    await loadTasks();
    setRefreshing(false);
  }

  function goToPostTask(category?: TaskCategory) {
    if (category) {
      router.push({ pathname: "/post-task", params: { category } });
    } else {
      router.push("/post-task");
    }
  }

  return (
    <Screen
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
      }
    >
      <View className="mb-6 mt-4 flex-row items-center justify-between">
        <Text className="text-display text-ink">Hi {firstName ?? "there"}</Text>
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

      {tasks === null && !error ? (
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : tasks === null && error ? (
        <EmptyState
          icon={CircleAlert}
          title="Couldn't load your tasks"
          caption={error}
          actionLabel="Retry"
          onAction={loadTasks}
        />
      ) : tasks && tasks.length === 0 ? (
        <EmptyState
          icon={ClipboardList}
          title="No active tasks"
          caption="Post your first one!"
          actionLabel="Post a task"
          onAction={() => goToPostTask()}
        />
      ) : (
        <View>
          {tasks?.map((task) => (
            <TaskRow key={task.id} task={task} onPress={() => router.push(`/task/${task.id}`)} />
          ))}
        </View>
      )}
    </Screen>
  );
}
