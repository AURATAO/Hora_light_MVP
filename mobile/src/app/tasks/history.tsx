import { useCallback, useEffect, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import { ChevronLeft, ClipboardList } from "lucide-react-native";
import { TaskListItem } from "../../components/TaskListItem";
import { Button, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getDonePage, getPostedClosedPage, type KeysetCursor } from "../../lib/api";
import type { Task } from "../../lib/types";
import { color, size } from "../../theme/tokens";

type Role = "posted" | "working";

const PAGE_SIZE = 20;

const COPY: Record<Role, { headline: string; emptyTitle: string; emptyCaption: string }> = {
  posted: {
    headline: "Posted history",
    emptyTitle: "Nothing here yet",
    emptyCaption: "Tasks you post will appear here once they're finished.",
  },
  working: {
    headline: "Work history",
    emptyTitle: "Nothing here yet",
    emptyCaption: "Tasks you complete will appear here.",
  },
};

function parseRole(raw: string | string[] | undefined): Role {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return value === "working" ? "working" : "posted";
}

/**
 * The full, paginated history behind "See all".
 *
 * ONE SCREEN FOR BOTH ROLES, picked by a param. The two lists differ in
 * exactly two things — which endpoint they read and what they are called — and
 * splitting them into two files would be two copies of the same pagination to
 * keep in step.
 *
 * Loads a page at a time off the server's keyset cursor rather than pulling
 * everything: this screen exists BECAUSE the unbounded lists were the problem,
 * and fixing that by fetching the same unbounded list one level deeper would
 * be moving it rather than solving it.
 */
export default function TaskHistory() {
  const router = useRouter();
  const role = parseRole(useLocalSearchParams().role);
  const copy = COPY[role];

  const [items, setItems] = useState<Task[]>([]);
  const [cursor, setCursor] = useState<KeysetCursor | null>(null);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchPage = useCallback(
    async (after: KeysetCursor | null) => {
      const params = { limit: PAGE_SIZE, ...(after ?? {}) };
      return role === "posted" ? getPostedClosedPage(params) : getDonePage(params);
    },
    [role]
  );

  const load = useCallback(async () => {
    try {
      const page = await fetchPage(null);
      setItems(page.items);
      setCursor(page.next);
      setTotal(page.total);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setError(e instanceof Error ? e.message : "Couldn't load your history");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fetchPage]);

  useEffect(() => {
    load();
  }, [load]);

  async function loadMore() {
    // The cursor is the ONLY guard against a double tap appending the same
    // page twice — it is cleared while the request is in flight below.
    if (!cursor || loadingMore) return;
    setLoadingMore(true);
    try {
      const page = await fetchPage(cursor);
      // Append, de-duplicating by id: a task that closed between two page
      // fetches shifts the window, and the same row can legitimately arrive in
      // both. Dropping it is right; rendering it twice is not.
      setItems((prev) => {
        const seen = new Set(prev.map((t) => t.id));
        return [...prev, ...page.items.filter((t) => !seen.has(t.id))];
      });
      setCursor(page.next);
      setTotal(page.total);
    } catch {
      // Keep what is already on screen. A failed "load more" is not a reason
      // to throw away the pages that worked.
    } finally {
      setLoadingMore(false);
    }
  }

  async function onRefresh() {
    setRefreshing(true);
    await load();
    setRefreshing(false);
  }

  return (
    <Screen
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}
    >
      <View className="mb-6 mt-4 flex-row items-center">
        <PressableScale
          onPress={() => router.back()}
          accessibilityRole="button"
          accessibilityLabel="Back"
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="ml-1 text-title font-semibold text-ink">{copy.headline}</Text>
      </View>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : error && items.length === 0 ? (
        <EmptyState
          icon={ClipboardList}
          title="Couldn't load your history"
          caption={error}
          actionLabel="Retry"
          onAction={load}
        />
      ) : items.length === 0 ? (
        <EmptyState icon={ClipboardList} title={copy.emptyTitle} caption={copy.emptyCaption} />
      ) : (
        <>
          <Text className="mb-3 text-caption text-muted">
            {total} {total === 1 ? "task" : "tasks"}
          </Text>
          <View className="gap-3">
            {items.map((task) => (
              <TaskListItem
                key={task.id}
                task={task}
                onPress={() => router.push(`/task/${task.id}`)}
              />
            ))}
          </View>
          {/* Explicit, not infinite scroll. A history is something people
              scan for one thing, and a list that grows under the thumb makes
              the bottom of it unreachable. */}
          {cursor ? (
            <View className="mt-4 mb-8">
              <Button
                label="Load more"
                variant="secondary"
                onPress={loadMore}
                loading={loadingMore}
              />
            </View>
          ) : (
            <View className="mb-8" />
          )}
        </>
      )}
    </Screen>
  );
}
