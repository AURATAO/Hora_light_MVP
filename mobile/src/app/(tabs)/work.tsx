import { useCallback, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import { Briefcase, MapPin } from "lucide-react-native";
import { AvailabilityCard } from "../../components/AvailabilityCard";
import { askForPushInContext } from "../../lib/push";

// The board asks (or re-registers a granted device) once per app session, not
// on every focus.
let pushAskedThisSession = false;
import { SupporterStatusBanner } from "../../components/SupporterStatusBanner";
import { Card, EmptyState, Screen, Skeleton } from "../../components/ui";
import { ApiError, getAppConfig, getAvailableTasks, getProfile } from "../../lib/api";
import { getCategoryMeta } from "../../lib/categories";
import { formatCost, formatMinutes, formatScheduledAt } from "../../lib/task-utils";
import type { Profile, Task } from "../../lib/types";
import { color, size } from "../../theme/tokens";

function formatWhen(task: Task): string {
  if (task.is_immediate) return "ASAP";
  if (!task.scheduled_at) return "—";
  return formatScheduledAt(task.scheduled_at);
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
  // Whether the server pushes new tasks (D-24). Off, or unreadable, means the
  // availability card stays hidden and the feed is the whole screen.
  const [dispatchOn, setDispatchOn] = useState(false);

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
        // The permission prompt's first moment: supporter onboarding is done
        // and this is the board the notifications are about. Never at launch,
        // asked once per device by this path (push.ts askForPushInContext).
        if (!pushAskedThisSession) {
          pushAskedThisSession = true;
          askForPushInContext("supporter_ready");
        }
        getAppConfig()
          .then((cfg) => setDispatchOn(cfg.dispatch === true))
          .catch(() => setDispatchOn(false));
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

  if (profileLoading) {
    return (
      <Screen insetForTabBar headline="Tasks">
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      </Screen>
    );
  }

  if (profileError && !profile) {
    return (
      <Screen insetForTabBar headline="Tasks">
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

  // The app is supporter-only (PLAN §13b-3): every account that is not yet an
  // approved supporter lands here and sees where it stands — "none" gets the
  // apply card, applied/rejected get their status. There is no requester home
  // to send anyone to any more.

  // Every non-approved state is one banner (web parity — see
  // SupporterStatusBanner); the tasks feed below is approved-only.
  if (status !== "approved") {
    return (
      <Screen
        insetForTabBar
        headline="Tasks"
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}
      >
        <SupporterStatusBanner status={status} onApply={() => router.push("/supporter-apply")} />
      </Screen>
    );
  }

  return (
    <Screen
      insetForTabBar
      headline="Tasks"
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}
    >
      {dispatchOn ? <AvailabilityCard /> : null}
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
          title="New tasks post throughout the day"
          caption={
            dispatchOn
              ? "Nothing open right now. Go available and new tasks come straight to your phone."
              : "Nothing open right now. Pull down to check again."
          }
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
