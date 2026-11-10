import { useCallback, useState } from "react";
import { RefreshControl, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import { Hourglass } from "lucide-react-native";
import { HISTORY_PREVIEW_COUNT, HistorySection } from "../../components/HistorySection";
import { TaskListItem } from "../../components/TaskListItem";
import { EmptyState, Screen, Skeleton } from "../../components/ui";
import { ApiError, getAssignedTasks, getDonePage } from "../../lib/api";
import type { Task } from "../../lib/types";
import { color } from "../../theme/tokens";

// MY JOBS — the tasks this supporter has accepted, and what they finished.
//
// The app is supporter-only (PLAN §13b-3, 2026-11-06): requesters book over
// iMessage, WhatsApp or the web, so the "Posted" side this screen used to
// carry, with its swipe-to-cancel and "Post again", is gone. The tab itself is
// hidden until the account is an approved supporter ((tabs)/_layout).

interface Bucket {
  active: Task[];
  /** The HISTORY_PREVIEW_COUNT most recent, not the whole history. */
  history: Task[];
  /** How many finished tasks exist in total, server-counted. */
  historyTotal: number;
  loading: boolean;
  error: string | null;
}

const EMPTY_BUCKET: Bucket = {
  active: [],
  history: [],
  historyTotal: 0,
  loading: true,
  error: null,
};

function sortByCreatedDesc(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => (a.created_at < b.created_at ? 1 : -1));
}

export default function MyJobs() {
  const router = useRouter();
  const [working, setWorking] = useState<Bucket>(EMPTY_BUCKET);
  const [refreshing, setRefreshing] = useState(false);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const loadWorking = useCallback(async () => {
    try {
      const [active, donePage] = await Promise.all([
        getAssignedTasks(),
        getDonePage({ limit: HISTORY_PREVIEW_COUNT }),
      ]);
      setWorking({
        active: sortByCreatedDesc(active),
        history: donePage.items,
        historyTotal: donePage.total,
        loading: false,
        error: null,
      });
    } catch (e) {
      if (handleAuthError(e)) return;
      setWorking((b) => ({
        ...b,
        loading: false,
        error: e instanceof Error ? e.message : "Couldn't load your jobs",
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      loadWorking();
    }, [loadWorking])
  );

  async function onRefresh() {
    setRefreshing(true);
    await loadWorking();
    setRefreshing(false);
  }

  function goToDetail(id: string) {
    router.push(`/task/${id}`);
  }

  return (
    <Screen
      insetForTabBar
      headline="My jobs"
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
      }
    >
      {working.loading ? (
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : working.error && working.active.length === 0 && working.history.length === 0 ? (
        <EmptyState
          icon={Hourglass}
          title="Couldn't load your jobs"
          caption={working.error}
          actionLabel="Retry"
          onAction={loadWorking}
        />
      ) : working.active.length === 0 && working.history.length === 0 ? (
        <EmptyState
          icon={Hourglass}
          title="Accept your first task"
          caption="Tasks you accept will show up here."
          actionLabel="See open tasks"
          onAction={() => router.push("/(tabs)/work")}
        />
      ) : (
        <View>
          <View className="gap-3">
            {working.active.map((task) => (
              <TaskListItem key={task.id} task={task} onPress={() => goToDetail(task.id)} />
            ))}
          </View>

          {working.history.length > 0 ? (
            <HistorySection
              title="History"
              total={working.historyTotal}
              onSeeAll={() => router.push("/tasks/history?role=working")}
            >
              {working.history.map((task) => (
                <TaskListItem key={task.id} task={task} onPress={() => goToDetail(task.id)} />
              ))}
            </HistorySection>
          ) : null}
        </View>
      )}
    </Screen>
  );
}
