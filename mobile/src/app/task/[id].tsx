import { useCallback, useEffect, useRef, useState } from "react";
import { Image, RefreshControl, ScrollView, Text, View } from "react-native";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import * as Clipboard from "expo-clipboard";
import * as Location from "expo-location";
import {
  Bike,
  Bus,
  Car,
  ChevronLeft,
  CircleAlert,
  Copy,
  MapPin,
  MessageCircle,
  Navigation,
  Pencil,
} from "lucide-react-native";
import { CancelTaskSheet } from "../../components/CancelTaskSheet";
import { CompleteTaskSheet, type CompleteTaskPayload } from "../../components/CompleteTaskSheet";
import { ReviewSheet, type ReviewSubmitPayload } from "../../components/ReviewSheet";
import { Avatar, Badge, Button, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import {
  ApiError,
  acceptTask,
  cancelTask,
  clockIn,
  clockOut,
  completeTask,
  getLatestLocation,
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
import { openAddressInMaps, openCoordsInMaps, openRouteInMaps } from "../../lib/maps";
import { cancelOvertimeReminders, scheduleOvertimeReminders } from "../../lib/overtime-reminders";
import {
  LOCATION_STALE_MS,
  deriveTaskStatus,
  formatCost,
  formatElapsed,
  formatLastSeen,
  formatMinutes,
  formatRelativeTime,
  formatScheduledAt,
  statusLabel,
} from "../../lib/task-utils";
import type { LatestLocation, PublicProfile, Review, Task, WorklogsSummary } from "../../lib/types";
import { color, size } from "../../theme/tokens";

const GPS_PING_INTERVAL_MS = 30_000;

// The requester polls the supporter's last-known position at half the ping
// cadence (30s) — often enough to feel current, gentle enough to skip while the
// screen is backgrounded. Foreground-only, gated on the supporter being clocked
// in; see the useFocusEffect below.
const LOCATION_POLL_INTERVAL_MS = 60_000;

// How long the "Copied" confirmation replaces the section label.
const COPIED_FEEDBACK_MS = 1500;

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

function firstName(name?: string | null): string {
  if (!name) return "them";
  return name.trim().split(/\s+/)[0] || "them";
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
  const [latestLocation, setLatestLocation] = useState<LatestLocation | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [descriptionCopied, setDescriptionCopied] = useState(false);
  const gpsIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const copiedTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (copiedTimeoutRef.current) clearTimeout(copiedTimeoutRef.current);
    };
  }, []);

  async function handleCopyDescription() {
    if (!task?.description) return;
    try {
      await Clipboard.setStringAsync(task.description);
    } catch {
      return; // Nothing copied — say nothing rather than claim success.
    }
    setDescriptionCopied(true);
    if (copiedTimeoutRef.current) clearTimeout(copiedTimeoutRef.current);
    copiedTimeoutRef.current = setTimeout(() => setDescriptionCopied(false), COPIED_FEEDBACK_MS);
  }

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

  // On focus rather than only on mount, so coming back from a screen that can
  // change this task — the edit modal above all, but also chat, where it may
  // have been accepted meanwhile — never leaves a stale copy on screen. This
  // fires on the initial focus too, so it replaces the mount effect. `loading`
  // is only ever set true at init, so a refocus refetch updates in place
  // instead of flashing the skeleton.
  useFocusEffect(
    useCallback(() => {
      load();
    }, [load])
  );

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

  async function handleCompleteSubmit({ photoUri, photoMimeType, photoFileName, note }: CompleteTaskPayload) {
    try {
      const { url } = await uploadCompletionPhoto(id, {
        uri: photoUri,
        mimeType: photoMimeType,
        fileName: photoFileName,
      });
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

  // The requester's live-location view mirrors web (app/src/pages/TaskDetail.jsx)
  // but narrows the gate to "supporter is actually clocked in" (an open worklog)
  // rather than merely "task is open" — no open worklog means no pings are being
  // written, so there is nothing live to poll for. Computed before the loading
  // early-returns so the focus-effect hook below can depend on it. `task` may be
  // null here (still loading); optional-chaining keeps this false until it loads.
  const isRequesterView = meId !== null && task?.requester_id === meId;
  const canSeeLiveLocation = isRequesterView && hasOpenWorklog;

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

  // Requester side: poll the supporter's last-known position every 60s, but only
  // while this screen is focused AND the supporter is clocked in. useFocusEffect
  // tears the interval down when the screen blurs, so no timer runs in the
  // background. A denied permission on the supporter's phone simply means no new
  // pings arrive — the row stays in its waiting/last-known state, never an error.
  useFocusEffect(
    useCallback(() => {
      if (!canSeeLiveLocation) {
        // Clear any position carried over from a previous clock-in session so a
        // stale coordinate can't masquerade as current once they clock out.
        setLatestLocation(null);
        return;
      }
      let active = true;
      async function poll() {
        try {
          const loc = await getLatestLocation(id);
          if (active) setLatestLocation(loc);
        } catch {
          // Silent — a dropped poll leaves the last-known row untouched.
        }
      }
      poll();
      const interval = setInterval(poll, LOCATION_POLL_INTERVAL_MS);
      return () => {
        active = false;
        clearInterval(interval);
      };
    }, [canSeeLiveLocation, id])
  );

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
  // Editing and cancelling answer to one rule: my task, still open, nobody on
  // it yet — deriveTaskStatus only returns "open" while assigned_to_id is null.
  // Once accepted, both are off the table and coordination moves to chat; the
  // server enforces the same rule (main.go updateTask, cancelTask).
  const editable = isRequester && status === "open";
  const cancellable = editable;
  const locations = locationParts(task.location_text);
  const TransportIcon = task.transport_required ? TRANSPORT_ICON[task.transport_required] : undefined;
  // Supporter-only, and only once this task is actually theirs: the route action
  // answers "where am I going next", which is meaningless to a requester and to
  // anyone browsing an open task.
  const canGetDirections = isAssignee && locations.length > 0;
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
      <HeaderRow
        onBack={() => router.back()}
        onEdit={editable ? () => router.push(`/task/${id}/edit`) : undefined}
      />

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
          {/* Description is selectable (long-press) AND has an explicit copy
              button — addresses and shopping lists get pasted into other apps
              constantly, and long-press alone isn't discoverable. */}
          <View className="gap-1">
            <View className="flex-row items-center justify-between">
              <Text className="text-caption font-semibold text-muted">
                {descriptionCopied ? "Copied" : "Description"}
              </Text>
              {task.description ? (
                <PressableScale
                  onPress={handleCopyDescription}
                  accessibilityLabel="Copy description"
                  accessibilityRole="button"
                  hitSlop={8}
                  // 44×44 tap target (DESIGN.md §3) without inflating the
                  // header row — the negative margins absorb the overflow.
                  className="-my-3 -mr-3 h-11 w-11 items-center justify-center"
                >
                  <Copy color={color.muted} size={16} strokeWidth={size.iconStroke} />
                </PressableScale>
              ) : null}
            </View>
            {task.description ? (
              <Text selectable className="text-body text-ink">
                {task.description}
              </Text>
            ) : (
              <Text className="text-body text-muted">No description provided.</Text>
            )}
          </View>

          {/* Each address opens the platform maps app; the supporter additionally
              gets the whole multi-stop route in one tap (see below). */}
          {locations.length > 0 ? (
            <View className="gap-1">
              {locations.map((loc, i) => (
                <PressableScale
                  key={i}
                  onPress={() => openAddressInMaps(loc)}
                  accessibilityRole="link"
                  accessibilityLabel={`Open ${loc} in maps`}
                  className="min-h-11 flex-row items-center gap-2"
                >
                  <MapPin color={color.brand} size={16} strokeWidth={size.iconStroke} />
                  <Text className="flex-1 text-caption text-brand">
                    {locations.length > 1 ? (
                      <Text className="text-muted">{i === 0 ? "Pick-up: " : `Stop ${i}: `}</Text>
                    ) : null}
                    {loc}
                  </Text>
                </PressableScale>
              ))}
              {canGetDirections ? (
                <PressableScale
                  onPress={() => openRouteInMaps(locations)}
                  accessibilityRole="button"
                  className="mt-1 min-h-11 flex-row items-center gap-2 border-t border-line pt-2"
                >
                  <Navigation color={color.brand} size={16} strokeWidth={size.iconStroke} />
                  <Text className="text-caption text-brand">
                    {locations.length > 1 ? "Get directions through all stops" : "Get directions"}
                  </Text>
                </PressableScale>
              ) : null}
            </View>
          ) : null}

          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">When</Text>
            <Text className="text-caption text-ink">
              {task.is_immediate ? "ASAP" : task.scheduled_at ? formatScheduledAt(task.scheduled_at) : "—"}
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
          <View className="mb-4 rounded-card border border-line bg-surface">
            <PressableScale
              onPress={() => router.push(`/profile/${task.assigned_to_id}`)}
              className="flex-row items-center gap-3 p-4"
            >
              <Avatar uri={supporter?.avatar_url} name={supporter?.name} size={44} />
              <View>
                <Text className="text-body font-semibold text-ink">{supporter?.name ?? "Your supporter"}</Text>
                <Text className="text-caption text-muted">Supporter</Text>
              </View>
            </PressableScale>
            <PressableScale
              onPress={() => router.push(`/task/${id}/chat`)}
              className="min-h-11 justify-center border-t border-line p-4"
            >
              <View className="flex-row items-center gap-2">
                <MessageCircle color={color.muted} size={18} strokeWidth={size.iconStroke} />
                <Text className="text-body text-ink">Message {firstName(supporter?.name)}</Text>
              </View>
            </PressableScale>
          </View>
        ) : null}

        {/* Requester (assignee's view) */}
        {isAssignee ? (
          <View className="mb-4 rounded-card border border-line bg-surface">
            <PressableScale
              onPress={task.requester_id ? () => router.push(`/profile/${task.requester_id}`) : undefined}
              className="flex-row items-center gap-3 p-4"
            >
              <Avatar uri={requester?.avatar_url} name={requester?.name} size={44} />
              <View>
                <Text className="text-body font-semibold text-ink">{requester?.name ?? "Requester"}</Text>
                <Text className="text-caption text-muted">Requester</Text>
              </View>
            </PressableScale>
            <PressableScale
              onPress={() => router.push(`/task/${id}/chat`)}
              className="min-h-11 justify-center border-t border-line p-4"
            >
              <View className="flex-row items-center gap-2">
                <MessageCircle color={color.muted} size={18} strokeWidth={size.iconStroke} />
                <Text className="text-body text-ink">Message {firstName(requester?.name)}</Text>
              </View>
            </PressableScale>
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
            {/* Supporter's last-known position — requester-only, and only while
                they're clocked in (an open worklog). Interim until the v1.1 live
                map (see skills/decisions/D-09). */}
            {canSeeLiveLocation ? <LiveLocationRow location={latestLocation} nowMs={now} /> : null}
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

// The supporter's last-known GPS position as a single Progress-card row. Three
// honest states, no error state: no ping yet (also the case when the supporter
// denied location — nothing arrives) shows a muted "waiting"; a fresh ping is a
// tappable brand link into the maps app; a ping older than LOCATION_STALE_MS is
// still shown and still tappable, but muted so it never pretends to be live.
function LiveLocationRow({ location, nowMs }: { location: LatestLocation | null; nowMs: number }) {
  if (!location) {
    return (
      <View className="min-h-11 flex-row items-center gap-2">
        <MapPin color={color.muted} size={16} strokeWidth={size.iconStroke} />
        <Text className="text-caption text-muted">Waiting for location…</Text>
      </View>
    );
  }
  const stale = nowMs - new Date(location.created_at).getTime() > LOCATION_STALE_MS;
  return (
    <PressableScale
      onPress={() => openCoordsInMaps(location.lat, location.lng)}
      accessibilityRole="link"
      accessibilityLabel="Open supporter's last known location in maps"
      className="min-h-11 flex-row items-center gap-2"
    >
      <MapPin color={stale ? color.muted : color.brand} size={16} strokeWidth={size.iconStroke} />
      <Text className={`text-caption ${stale ? "text-muted" : "text-brand"}`}>
        Last seen {formatLastSeen(location.created_at, nowMs)}
      </Text>
    </PressableScale>
  );
}

// `onEdit` is passed only for a task the signed-in requester can still change,
// so the pencil is absent — not disabled — the moment someone accepts it.
function HeaderRow({ onBack, onEdit }: { onBack: () => void; onEdit?: () => void }) {
  return (
    <View className="mb-6 mt-4 flex-row items-center justify-between">
      <View className="flex-row items-center">
        <PressableScale
          onPress={onBack}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="ml-1 text-title font-semibold text-ink">Task</Text>
      </View>
      {onEdit ? (
        <PressableScale
          onPress={onEdit}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
          accessibilityRole="button"
          accessibilityLabel="Edit task"
        >
          <Pencil color={color.ink} size={20} strokeWidth={size.iconStroke} />
        </PressableScale>
      ) : null}
    </View>
  );
}
