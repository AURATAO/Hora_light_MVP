import { useCallback, useEffect, useRef, useState } from "react";
import { Image, RefreshControl, ScrollView, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import * as Location from "expo-location";
import { Bike, Bus, Car, ChevronLeft, CircleAlert, MapPin, UserRound } from "lucide-react-native";
import { CancelTaskSheet } from "../../components/CancelTaskSheet";
import { CompleteTaskSheet, type CompleteTaskPayload } from "../../components/CompleteTaskSheet";
import { ReviewSheet, type ReviewSubmitPayload } from "../../components/ReviewSheet";
import { Badge, Button, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import {
  ApiError,
  acceptTask,
  cancelTask,
  clockIn,
  clockOut,
  completeTask,
  getMe,
  getPublicProfile,
  getProfileReviews,
  getTask,
  getWorklogs,
  sendGpsPing,
  submitReview,
  uploadCompletionPhoto,
} from "../../lib/api";
import { getCategoryMeta } from "../../lib/categories";
import { cancelOvertimeReminders, scheduleOvertimeReminders } from "../../lib/overtime-reminders";
import {
  deriveTaskStatus,
  formatCost,
  formatElapsed,
  formatMinutes,
  formatRelativeTime,
  statusLabel,
} from "../../lib/task-utils";
import type { PublicProfile, Review, Task, WorklogsSummary } from "../../lib/types";
import { color, size } from "../../theme/tokens";

const GPS_PING_INTERVAL_MS = 30_000;

const TRANSPORT_LABELS: Record<string, string> = {
  none: "No transport needed",
  car: "Car",
  bike: "Bike",
  public: "Public transport is fine",
};

const TRANSPORT_ICON: Record<string, typeof Car> = {
  car: Car,
  bike: Bike,
  public: Bus,
};

function locationParts(locationText: string | null): string[] {
  return (locationText ?? "")
    .split(" | ")
    .map((s) => s.trim())
    .filter(Boolean);
}

function formatClock(iso: string): string {
  return new Date(iso).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export default function TaskDetail() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const id = Array.isArray(params.id) ? params.id[0] : (params.id ?? "");

  const [meId, setMeId] = useState<string | null>(null);
  const [task, setTask] = useState<Task | null>(null);
  const [worklogs, setWorklogs] = useState<WorklogsSummary | null>(null);
  const [supporter, setSupporter] = useState<PublicProfile | null>(null);
  const [requester, setRequester] = useState<PublicProfile | null>(null);
  const [myReview, setMyReview] = useState<Review | null>(null);

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [reviewOpen, setReviewOpen] = useState(false);
  const [completeOpen, setCompleteOpen] = useState(false);

  const [accepting, setAccepting] = useState(false);
  const [acceptError, setAcceptError] = useState<string | null>(null);
  const [clockLoading, setClockLoading] = useState(false);
  const [clockError, setClockError] = useState<string | null>(null);
  const [gpsNotice, setGpsNotice] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const gpsIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const load = useCallback(async () => {
    try {
      const [me, t] = await Promise.all([getMe(), getTask(id)]);
      if (!me.auth) {
        router.replace("/(auth)/login");
        return;
      }
      setMeId(me.id);
      setTask(t);
      const iAmAssignee = t.assigned_to_id === me.id;

      if (t.assigned_to_id) {
        const [wl, sup] = await Promise.all([
          getWorklogs(id).catch(() => null),
          getPublicProfile(t.assigned_to_id).catch(() => null),
        ]);
        setWorklogs(wl);
        setSupporter(sup);

        if (t.status === "completed") {
          try {
            const reviews = await getProfileReviews(t.assigned_to_id);
            setMyReview(reviews.find((r) => r.task_id === id) ?? null);
          } catch {
            setMyReview(null);
          }
        } else {
          setMyReview(null);
        }
      } else {
        setWorklogs(null);
        setSupporter(null);
        setMyReview(null);
      }

      if (iAmAssignee && t.requester_id) {
        setRequester(await getPublicProfile(t.requester_id).catch(() => null));
      } else {
        setRequester(null);
      }
      setError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setError(e instanceof Error ? e.message : "Couldn't load this task");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  async function onRefresh() {
    setRefreshing(true);
    await load();
    setRefreshing(false);
  }

  async function handleCancelConfirm(reason: string) {
    try {
      await cancelTask(id, reason);
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setTask((t) =>
      t ? { ...t, status: "cancelled", cancel_reason: reason, cancelled_at: new Date().toISOString() } : t
    );
    setCancelOpen(false);
  }

  async function handleReviewSubmit(payload: ReviewSubmitPayload) {
    let review: Review;
    try {
      review = await submitReview(id, payload);
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setMyReview(review);
    setReviewOpen(false);
  }

  async function handleAccept() {
    setAccepting(true);
    setAcceptError(null);
    try {
      await acceptTask(id);
      await load();
    } catch (e) {
      if (handleAuthError(e)) return;
      if (e instanceof ApiError && e.message === "not available") {
        setAcceptError("This task was just accepted by someone else.");
        await load();
      } else {
        setAcceptError(e instanceof Error ? e.message : "Couldn't accept this task.");
      }
    } finally {
      setAccepting(false);
    }
  }

  async function handleClockIn() {
    setClockLoading(true);
    setClockError(null);
    try {
      const wl = await clockIn(id);
      // Reflect the new open worklog immediately — don't make the button
      // and timer wait on the slower full task+worklogs reload below just
      // to notice a mutation that already succeeded.
      setWorklogs((w) => ({
        worklogs: [...(w?.worklogs ?? []), wl],
        total_minutes: w?.total_minutes ?? 0,
        total_cost_cents: w?.total_cost_cents ?? 0,
      }));
      if (task?.estimated_minutes) {
        scheduleOvertimeReminders(id, task.title, task.estimated_minutes).catch(() => {});
      }
      await load();
    } catch (e) {
      if (handleAuthError(e)) return;
      setClockError(e instanceof Error ? e.message : "Couldn't clock in. Try again.");
    } finally {
      setClockLoading(false);
    }
  }

  async function handleClockOut() {
    setClockLoading(true);
    setClockError(null);
    try {
      const wl = await clockOut(id);
      // Same immediate-update reasoning as handleClockIn — flip the button
      // and stop the timer/GPS pings right away; load() below reconciles
      // total_minutes/total_cost_cents shortly after (this response doesn't
      // carry the recalculated totals, only the closed worklog itself).
      setWorklogs((w) =>
        w
          ? { ...w, worklogs: w.worklogs.map((existing) => (existing.id === wl.id ? wl : existing)) }
          : { worklogs: [wl], total_minutes: 0, total_cost_cents: 0 }
      );
      cancelOvertimeReminders(id).catch(() => {});
      await load();
    } catch (e) {
      if (handleAuthError(e)) return;
      setClockError(e instanceof Error ? e.message : "Couldn't clock out. Try again.");
    } finally {
      setClockLoading(false);
    }
  }

  async function handleCompleteSubmit({ photoUri, note }: CompleteTaskPayload) {
    try {
      const { url } = await uploadCompletionPhoto(id, photoUri);
      const updated = await completeTask(id, {
        completion_photo_url: url,
        completion_note: note || undefined,
      });
      setTask(updated);
      cancelOvertimeReminders(id).catch(() => {});
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setCompleteOpen(false);
  }

  const hasOpenWorklog = worklogs ? worklogs.worklogs.some((wl) => wl.end_at === null) : false;
  const openWorklog = worklogs?.worklogs.find((wl) => wl.end_at === null) ?? null;

  // Live elapsed timer while clocked in — ticks every second, no server round-trip.
  useEffect(() => {
    if (!hasOpenWorklog) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [hasOpenWorklog]);

  // GPS pings while clocked in — matches web's cadence (every 30s), sends one
  // immediately on clock-in too. Stops on clock-out or unmount. Permission
  // denial degrades to a notice only — clock-in itself never depends on it.
  useEffect(() => {
    if (!hasOpenWorklog) {
      if (gpsIntervalRef.current) {
        clearInterval(gpsIntervalRef.current);
        gpsIntervalRef.current = null;
      }
      return;
    }

    let cancelled = false;

    async function pingOnce() {
      try {
        const perm = await Location.getForegroundPermissionsAsync();
        let granted = perm.status === Location.PermissionStatus.GRANTED;
        if (!granted) {
          const req = await Location.requestForegroundPermissionsAsync();
          granted = req.status === Location.PermissionStatus.GRANTED;
        }
        if (cancelled) return;
        if (!granted) {
          setGpsNotice("Live location is off. Turn it on in Settings to share your position.");
          return;
        }
        setGpsNotice(null);
        const pos = await Location.getCurrentPositionAsync({ accuracy: Location.Accuracy.Balanced });
        if (cancelled) return;
        await sendGpsPing(id, {
          lat: pos.coords.latitude,
          lng: pos.coords.longitude,
          accuracy: pos.coords.accuracy != null ? Math.round(pos.coords.accuracy) : undefined,
        });
      } catch {
        // Silent — matches web's swallow-errors behavior for pings.
      }
    }

    pingOnce();
    gpsIntervalRef.current = setInterval(pingOnce, GPS_PING_INTERVAL_MS);
    return () => {
      cancelled = true;
      if (gpsIntervalRef.current) {
        clearInterval(gpsIntervalRef.current);
        gpsIntervalRef.current = null;
      }
    };
  }, [hasOpenWorklog, id]);

  if (loading) {
    return (
      <Screen scroll={false}>
        <HeaderRow onBack={() => router.back()} />
        <View className="gap-3">
          <Skeleton className="h-8 w-2/3" />
          <Skeleton className="h-[120px]" />
          <Skeleton className="h-[80px]" />
        </View>
      </Screen>
    );
  }

  if (error && !task) {
    return (
      <Screen scroll={false}>
        <HeaderRow onBack={() => router.back()} />
        <EmptyState icon={CircleAlert} title="Couldn't load this task" caption={error} actionLabel="Retry" onAction={load} />
      </Screen>
    );
  }

  if (!task) return null;

  const status = deriveTaskStatus(task);
  const meta = getCategoryMeta(task.category);
  const Icon = meta.icon;
  const isRequester = meId !== null && task.requester_id === meId;
  const isAssignee = meId !== null && task.assigned_to_id === meId;
  const isAvailableToAccept = meId !== null && !isRequester && !isAssignee && task.status === "open" && !task.assigned_to_id;
  const cancellable = isRequester && status === "open";
  const locations = locationParts(task.location_text);
  const TransportIcon = task.transport_required ? TRANSPORT_ICON[task.transport_required] : undefined;
  const canReview = isRequester && task.status === "completed" && !!task.assigned_to_id;
  const hasClosedWorklog = worklogs ? worklogs.worklogs.some((wl) => wl.end_at !== null) : false;
  const canComplete = isAssignee && task.status === "open" && !hasOpenWorklog && hasClosedWorklog;
  const elapsedMs = openWorklog ? now - new Date(openWorklog.start_at).getTime() : 0;
  const elapsedLabel = openWorklog ? formatElapsed(elapsedMs) : null;
  const isOvertime = !!task.estimated_minutes && elapsedMs / 60000 > task.estimated_minutes;

  return (
    <Screen
      scroll={false}
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}
    >
      <HeaderRow onBack={() => router.back()} />

      <ScrollView showsVerticalScrollIndicator={false}>
        <View className="mb-2 flex-row items-center gap-2">
          <Icon color={color.muted} size={18} strokeWidth={size.iconStroke} />
          <Text className="text-caption text-muted">{meta.label}</Text>
        </View>
        <Text className="mb-2 text-display text-ink">{task.title}</Text>
        <View className="mb-6 flex-row items-center gap-2">
          <Badge label={statusLabel(status)} variant="success" />
          <Text className="text-caption text-muted">{formatRelativeTime(task.created_at)}</Text>
        </View>

        {/* Info */}
        <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
          {task.description ? (
            <Text className="text-body text-ink">{task.description}</Text>
          ) : (
            <Text className="text-body text-muted">No description provided.</Text>
          )}

          {locations.length > 0 ? (
            <View className="gap-1">
              {locations.map((loc, i) => (
                <View key={i} className="flex-row items-start gap-2">
                  <MapPin color={color.muted} size={16} strokeWidth={size.iconStroke} />
                  <Text className="flex-1 text-caption text-ink">
                    {locations.length > 1 ? (
                      <Text className="text-muted">{i === 0 ? "Pick-up: " : `Stop ${i}: `}</Text>
                    ) : null}
                    {loc}
                  </Text>
                </View>
              ))}
            </View>
          ) : null}

          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">When</Text>
            <Text className="text-caption text-ink">
              {task.is_immediate ? "ASAP" : task.scheduled_at ? new Date(task.scheduled_at).toLocaleString() : "—"}
            </Text>
          </View>

          <View className="flex-row items-center justify-between">
            <Text className="text-caption text-muted">Transport</Text>
            <View className="flex-row items-center gap-1.5">
              {TransportIcon ? (
                <TransportIcon color={color.muted} size={14} strokeWidth={size.iconStroke} />
              ) : null}
              <Text className="text-caption text-ink">
                {TRANSPORT_LABELS[task.transport_required ?? "none"] ?? task.transport_required}
              </Text>
            </View>
          </View>

          {task.prepay_amount_cents && task.prepay_amount_cents > 0 ? (
            <View className="flex-row justify-between">
              <Text className="text-caption text-muted">Shopping budget</Text>
              <Text className="text-caption text-ink">{formatCost(task.prepay_amount_cents)}</Text>
            </View>
          ) : null}
        </View>

        {/* Supporter */}
        {isRequester && task.assigned_to_id ? (
          <PressableScale className="mb-4 flex-row items-center gap-3 rounded-card border border-line bg-surface p-4">
            {supporter?.avatar_url ? (
              <Image source={{ uri: supporter.avatar_url }} className="h-11 w-11 rounded-pill" />
            ) : (
              <View className="h-11 w-11 items-center justify-center rounded-pill bg-page">
                <UserRound color={color.muted} size={20} strokeWidth={size.iconStroke} />
              </View>
            )}
            <View>
              <Text className="text-body font-semibold text-ink">{supporter?.name ?? "Your supporter"}</Text>
              <Text className="text-caption text-muted">Supporter</Text>
            </View>
          </PressableScale>
        ) : null}

        {/* Requester (assignee's view) */}
        {isAssignee ? (
          <View className="mb-4 flex-row items-center gap-3 rounded-card border border-line bg-surface p-4">
            {requester?.avatar_url ? (
              <Image source={{ uri: requester.avatar_url }} className="h-11 w-11 rounded-pill" />
            ) : (
              <View className="h-11 w-11 items-center justify-center rounded-pill bg-page">
                <UserRound color={color.muted} size={20} strokeWidth={size.iconStroke} />
              </View>
            )}
            <View>
              <Text className="text-body font-semibold text-ink">{requester?.name ?? "Requester"}</Text>
              <Text className="text-caption text-muted">Requester</Text>
            </View>
          </View>
        ) : null}

        {/* Work session (assignee's view) */}
        {isAssignee && task.status === "open" ? (
          <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
            <Text className="text-caption font-semibold text-muted">Work session</Text>
            {hasOpenWorklog ? (
              <>
                <Text className={`text-title font-semibold ${isOvertime ? "text-danger" : "text-ink"}`}>
                  {elapsedLabel}
                </Text>
                {isOvertime ? <Text className="text-caption text-danger">Over the estimated time</Text> : null}
                <Button label="Clock out" onPress={handleClockOut} loading={clockLoading} />
                {gpsNotice ? <Text className="text-caption text-muted">{gpsNotice}</Text> : null}
              </>
            ) : (
              <Button label="Clock in" onPress={handleClockIn} loading={clockLoading} />
            )}
            {clockError ? <Text className="text-caption text-danger">{clockError}</Text> : null}
          </View>
        ) : null}

        {/* Progress */}
        {task.assigned_to_id && worklogs ? (
          <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
            <Text className="text-caption font-semibold text-muted">Progress</Text>
            {worklogs.worklogs.length === 0 ? (
              <Text className="text-caption text-muted">No time logged yet.</Text>
            ) : (
              <View className="gap-2">
                {worklogs.worklogs.map((wl) => {
                  const minutes = wl.end_at
                    ? Math.round((new Date(wl.end_at).getTime() - new Date(wl.start_at).getTime()) / 60000)
                    : null;
                  return (
                    <View key={wl.id} className="flex-row justify-between">
                      <Text className="text-caption text-ink">
                        {formatClock(wl.start_at)} – {wl.end_at ? formatClock(wl.end_at) : "in progress"}
                      </Text>
                      <Text className="text-caption text-muted">
                        {minutes !== null ? formatMinutes(minutes) : "—"}
                      </Text>
                    </View>
                  );
                })}
              </View>
            )}
            <View className="flex-row justify-between border-t border-line pt-2">
              <Text className="text-caption text-muted">Total time</Text>
              <Text className="text-caption text-ink">{formatMinutes(worklogs.total_minutes)}</Text>
            </View>
            <View className="flex-row justify-between">
              <Text className="text-caption font-semibold text-ink">Total cost</Text>
              <Text className="text-caption font-semibold text-ink">
                {formatCost(worklogs.total_cost_cents)}
              </Text>
            </View>
          </View>
        ) : null}

        {/* Completion */}
        {task.status === "completed" ? (
          <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
            <Text className="text-caption font-semibold text-muted">Completion</Text>
            {task.completion_photo_url ? (
              <Image
                source={{ uri: task.completion_photo_url }}
                className="h-40 w-full rounded-sm"
                resizeMode="cover"
              />
            ) : null}
            {task.completion_note ? (
              <Text className="text-body text-ink">{task.completion_note}</Text>
            ) : null}
            {task.completed_at ? (
              <Text className="text-caption text-muted">Completed {formatRelativeTime(task.completed_at)}</Text>
            ) : null}
          </View>
        ) : null}

        {/* Complete action */}
        {canComplete ? (
          <Button
            label="Complete task"
            variant="secondary"
            onPress={() => setCompleteOpen(true)}
            className="mb-4"
          />
        ) : null}

        {/* Review */}
        {canReview ? (
          myReview ? (
            <View className="mb-4 gap-2 rounded-card border border-line bg-surface p-4">
              <Text className="text-caption font-semibold text-muted">Your review</Text>
              <Text className="text-body text-ink">{"★".repeat(myReview.stars)}{"☆".repeat(5 - myReview.stars)}</Text>
              {myReview.comment ? <Text className="text-caption text-ink">{myReview.comment}</Text> : null}
            </View>
          ) : (
            <Button label="Leave a review" variant="secondary" onPress={() => setReviewOpen(true)} className="mb-4" />
          )
        ) : null}

        {/* Cancel action */}
        {cancellable ? (
          <Button
            label="Cancel task"
            variant="secondary"
            onPress={() => setCancelOpen(true)}
            className="mb-8 border-danger"
          />
        ) : null}

        {task.status === "cancelled" && task.cancel_reason ? (
          <View className="mb-8 rounded-card border border-line bg-surface p-4">
            <Text className="text-caption font-semibold text-muted">Cancellation reason</Text>
            <Text className="mt-1 text-body text-ink">{task.cancel_reason}</Text>
          </View>
        ) : null}

        {/* Accept action */}
        {isAvailableToAccept ? (
          <View className="mb-8 gap-2">
            <Button label="Accept task" onPress={handleAccept} loading={accepting} />
            {acceptError ? <Text className="text-caption text-danger">{acceptError}</Text> : null}
          </View>
        ) : null}
      </ScrollView>

      <CancelTaskSheet visible={cancelOpen} onClose={() => setCancelOpen(false)} onConfirm={handleCancelConfirm} />
      <ReviewSheet visible={reviewOpen} onClose={() => setReviewOpen(false)} onSubmit={handleReviewSubmit} />
      <CompleteTaskSheet
        visible={completeOpen}
        onClose={() => setCompleteOpen(false)}
        onSubmit={handleCompleteSubmit}
      />
    </Screen>
  );
}

function HeaderRow({ onBack }: { onBack: () => void }) {
  return (
    <View className="mb-6 mt-4 flex-row items-center">
      <PressableScale
        onPress={onBack}
        className="h-11 w-11 items-center justify-center rounded-pill"
        hitSlop={8}
      >
        <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
      </PressableScale>
      <Text className="ml-1 text-title font-semibold text-ink">Task</Text>
    </View>
  );
}
