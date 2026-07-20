import { useCallback, useState } from "react";
import { FlatList, RefreshControl, Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useFocusEffect, useRouter } from "expo-router";
import Swipeable from "react-native-gesture-handler/ReanimatedSwipeable";
import { Bell, ChevronLeft, CircleAlert, X } from "lucide-react-native";
import { PressableScale } from "../components/ui/PressableScale";
import { EmptyState } from "../components/ui/EmptyState";
import { Skeleton } from "../components/ui/Skeleton";
import {
  ApiError,
  deleteNotification,
  getNotifications,
  markAllRead,
  markNotificationRead,
} from "../lib/api";
import { getNotificationMeta, getNotificationTintStyles } from "../lib/notifications";
import { formatRelativeTime } from "../lib/task-utils";
import type { AppNotification } from "../lib/types";
import { color, size } from "../theme/tokens";

const PAGE_SIZE = 20;

function NotificationRow({
  notification,
  onPress,
  onDelete,
}: {
  notification: AppNotification;
  onPress: () => void;
  onDelete: () => void;
}) {
  const meta = getNotificationMeta(notification.type);
  const tint = getNotificationTintStyles(meta.tint);
  const Icon = meta.icon;

  return (
    <Swipeable
      overshootRight={false}
      renderRightActions={() => (
        <PressableScale
          onPress={onDelete}
          className="ml-2 w-24 items-center justify-center rounded-card bg-danger"
        >
          <Text className="text-body font-semibold text-white">Delete</Text>
        </PressableScale>
      )}
    >
      <PressableScale
        onPress={onPress}
        className={`rounded-card border border-line p-4 ${
          notification.unread ? "bg-brand-tint" : "bg-surface"
        }`}
      >
        <View className="flex-row items-start gap-3">
          <View className={`h-9 w-9 items-center justify-center rounded-pill ${tint.bg}`}>
            <Icon color={tint.iconColor} size={18} strokeWidth={size.iconStroke} />
          </View>
          <View className="flex-1 pr-6">
            <Text className="text-body font-semibold text-ink" numberOfLines={2}>
              {notification.title}
            </Text>
            <Text className="mt-1 text-body text-muted" numberOfLines={2}>
              {notification.body}
            </Text>
            <Text className="mt-1 text-caption text-muted">
              {formatRelativeTime(notification.created_at)}
            </Text>
          </View>
        </View>
        <PressableScale
          onPress={onDelete}
          hitSlop={10}
          className="absolute right-3 top-3 h-6 w-6 items-center justify-center"
        >
          <X color={color.muted} size={16} strokeWidth={size.iconStroke} />
        </PressableScale>
      </PressableScale>
    </Swipeable>
  );
}

export default function Notifications() {
  const router = useRouter();
  const [items, setItems] = useState<AppNotification[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMore, setHasMore] = useState(true);
  const [error, setError] = useState<string | null>(null);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const loadFirstPage = useCallback(async () => {
    try {
      const page = await getNotifications({ limit: PAGE_SIZE });
      setItems(page);
      setHasMore(page.length === PAGE_SIZE);
      setError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setError(e instanceof Error ? e.message : "Couldn't load notifications");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      loadFirstPage();
    }, [loadFirstPage])
  );

  async function onRefresh() {
    setRefreshing(true);
    await loadFirstPage();
    setRefreshing(false);
  }

  async function loadMore() {
    if (loadingMore || !hasMore || items.length === 0) return;
    setLoadingMore(true);
    try {
      const cursor = items[items.length - 1].created_at;
      const page = await getNotifications({ limit: PAGE_SIZE, before: cursor });
      setItems((current) => {
        const seen = new Set(current.map((n) => n.id));
        return [...current, ...page.filter((n) => !seen.has(n.id))];
      });
      setHasMore(page.length === PAGE_SIZE);
    } catch (e) {
      if (handleAuthError(e)) return;
      setHasMore(false);
    } finally {
      setLoadingMore(false);
    }
  }

  async function openNotification(notification: AppNotification) {
    if (notification.unread) {
      setItems((current) =>
        current.map((n) => (n.id === notification.id ? { ...n, unread: false } : n))
      );
      try {
        await markNotificationRead(notification.id);
      } catch (e) {
        handleAuthError(e);
      }
    }
    if (notification.task_id) {
      router.push(`/task/${notification.task_id}`);
    }
  }

  async function handleMarkAllRead() {
    setItems((current) => current.map((n) => ({ ...n, unread: false })));
    try {
      await markAllRead();
    } catch (e) {
      handleAuthError(e);
    }
  }

  async function handleDelete(id: string) {
    const previous = items;
    setItems((current) => current.filter((n) => n.id !== id));
    try {
      await deleteNotification(id);
    } catch (e) {
      if (handleAuthError(e)) return;
      setItems(previous);
    }
  }

  const hasUnread = items.some((n) => n.unread);

  return (
    <SafeAreaView className="flex-1 bg-page" edges={["top", "left", "right"]}>
      <View className="mb-6 mt-4 flex-row items-center justify-between px-6">
        <View className="flex-row items-center">
          <PressableScale
            onPress={() => router.back()}
            className="h-11 w-11 items-center justify-center rounded-pill"
            hitSlop={8}
          >
            <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
          </PressableScale>
          <Text className="ml-1 text-title font-semibold text-ink">Notifications</Text>
        </View>
        {hasUnread ? (
          <PressableScale onPress={handleMarkAllRead} hitSlop={8}>
            <Text className="text-body text-brand">Mark all read</Text>
          </PressableScale>
        ) : null}
      </View>

      {loading ? (
        <View className="gap-3 px-6">
          <Skeleton className="h-[72px]" />
          <Skeleton className="h-[72px]" />
          <Skeleton className="h-[72px]" />
        </View>
      ) : error && items.length === 0 ? (
        <View className="px-6">
          <EmptyState
            icon={CircleAlert}
            title="Couldn't load notifications"
            caption={error}
            actionLabel="Retry"
            onAction={loadFirstPage}
          />
        </View>
      ) : items.length === 0 ? (
        <View className="px-6">
          <EmptyState
            icon={Bell}
            title="You're all caught up"
            caption="New notifications will show up here."
          />
        </View>
      ) : (
        <FlatList
          data={items}
          keyExtractor={(n) => n.id}
          contentContainerStyle={{ paddingHorizontal: 24, paddingBottom: 24, gap: 12 }}
          renderItem={({ item }) => (
            <NotificationRow
              notification={item}
              onPress={() => openNotification(item)}
              onDelete={() => handleDelete(item.id)}
            />
          )}
          refreshControl={
            <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
          }
          onEndReachedThreshold={0.4}
          onEndReached={loadMore}
          ListFooterComponent={loadingMore ? <Skeleton className="h-[72px]" /> : null}
        />
      )}
    </SafeAreaView>
  );
}
