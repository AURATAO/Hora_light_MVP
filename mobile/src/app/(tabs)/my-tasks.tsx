import { useCallback, useEffect, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import Swipeable from "react-native-gesture-handler/ReanimatedSwipeable";
import { ClipboardList, Hourglass } from "lucide-react-native";
import { CancelTaskSheet } from "../../components/CancelTaskSheet";
import { TaskListItem } from "../../components/TaskListItem";
import { EmptyState, PressableScale, Pill, Screen, Skeleton } from "../../components/ui";
import {
  ApiError,
  cancelTask,
  getAssignedTasks,
  getDoneTasks,
  getPostedClosedTasks,
  getPostedTasks,
} from "../../lib/api";
import { deriveTaskStatus } from "../../lib/task-utils";
import type { Task } from "../../lib/types";
import { color } from "../../theme/tokens";

type Segment = "posted" | "working";

interface Bucket {
  active: Task[];
  history: Task[];
  loading: boolean;
  error: string | null;
}

const EMPTY_BUCKET: Bucket = { active: [], history: [], loading: true, error: null };

// getPostedTasks() and getPostedClosedTasks() overlap in practice —
// server/main.go's listMyTasks (GET /tasks/posted) has no status filter at
// all, so completed/cancelled tasks already come back from it too, same as
// from /tasks/posted/closed. Union + dedupe by id, then bucket by derived
// status client-side rather than trust either endpoint's name.
function bucketByStatus(tasks: Task[]): { active: Task[]; history: Task[] } {
  const active: Task[] = [];
  const history: Task[] = [];
  for (const t of tasks) {
    const status = deriveTaskStatus(t);
    // "removed" is terminal like the other two — a taken-down task belongs in
    // history, never in the list of things still to do.
    if (status === "completed" || status === "cancelled" || status === "removed") history.push(t);
    else active.push(t);
  }
  return { active, history };
}

function sortByCreatedDesc(tasks: Task[]): Task[] {
  return [...tasks].sort((a, b) => (a.created_at < b.created_at ? 1 : -1));
}

// "Post again" is offered on a requester's own finished tasks: completed, and
// cancelled too — a task you called off is exactly the one you may want back.
// Never on `removed`: a task the HO:RA team took down for policy reasons must
// not be one swipe away from going straight back up.
function isRepostable(task: Task): boolean {
  const status = deriveTaskStatus(task);
  return status === "completed" || status === "cancelled";
}

function parseSegment(raw: string | string[] | undefined): Segment | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return value === "posted" || value === "working" ? value : undefined;
}

export default function MyTasks() {
  const router = useRouter();
  // Callers can land on a specific segment (Home's "See more" → Posted). This
  // is a tab screen that keeps its state between visits, so the param has to
  // drive the segment, not just seed it.
  const params = useLocalSearchParams();
  const requestedSegment = parseSegment(params.segment);
  const [segment, setSegment] = useState<Segment>(requestedSegment ?? "posted");
  const [posted, setPosted] = useState<Bucket>(EMPTY_BUCKET);
  const [working, setWorking] = useState<Bucket>(EMPTY_BUCKET);
  const [refreshing, setRefreshing] = useState(false);
  const [cancelTarget, setCancelTarget] = useState<Task | null>(null);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  useEffect(() => {
    if (requestedSegment) setSegment(requestedSegment);
  }, [requestedSegment]);

  const loadPosted = useCallback(async () => {
    try {
      const [open, closed] = await Promise.all([getPostedTasks(), getPostedClosedTasks()]);
      const byId = new Map<string, Task>();
      for (const t of open) byId.set(t.id, t);
      for (const t of closed) byId.set(t.id, t);
      const { active, history } = bucketByStatus(Array.from(byId.values()));
      setPosted({
        active: sortByCreatedDesc(active),
        history: sortByCreatedDesc(history),
        loading: false,
        error: null,
      });
    } catch (e) {
      if (handleAuthError(e)) return;
      setPosted((b) => ({
        ...b,
        loading: false,
        error: e instanceof Error ? e.message : "Couldn't load your tasks",
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loadWorking = useCallback(async () => {
    try {
      const [active, done] = await Promise.all([getAssignedTasks(), getDoneTasks()]);
      setWorking({
        active: sortByCreatedDesc(active),
        history: sortByCreatedDesc(done),
        loading: false,
        error: null,
      });
    } catch (e) {
      if (handleAuthError(e)) return;
      setWorking((b) => ({
        ...b,
        loading: false,
        error: e instanceof Error ? e.message : "Couldn't load your tasks",
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      loadPosted();
      loadWorking();
    }, [loadPosted, loadWorking])
  );

  async function onRefresh() {
    setRefreshing(true);
    await Promise.all([loadPosted(), loadWorking()]);
    setRefreshing(false);
  }

  async function confirmCancel(reason: string) {
    const task = cancelTarget;
    if (!task) return;
    try {
      await cancelTask(task.id, reason);
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setPosted((b) => ({
      ...b,
      active: b.active.filter((t) => t.id !== task.id),
      history: [
        { ...task, status: "cancelled", cancel_reason: reason, cancelled_at: new Date().toISOString() },
        ...b.history,
      ],
    }));
    setCancelTarget(null);
  }

  function goToDetail(id: string) {
    router.push(`/task/${id}`);
  }

  // Only the source task's id travels — Post Task fetches and maps it. What
  // comes back is a brand-new task on submit, with no link to this one.
  function goToRepost(id: string) {
    router.push(`/post-task?duplicate=${id}`);
  }

  function renderPostedRow(task: Task) {
    const cancellable = deriveTaskStatus(task) === "open";
    return (
      <View key={task.id} className="mb-3">
        <Swipeable
          enabled={cancellable}
          overshootRight={false}
          renderRightActions={
            cancellable
              ? () => (
                  <PressableScale
                    onPress={() => setCancelTarget(task)}
                    className="ml-2 w-24 items-center justify-center rounded-card bg-danger"
                  >
                    <Text className="text-body font-semibold text-white">Cancel</Text>
                  </PressableScale>
                )
              : undefined
          }
        >
          <TaskListItem task={task} onPress={() => goToDetail(task.id)} />
        </Swipeable>
      </View>
    );
  }

  // Same swipe affordance as an active posted row, one segment down the
  // screen: right-swipe reveals the single action that row supports. Posted
  // history only — a task in "Working" was supported, not posted, and its
  // requester is somebody else.
  function renderPostedHistoryRow(task: Task) {
    if (!isRepostable(task)) {
      return <TaskListItem key={task.id} task={task} onPress={() => goToDetail(task.id)} />;
    }
    return (
      <Swipeable
        key={task.id}
        overshootRight={false}
        renderRightActions={() => (
          <PressableScale
            onPress={() => goToRepost(task.id)}
            accessibilityRole="button"
            accessibilityLabel={`Post ${task.title} again`}
            className="ml-2 w-28 items-center justify-center rounded-card bg-ink"
          >
            <Text className="text-body font-semibold text-white">Post again</Text>
          </PressableScale>
        )}
      >
        <TaskListItem task={task} onPress={() => goToDetail(task.id)} />
      </Swipeable>
    );
  }

  function renderSkeletons() {
    return (
      <View className="gap-3">
        <Skeleton className="h-[84px]" />
        <Skeleton className="h-[84px]" />
        <Skeleton className="h-[84px]" />
      </View>
    );
  }

  const bucket = segment === "posted" ? posted : working;
  const isPosted = segment === "posted";

  return (
    <Screen
      insetForTabBar
      headline="My tasks"
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
      }
    >
      <View className="mb-6 flex-row gap-2">
        <Pill label="Posted" selected={isPosted} onPress={() => setSegment("posted")} />
        <Pill label="Working" selected={!isPosted} onPress={() => setSegment("working")} />
      </View>

      {bucket.loading ? (
        renderSkeletons()
      ) : bucket.error && bucket.active.length === 0 && bucket.history.length === 0 ? (
        <EmptyState
          icon={ClipboardList}
          title="Couldn't load your tasks"
          caption={bucket.error}
          actionLabel="Retry"
          onAction={isPosted ? loadPosted : loadWorking}
        />
      ) : bucket.active.length === 0 && bucket.history.length === 0 ? (
        <EmptyState
          icon={isPosted ? ClipboardList : Hourglass}
          title={isPosted ? "Nothing posted yet" : "You're not working on any tasks"}
          caption={
            isPosted
              ? "Tasks you post will show up here."
              : "Tasks you accept will show up here."
          }
          actionLabel={isPosted ? "Post a task" : undefined}
          onAction={isPosted ? () => router.push("/post-task") : undefined}
        />
      ) : (
        <View>
          {isPosted ? (
            posted.active.map(renderPostedRow)
          ) : (
            <View className="gap-3">
              {working.active.map((task) => (
                <TaskListItem key={task.id} task={task} onPress={() => goToDetail(task.id)} />
              ))}
            </View>
          )}

          {bucket.history.length > 0 ? (
            <>
              <Text className="mb-3 mt-6 text-title font-semibold text-ink">History</Text>
              <View className="gap-3">
                {isPosted
                  ? posted.history.map(renderPostedHistoryRow)
                  : working.history.map((task) => (
                      <TaskListItem key={task.id} task={task} onPress={() => goToDetail(task.id)} />
                    ))}
              </View>
            </>
          ) : null}
        </View>
      )}

      <CancelTaskSheet
        visible={cancelTarget !== null}
        onClose={() => setCancelTarget(null)}
        onConfirm={confirmCancel}
      />
    </Screen>
  );
}
