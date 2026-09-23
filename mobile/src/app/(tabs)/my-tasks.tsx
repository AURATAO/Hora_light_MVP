import { useCallback, useEffect, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import Swipeable from "react-native-gesture-handler/ReanimatedSwipeable";
import { ClipboardList, Hourglass } from "lucide-react-native";
import { CancelTaskSheet } from "../../components/CancelTaskSheet";
import { HISTORY_PREVIEW_COUNT, HistorySection } from "../../components/HistorySection";
import { TaskListItem } from "../../components/TaskListItem";
import { EmptyState, PressableScale, Pill, Screen, Skeleton } from "../../components/ui";
import {
  ApiError,
  cancelTask,
  getAssignedTasks,
  getTask,
  getDonePage,
  getPostedClosedPage,
  getPostedTasks,
} from "../../lib/api";
import { endTaskTracking } from "../../lib/task-teardown";
import { deriveTaskStatus } from "../../lib/task-utils";
import { useSupporterStatus } from "../../lib/use-supporter-status";
import type { Task } from "../../lib/types";
import { color } from "../../theme/tokens";

type Segment = "posted" | "working";

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

// GET /tasks/posted has no status filter at all (server/main.go listMyTasks),
// so finished tasks come back from it alongside live ones. This splits them,
// and only the ACTIVE half is used now — the history half is read from
// /tasks/posted/closed, which is authoritative for it and carries the count.
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
  const { isApproved } = useSupporterStatus();
  const [cancelTarget, setCancelTarget] = useState<Task | null>(null);
  // The priced cancellation block for whatever row is being cancelled.
  //
  // LIST PAYLOADS DO NOT CARRY IT — it is requester-only and attached by the
  // task-detail handler — so the sheet would otherwise open with nothing to
  // say about money. That was survivable while only unaccepted tasks could be
  // cancelled from here (always free, nothing to say); it is not now that an
  // accepted one can be, and charges the base fee. One GET on a deliberate
  // swipe is cheap, and it is the same fetch web's dialog makes.
  const [cancelDetail, setCancelDetail] = useState<Task | null>(null);

  function startCancel(task: Task) {
    setCancelTarget(task);
    setCancelDetail(null);
    // Silent on failure: a cancel must never be blocked by not knowing what it
    // costs. The sheet falls back to saying nothing about money.
    getTask(task.id)
      .then((t) => setCancelDetail(t))
      .catch(() => {});
  }

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
      // ACTIVE from the unfiltered list, HISTORY from the closed endpoint.
      //
      // Both used to be unioned and bucketed client-side, because
      // /tasks/posted has no status filter and returns finished tasks too. The
      // history half is now a three-row PREVIEW with a server count behind it,
      // and neither of those can be derived from a client-side bucket of
      // whatever happened to be fetched — so the closed endpoint, which is
      // authoritative for exactly this, answers it directly.
      const [open, closedPage] = await Promise.all([
        getPostedTasks(),
        getPostedClosedPage({ limit: HISTORY_PREVIEW_COUNT }),
      ]);
      const { active } = bucketByStatus(open);
      setPosted({
        active: sortByCreatedDesc(active),
        history: closedPage.items,
        historyTotal: closedPage.total,
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
        error: e instanceof Error ? e.message : "Couldn't load your tasks",
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      loadPosted();
      // No supporter side, no request. The endpoint would answer with an empty
      // list, but asking for it at all is a round trip on every focus for
      // something that cannot render.
      if (isApproved) loadWorking();
    }, [loadPosted, loadWorking, isApproved])
  );

  async function onRefresh() {
    setRefreshing(true);
    await Promise.all([loadPosted(), ...(isApproved ? [loadWorking()] : [])]);
    setRefreshing(false);
  }

  async function confirmCancel(reason: string, reasonCode: string) {
    const task = cancelTarget;
    if (!task) return;
    try {
      await cancelTask(task.id, reason, reasonCode);
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    // Terminal, from a screen that never started any tracking: the teardown
    // is ownership-scoped and idempotent, so it costs nothing here and keeps
    // the rule simple — every transition to a terminal state calls it.
    await endTaskTracking(task.id, "cancelled");
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
    // task.status rather than deriveTaskStatus: the derived value is "open"
    // only while nobody has accepted, and an accepted task is now cancellable
    // too — it simply costs the supporter's base fee, which the sheet says
    // before anything happens.
    const cancellable = task.status === "open";
    return (
      <View key={task.id} className="mb-3">
        <Swipeable
          enabled={cancellable}
          overshootRight={false}
          renderRightActions={
            cancellable
              ? () => (
                  <PressableScale
                    onPress={() => startCancel(task)}
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

  // THE SEGMENT CONTROL IS SUPPORTER-ONLY, and so is the segment behind it.
  //
  // A requester-only user was being offered a "Working" tab that could never
  // hold anything — and a two-segment control where one of them is
  // permanently empty is not a choice, it is a question the app cannot
  // answer. A LONE segment is noise for the same reason: "Posted" over a list
  // of posted tasks says nothing the headline does not.
  //
  // Forced back to "posted" rather than merely hidden, so a non-supporter who
  // was left on the working segment by a stale param, or whose approval was
  // revoked while the screen was mounted, cannot end up staring at an empty
  // list with no way back to their own tasks.
  const isPosted = !isApproved || segment === "posted";
  const bucket = isPosted ? posted : working;

  return (
    <Screen
      insetForTabBar
      headline="My tasks"
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
      }
    >
      {isApproved ? (
        <View className="mb-6 flex-row gap-2">
          <Pill label="Posted" selected={isPosted} onPress={() => setSegment("posted")} />
          <Pill label="Working" selected={!isPosted} onPress={() => setSegment("working")} />
        </View>
      ) : null}

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

          {/* THE THREE MOST RECENT, and a way to the rest. Active work above
              stays whole: it is what the screen is for. Finished work is a
              reference, and an unbounded reference list pushed the live tasks
              further down the screen the longer somebody had used the app. */}
          {bucket.history.length > 0 ? (
            <HistorySection
              title="History"
              total={bucket.historyTotal}
              onSeeAll={() =>
                router.push(`/tasks/history?role=${isPosted ? "posted" : "working"}`)
              }
            >
              {isPosted
                ? posted.history.map(renderPostedHistoryRow)
                : working.history.map((task) => (
                    <TaskListItem key={task.id} task={task} onPress={() => goToDetail(task.id)} />
                  ))}
            </HistorySection>
          ) : null}
        </View>
      )}

      <CancelTaskSheet
        visible={cancelTarget !== null}
        cancellation={cancelDetail?.cancellation}
        payment={cancelDetail?.payment}
        onClose={() => {
          setCancelTarget(null);
          setCancelDetail(null);
        }}
        onConfirm={confirmCancel}
      />
    </Screen>
  );
}
