import { useCallback, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import { Briefcase, Hourglass, MapPin } from "lucide-react-native";
import { ApplySupporterSheet } from "../../components/ApplySupporterSheet";
import { Button, Card, EmptyState, Screen, Skeleton } from "../../components/ui";
import { ApiError, getAvailableTasks, getProfile } from "../../lib/api";
import { getCategoryMeta } from "../../lib/categories";
import { formatCost, formatMinutes } from "../../lib/task-utils";
import type { Profile, Task } from "../../lib/types";
import { color, size } from "../../theme/tokens";

function formatWhen(task: Task): string {
  if (task.is_immediate) return "ASAP";
  if (!task.scheduled_at) return "—";
  return new Date(task.scheduled_at).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function AvailableTaskRow({ task, onPress }: { task: Task; onPress: () => void }) {
  const meta = getCategoryMeta(task.category);
  const Icon = meta.icon;
  const location = (task.location_text ?? "").split(" | ")[0]?.trim();

  const metaParts = [meta.label, formatWhen(task)];
  if (task.estimated_minutes) metaParts.push(formatMinutes(task.estimated_minutes));
  if (task.prepay_amount_cents && task.prepay_amount_cents > 0) {
    metaParts.push(`Budget ${formatCost(task.prepay_amount_cents)}`);
  }

  return (
    <Card className="mb-3" onPress={onPress}>
      <View className="flex-row items-center gap-2">
        <Icon color={color.muted} size={18} strokeWidth={size.iconStroke} />
        <Text className="flex-1 text-body font-semibold text-ink" numberOfLines={1}>
          {task.title}
        </Text>
      </View>
      <Text className="mt-2 text-caption text-muted">{metaParts.join(" · ")}</Text>
      {location ? (
        <View className="mt-1 flex-row items-center gap-1.5">
          <MapPin color={color.muted} size={14} strokeWidth={size.iconStroke} />
          <Text className="flex-1 text-caption text-muted" numberOfLines={1}>
            {location}
          </Text>
        </View>
      ) : null}
    </Card>
  );
}

export default function Work() {
  const router = useRouter();

  const [profile, setProfile] = useState<Profile | null>(null);
  const [profileLoading, setProfileLoading] = useState(true);
  const [profileError, setProfileError] = useState<string | null>(null);

  const [tasks, setTasks] = useState<Task[]>([]);
  const [tasksLoading, setTasksLoading] = useState(true);
  const [tasksError, setTasksError] = useState<string | null>(null);

  const [refreshing, setRefreshing] = useState(false);
  const [applyOpen, setApplyOpen] = useState(false);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const loadTasks = useCallback(async () => {
    setTasksLoading(true);
    try {
      const items = await getAvailableTasks();
      setTasks(items);
      setTasksError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setTasksError(e instanceof Error ? e.message : "Couldn't load available tasks");
    } finally {
      setTasksLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loadAll = useCallback(async () => {
    try {
      const p = await getProfile();
      setProfile(p);
      setProfileError(null);
      if (p.supporter_status === "approved") {
        await loadTasks();
      } else {
        setTasksLoading(false);
      }
    } catch (e) {
      if (handleAuthError(e)) return;
      setProfileError(e instanceof Error ? e.message : "Couldn't load your profile");
    } finally {
      setProfileLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadTasks]);

  useFocusEffect(
    useCallback(() => {
      loadAll();
    }, [loadAll])
  );

  async function onRefresh() {
    setRefreshing(true);
    await loadAll();
    setRefreshing(false);
  }

  async function handleApplied() {
    setApplyOpen(false);
    await loadAll();
  }

  if (profileLoading) {
    return (
      <Screen insetForTabBar headline="Work">
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      </Screen>
    );
  }

  if (profileError && !profile) {
    return (
      <Screen insetForTabBar headline="Work">
        <EmptyState
          icon={Briefcase}
          title="Couldn't load this page"
          caption={profileError}
          actionLabel="Retry"
          onAction={loadAll}
        />
      </Screen>
    );
  }

  const status = profile?.supporter_status ?? "none";

  if (status === "none") {
    return (
      <Screen insetForTabBar headline="Work">
        <View className="gap-4 rounded-card border border-line bg-surface p-4">
          <Text className="text-title font-semibold text-ink">Become a supporter</Text>
          <Text className="text-body text-muted">Help people nearby and earn on your own schedule.</Text>
          <Button label="Apply" onPress={() => setApplyOpen(true)} />
        </View>
        <ApplySupporterSheet visible={applyOpen} onClose={() => setApplyOpen(false)} onApplied={handleApplied} />
      </Screen>
    );
  }

  if (status === "applied") {
    return (
      <Screen insetForTabBar headline="Work">
        <EmptyState icon={Hourglass} title="Application received" caption="We'll review it soon." />
      </Screen>
    );
  }

  return (
    <Screen
      insetForTabBar
      headline="Available tasks"
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}
    >
      {tasksLoading ? (
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : tasksError && tasks.length === 0 ? (
        <EmptyState
          icon={Briefcase}
          title="Couldn't load tasks"
          caption={tasksError}
          actionLabel="Retry"
          onAction={loadTasks}
        />
      ) : tasks.length === 0 ? (
        <EmptyState
          icon={Briefcase}
          title="No open tasks right now"
          caption="Check back soon — new tasks post throughout the day."
        />
      ) : (
        <View>
          {tasks.map((task) => (
            <AvailableTaskRow key={task.id} task={task} onPress={() => router.push(`/task/${task.id}`)} />
          ))}
        </View>
      )}
    </Screen>
  );
}
