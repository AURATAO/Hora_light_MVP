import { useCallback, useEffect, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useRouter } from "expo-router";
import { Banknote, ChevronLeft } from "lucide-react-native";
import { TransferRow } from "../../components/TransferRow";
import { Button, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getEarnings, type EarningsTransfer } from "../../lib/api";
import { color, size } from "../../theme/tokens";

const PAGE_SIZE = 20;

/**
 * Every payout HO:RA has sent this supporter, a page at a time.
 *
 * NOT A REPLACEMENT FOR THE STRIPE DASHBOARD, and the earnings screen still
 * routes there. The two answer different questions: Stripe's is the record of
 * what reached their bank, this is the record of what HO:RA paid them and
 * which task each payment was for. Sending somebody out to an external
 * dashboard to answer "what have I earned here" is an export, not a list.
 *
 * OFFSET pagination, matching the endpoint. payouts.created_at is not unique —
 * two transfers written in the same settlement share it — so a keyset on it
 * can skip or repeat a row at a page boundary.
 */
export default function EarningsHistory() {
  const router = useRouter();

  const [items, setItems] = useState<EarningsTransfer[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await getEarnings({ limit: PAGE_SIZE, offset: 0 });
      setItems(data.transfers ?? []);
      setTotal(data.total ?? (data.transfers ?? []).length);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setError(e instanceof Error ? e.message : "Couldn't load your payments");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const hasMore = items.length < total;

  async function loadMore() {
    if (!hasMore || loadingMore) return;
    setLoadingMore(true);
    try {
      const data = await getEarnings({ limit: PAGE_SIZE, offset: items.length });
      // De-duplicated by task and timestamp, the same key the rows render
      // under: a payout landing between two page fetches shifts the window,
      // and offset pagination will hand back a row that is already on screen.
      setItems((prev) => {
        const seen = new Set(prev.map((t) => `${t.task_id}-${t.created_at}`));
        return [...prev, ...(data.transfers ?? []).filter((t) => !seen.has(`${t.task_id}-${t.created_at}`))];
      });
      setTotal(data.total ?? total);
    } catch {
      // Keep what is on screen. A failed "load more" is not a reason to throw
      // away the pages that worked.
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
        <Text className="ml-1 text-title font-semibold text-ink">All payments</Text>
      </View>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : error && items.length === 0 ? (
        <EmptyState
          icon={Banknote}
          title="Couldn't load your payments"
          caption={error}
          actionLabel="Retry"
          onAction={load}
        />
      ) : items.length === 0 ? (
        <EmptyState
          icon={Banknote}
          title="No payments yet"
          caption="Complete a task and your first payment appears here."
        />
      ) : (
        <>
          <Text className="mb-3 text-caption text-muted">
            {total} {total === 1 ? "payment" : "payments"}
          </Text>
          {items.map((t) => (
            <TransferRow key={`${t.task_id}-${t.created_at}`} transfer={t} />
          ))}
          {hasMore ? (
            <View className="mt-4 mb-8">
              <Button label="Load more" variant="secondary" onPress={loadMore} loading={loadingMore} />
            </View>
          ) : (
            <View className="mb-8" />
          )}
        </>
      )}
    </Screen>
  );
}
