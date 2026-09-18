import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, AppState, Image, Linking, RefreshControl, ScrollView, Text, View } from "react-native";
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
  ShieldAlert,
} from "lucide-react-native";
import { BudgetIncreaseSheet, type BudgetIncreaseSubmit } from "../../components/BudgetIncreaseSheet";
import { CancelTaskSheet } from "../../components/CancelTaskSheet";
import { CompleteTaskSheet, type CompleteTaskPayload } from "../../components/CompleteTaskSheet";
import { LiveTrackingCard } from "../../components/LiveTrackingCard";
import { ReviewSheet } from "../../components/ReviewSheet";
import { TractionReviewSheet } from "../../components/TractionReviewSheet";
import { Avatar, Badge, Button, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import {
  ApiError,
  acceptTask,
  isPayoutsOnboardingRequired,
  cancelTask,
  clockIn,
  clockOut,
  completeTask,
  getExtensions,
  getMe,
  getPublicProfile,
  getProfileReviews,
  getTask,
  getWorklogs,
  requestBudgetIncrease,
  requestTimeExtension,
  resolveExtension,
  sendGpsPing,
  startEnroute,
  submitReview,
  type SubmitReviewPayload,
  uploadCompletionPhoto,
} from "../../lib/api";
import { TRACTION_3_CONFIG, isTractionWindowActive } from "../../lib/beta-notice";
import { getCategoryMeta } from "../../lib/categories";
import { broadcastPhase, shouldPollLive } from "../../lib/live-tracking";
import {
  GAP_LABEL,
  LAUNDRY_WAIT_HINT,
  PAUSED_REQUESTER,
  PAUSED_SUPPORTER,
  buildSessionTimeline,
  cumulativeLoggedMinutes,
  gapsNote,
  isPaused as isPausedBetweenSessions,
  showsWaitHint,
  type TimelineEntry,
} from "../../lib/work-sessions";
import {
  GPS_DEBUG_ROW,
  isBackgroundGpsHealthy,
  readBreadcrumb,
  restartBackgroundGps,
  startBackgroundGps,
  stopBackgroundGpsFor,
  type GpsBreadcrumb,
  type GpsPhase,
} from "../../lib/gps-tracking";
import { openAddressInMaps, openRouteInMaps } from "../../lib/maps";
import { cancelOvertimeReminders, scheduleOvertimeReminders } from "../../lib/overtime-reminders";
import {
  deriveTaskStatus,
  formatCost,
  formatElapsed,
  formatMinutes,
  formatRelativeTime,
  formatScheduledAt,
  removalNotice,
  statusLabel,
} from "../../lib/task-utils";
import { SUPPORT_EMAIL } from "../../lib/constants";
import { holdSummary, holdWillBeReleasedMessage, timeBasisNote } from "../../lib/payment-copy";
import type {
  ExtensionRequest,
  ExtensionsResponse,
  PublicProfile,
  Review,
  Settlement,
  Task,
  TaskCost,
  TimeCapState,
  WorklogsSummary,
} from "../../lib/types";
import { color, size } from "../../theme/tokens";

const GPS_PING_INTERVAL_MS = 30_000;

// How often, while this screen is mounted, we re-check that the background
// session is still alive. Half the staleness threshold, so a dead session is
// noticed within roughly one check of going quiet.
const GPS_HEALTH_CHECK_MS = 30_000;

// How long the "Copied" confirmation replaces the section label.
const COPIED_FEEDBACK_MS = 1500;

// How often the mid-task asks are refetched while a task is active.
//
// Fast, and deliberately so. This is the only clock either party has on a
// five-minute approval window: the supporter is standing in a shop waiting for
// an answer, and the SERVER applies the expiry on every read of this list — so
// a poll is simultaneously how the answer arrives and how the timeout is made
// to happen. Five seconds means "within seconds of it happening", which is the
// promise the flow makes.
const EXTENSIONS_POLL_INTERVAL_MS = 5_000;

// The exact body server/main.go's acceptTask returns to the loser of a
// concurrent accept — from its guarded UPDATE (0 rows matched) and from the
// advisory pre-check, which mean the same thing to the user. Status alone can't
// carry this: 400 is also "cannot accept your own task". Web matches the same
// string (app/src/pages/TaskDetail.jsx, My.jsx).
const ACCEPT_LOST_ERROR = "not available";

// Losing the race isn't the user's mistake, so this reads as information, not
// an error — and it points somewhere useful rather than just reporting.
const ACCEPT_LOST_COPY = "Someone just grabbed this one — check out the other tasks.";

// Long enough to read one sentence before the screen goes away.
const ACCEPT_LOST_LEAVE_MS = 1500;

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

// For the GPS debug row: "42s ago" / "3m ago" / "never".
function formatAge(at: number | null, now: number): string {
  if (at === null) return "never";
  const seconds = Math.max(0, Math.round((now - at) / 1000));
  return seconds < 90 ? `${seconds}s ago` : `${Math.round(seconds / 60)}m ago`;
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
  const [budgetOpen, setBudgetOpen] = useState(false);
  // The mid-task asks, plus the two numbers that go with them (the currently
  // approved budget and the time ceiling). Null until the first fetch; the
  // sections that read it render nothing until then rather than guessing.
  const [extensions, setExtensions] = useState<ExtensionsResponse | null>(null);
  const [extensionBusy, setExtensionBusy] = useState(false);
  const [extensionError, setExtensionError] = useState<string | null>(null);

  const [accepting, setAccepting] = useState(false);
  const [acceptError, setAcceptError] = useState<string | null>(null);
  // Kept separate from acceptError: the lost-race notice has to outlive the
  // refetch that follows it. Once the task comes back assigned, the whole
  // Accept block (and any error inside it) unmounts, so a message rendered
  // there would blink out of existence before it could be read.
  const [acceptLost, setAcceptLost] = useState(false);
  // The payouts gate (Stripe Phase 3). Kept separate from acceptError because
  // it is not a failure to retry: the supporter can do this task, they just
  // have nowhere for the money to land yet, and the only useful response is a
  // route into onboarding rather than the same button again.
  const [payoutsGate, setPayoutsGate] = useState<string | null>(null);
  const [clockLoading, setClockLoading] = useState(false);
  // In flight on the "On my way" tap. The server is idempotent, but a button
  // that still looks unpressed after being pressed is its own bug.
  const [enrouteBusy, setEnrouteBusy] = useState(false);
  const [enrouteError, setEnrouteError] = useState<string | null>(null);
  const [clockError, setClockError] = useState<string | null>(null);
  // Which capture path is live for this clock-in, and the single source the
  // notice copy and the foreground interval both key off:
  //   "background" — "Always" granted, the TaskManager task owns the pings;
  //   "foreground" — only "When In Use", so the interval below does the work;
  //   "off"        — no location permission at all, or not clocked in.
  const [gpsMode, setGpsMode] = useState<"off" | "foreground" | "background">("off");
  // Debug builds only: what the headless task last did, read back from
  // AsyncStorage. Lets a field test tell "the task never fired" from "the task
  // fired and the POST failed" with no Xcode attached.
  const [gpsBreadcrumb, setGpsBreadcrumb] = useState<GpsBreadcrumb | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [descriptionCopied, setDescriptionCopied] = useState(false);
  // Set when the backend answers "task_removed" instead of a task: the HO:RA
  // team took this one down while the screen was open, and removal detaches the
  // supporter, so the person looking at it may no longer be allowed to read it.
  const [removedGone, setRemovedGone] = useState(false);
  const gpsIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const copiedTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const acceptLostTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (copiedTimeoutRef.current) clearTimeout(copiedTimeoutRef.current);
      if (acceptLostTimeoutRef.current) clearTimeout(acceptLostTimeoutRef.current);
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
      if (e instanceof ApiError && e.status === 403 && e.message === "task_removed") {
        setTask(null);
        setRemovedGone(true);
        setError(null);
        return;
      }
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
    let result;
    try {
      result = await cancelTask(id, reason);
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setTask((t) =>
      t ? { ...t, status: "cancelled", cancel_reason: reason, cancelled_at: new Date().toISOString() } : t
    );
    // Deliberately NOT closing the sheet: it now shows what happened to the
    // hold, and a sheet that dismisses itself takes that confirmation with it.
    // The requester closes it themselves.
    return result;
  }

  // One submit path for both sheets: the classic review and either
  // questionnaire are the same POST, differing only in which fields they fill.
  async function handleReviewSubmit(payload: SubmitReviewPayload) {
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

  // Back to browsing. The Work list reloads on focus ((tabs)/work.tsx's
  // useFocusEffect), so leaving is also what refreshes the feed — the task
  // someone else just took drops out of Available on the way in, with no
  // second fetch from here. The fallback covers arriving by deep link or push,
  // where there is no Work list underneath to pop back to.
  function leaveToWorkList() {
    if (router.canGoBack()) router.back();
    else router.replace("/(tabs)/work");
  }

  async function handleAccept() {
    setAccepting(true);
    setAcceptError(null);
    try {
      await acceptTask(id);
      await load();
    } catch (e) {
      if (handleAuthError(e)) return;
      if (isPayoutsOnboardingRequired(e)) {
        // The task is still open behind this — the server refuses BEFORE the
        // claiming update, so nothing was taken off the board and coming back
        // after onboarding finds it exactly as it was.
        const body = (e as ApiError).body as Record<string, unknown> | undefined;
        setPayoutsGate(typeof body?.message === "string" ? body.message : "");
        return;
      }
      if (e instanceof ApiError && e.message === ACCEPT_LOST_ERROR) {
        // Someone else won. Say so plainly, refetch so this screen stops
        // offering a task that is gone, then hand them back to the feed.
        setAcceptLost(true);
        await load();
        acceptLostTimeoutRef.current = setTimeout(leaveToWorkList, ACCEPT_LOST_LEAVE_MS);
      } else {
        setAcceptError(e instanceof Error ? e.message : "Couldn't accept this task.");
      }
    } finally {
      setAccepting(false);
    }
  }

  async function handleEnroute() {
    setEnrouteBusy(true);
    setEnrouteError(null);
    try {
      await startEnroute(id);
      // Reload rather than patching locally: enroute_at comes back on the task,
      // and it is what the button, the ping phase and the requester's card all
      // read. Patching one of those three and not the others is how they drift.
      await load();
    } catch (e) {
      setEnrouteError(e instanceof Error ? e.message : "Couldn't start sharing your location.");
    } finally {
      setEnrouteBusy(false);
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
        // Carried forward rather than blanked: the settlement and the cost
        // breakdown are unchanged by a clock-in, and dropping them here would
        // make the cards below them flicker out until load() lands.
        cost: w?.cost ?? null,
        settlement: w?.settlement ?? null,
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
          : { worklogs: [wl], total_minutes: 0, total_cost_cents: 0, cost: null, settlement: null }
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

  async function handleCompleteSubmit({
    photoUri,
    photoMimeType,
    photoFileName,
    note,
    receiptCents,
    receiptPhoto,
  }: CompleteTaskPayload) {
    try {
      const { url } = await uploadCompletionPhoto(id, {
        uri: photoUri,
        mimeType: photoMimeType,
        fileName: photoFileName,
      });
      // The receipt rides the same upload endpoint and bucket as the completion
      // photo — same kind of file, same phone, same moment. Uploaded second so
      // a failure here does not orphan the completion photo any more than the
      // completion itself failing would.
      let receiptURL: string | undefined;
      if (receiptPhoto) {
        const uploaded = await uploadCompletionPhoto(id, {
          uri: receiptPhoto.uri,
          mimeType: receiptPhoto.mimeType,
          fileName: receiptPhoto.fileName,
        });
        receiptURL = uploaded.url;
      }
      const updated = await completeTask(id, {
        completion_photo_url: url,
        completion_note: note || undefined,
        receipt_amount_cents: receiptCents,
        receipt_photo_url: receiptURL,
      });
      setTask(updated);
      cancelOvertimeReminders(id).catch(() => {});
      // The settlement the requester and supporter are both about to read comes
      // from the worklogs payload, not from the task — refetch so the
      // completed screen shows the real total rather than the running one.
      await load();
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setCompleteOpen(false);
  }

  // ── The mid-task asks ────────────────────────────────────────────────────

  const loadExtensions = useCallback(async () => {
    try {
      setExtensions(await getExtensions(id));
    } catch {
      // Silent. This runs on a five-second timer; a dropped poll leaves the
      // last answer on screen, which is right, and an error banner that
      // flickers every five seconds is worse than no banner.
    }
  }, [id]);

  async function handleBudgetRequest({
    requestedCents,
    reason,
    fallback,
    fallbackNote,
  }: BudgetIncreaseSubmit) {
    try {
      await requestBudgetIncrease(id, {
        requested_cents: requestedCents,
        reason: reason || undefined,
        fallback,
        fallback_note: fallbackNote || undefined,
      });
    } catch (e) {
      handleAuthError(e);
      throw e;
    }
    setBudgetOpen(false);
    await loadExtensions();
  }

  async function handleTimeRequest(minutes: number) {
    setExtensionBusy(true);
    setExtensionError(null);
    try {
      await requestTimeExtension(id, minutes);
      await loadExtensions();
    } catch (e) {
      if (handleAuthError(e)) return;
      setExtensionError(e instanceof Error ? e.message : "Couldn't send your request. Try again.");
    } finally {
      setExtensionBusy(false);
    }
  }

  // Requester's one tap. A 409 here is not an error the requester caused — the
  // request timed out, or their other device already answered — so the refetch
  // below is what actually resolves the screen, and the message just says what
  // happened.
  async function handleResolve(request: ExtensionRequest, decision: "approve" | "deny") {
    setExtensionBusy(true);
    setExtensionError(null);
    try {
      await resolveExtension(id, request.id, decision);
      await Promise.all([loadExtensions(), load()]);
    } catch (e) {
      if (handleAuthError(e)) return;
      setExtensionError(e instanceof Error ? e.message : "Couldn't send your answer. Try again.");
      await loadExtensions();
    } finally {
      setExtensionBusy(false);
    }
  }

  // "Report a problem" on a settled task. A mailto with the task id in the
  // subject, not a dispute system: for beta, ops reading an email and fixing it
  // by hand in the Stripe dashboard is the whole process, and building a
  // dispute flow for it would be building the wrong thing well.
  async function handleReportProblem() {
    const subject = encodeURIComponent(`Problem with task ${id}`);
    const url = `mailto:${SUPPORT_EMAIL}?subject=${subject}`;
    if (await Linking.canOpenURL(url)) {
      await Linking.openURL(url);
      return;
    }
    Alert.alert("No mail app", `Write to us at ${SUPPORT_EMAIL} and mention task ${id}.`);
  }

  const hasOpenWorklog = worklogs ? worklogs.worklogs.some((wl) => wl.end_at === null) : false;
  const openWorklog = worklogs?.worklogs.find((wl) => wl.end_at === null) ?? null;

  // The mid-task asks, reduced to the three things the UI actually renders:
  // the one still waiting on an answer, the most recent one overall (so a
  // denial or a timeout stays on screen until something replaces it), and
  // whether this task is in a state where asking is possible at all.
  const pendingExtension = extensions?.items.find((e) => e.status === "pending") ?? null;
  const latestExtension = extensions?.items.length
    ? extensions.items[extensions.items.length - 1]
    : null;
  const settlement: Settlement | null = worklogs?.settlement ?? null;
  const capState = settlement?.time_cap ?? null;
  const approvedBudgetCents =
    extensions?.approved_budget_cents ?? settlement?.approved_budget_cents ?? 0;
  const isTaskActive = task?.status === "open" && !!task?.assigned_to_id;
  // The hold on the requester's card. Requester-only by construction: the
  // server omits the key from the supporter's copy of the task, so this is
  // null for them without a check here.
  const holdLine = holdSummary(task?.payment);

  // The requester's live view. One predicate, shared with web
  // (app/src/lib/liveTracking.js ↔ ../../lib/live-tracking), so the two clients
  // cannot drift into polling different sets of tasks — and matching exactly
  // what the server will answer, since GET /tasks/:id/live 404s outside it.
  //
  // Deliberately NOT narrowed to "clocked in" the way the old last-known row
  // was: the whole point of this feature is the span BEFORE the clock starts.
  // `task` may still be null here; the optional chaining keeps it false until
  // it loads.
  const isRequesterView = meId !== null && task?.requester_id === meId;
  const canWatchLive = shouldPollLive({
    isRequester: isRequesterView,
    status: task?.status,
    assignedToId: task?.assigned_to_id,
  });

  // Is this task ours to track at all? Goes false the moment the task says
  // otherwise — cancelled, removed, completed, or reassigned to someone else.
  // Deliberately independent of `worklogs`, which stops loading at all once a
  // task is unassigned, so a reassignment still reads as a definite "no".
  const gpsTaskIsOurs =
    task !== null && meId !== null && task.assigned_to_id === meId && task.status === "open";

  // Whether *this device* should be sending pings for *this* task, and under
  // which phase. Four states, not two:
  //
  //   null      — still loading. NOT "no": treating unknown as no would tear
  //               down background tracking every time this screen mounts.
  //   "none"    — nothing to send. Not ours, or the window has closed.
  //   "enroute" — they tapped "On my way" and have not clocked in. Pings flow
  //               with source='enroute', which is the one value the server
  //               accepts without an open worklog.
  //   "working" — the original path: an open worklog.
  //
  // The three known states come from broadcastPhase, shared with web
  // (app/src/lib/liveTracking.js) and mirroring the server's two accept
  // windows; the null is this screen's own, because only this screen knows
  // whether the answer has loaded yet.
  //
  // What that predicate settles for multi-session: a PAUSED supporter — clocked
  // out with the task still live — sends nothing. "working" is gone with the
  // open worklog, and the enroute arm requires ZERO worklogs rather than merely
  // no open one, so it cannot re-open behind the pause and keep sharing a
  // position through every gap.
  const gpsPhaseWanted: GpsPhase | "none" | null =
    task === null || (gpsTaskIsOurs && worklogs === null)
      ? null
      : broadcastPhase({
          isAssignee: gpsTaskIsOurs,
          status: task?.status,
          hasOpenWorklog,
          sessionCount: worklogs?.worklogs.length ?? 0,
          enrouteAt: task?.enroute_at,
        });

  const gpsTrackingWanted: boolean | null =
    gpsPhaseWanted === null ? null : gpsPhaseWanted !== "none";
  // The button, and the state it leads to. Both require the window to still be
  // open — assigned to us, task live, and NOT a single worklog yet, which is
  // what closes the window at the first clock-in (server: enrouteWindowOpen).
  const enrouteWindowOpen = Boolean(
    gpsTaskIsOurs && worklogs !== null && worklogs.worklogs.length === 0
  );
  const canGoEnroute = enrouteWindowOpen && !task?.enroute_at;
  const isEnrouteSharing = enrouteWindowOpen && Boolean(task?.enroute_at);
  const gpsPhase: GpsPhase = gpsPhaseWanted === "enroute" ? "enroute" : "working";

  // One notice, derived from gpsMode, so the copy can't drift out of step with
  // which capture path is actually running. The trailing clause changes with
  // the phase — a supporter walking to an address cares that sharing dies when
  // the screen locks even more than one already standing in the kitchen does,
  // and telling them it stops "while you work" would be describing the wrong
  // half of the trip.
  const gpsNotice =
    gpsTrackingWanted !== true
      ? null
      : gpsMode === "off"
        ? "Location is off, so the requester can't see where you are — turn it on for HO:RA in Settings."
        : gpsMode === "foreground"
          ? 'Location sharing stops when your phone locks — choose "Always" for HO:RA in Settings to keep it on ' +
            (gpsPhase === "enroute" ? "on your way there." : "while you work.")
          : null;

  // Live elapsed timer while clocked in — ticks every second, no server round-trip.
  useEffect(() => {
    if (!hasOpenWorklog) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [hasOpenWorklog]);

  // Background GPS is the primary path while clocked in: iOS keeps delivering
  // to the TaskManager task with the phone locked or another app in front,
  // which is exactly the 15-60 min hole the foreground interval left behind.
  // Deliberately NOT stopped on unmount — walking away from this screen must
  // not end tracking; only losing the open worklog does.
  useEffect(() => {
    if (gpsTrackingWanted === null) return;
    if (!gpsTrackingWanted) {
      setGpsMode("off");
      // Scoped to this task id: opening some other task must not stop the
      // tracking that belongs to the one they're actually clocked in on.
      stopBackgroundGpsFor(id).catch(() => {});
      return;
    }

    let cancelled = false;
    startBackgroundGps(id, gpsPhase)
      .then((started) => {
        if (cancelled) return;
        // Falling back is not an error the supporter has to act on — clock-in
        // already succeeded and the interval below covers them while the app
        // is open. The notice above just points at the better setting.
        setGpsMode(started ? "background" : "foreground");
      })
      .catch(() => {
        if (!cancelled) setGpsMode("foreground");
      });

    return () => {
      cancelled = true;
    };
  }, [gpsTrackingWanted, gpsPhase, id]);

  // Self-heal. The background session can die under us — iOS can stop feeding
  // it, and before the AsyncStorage fix a screen lock killed it outright. On
  // every foreground and every 30s while this screen is up, confirm it is
  // still producing events; if it isn't, hand the work back to the foreground
  // interval immediately and try to bring the session back. The next check
  // promotes it to "background" again once it is genuinely alive, so the
  // supporter is never left with neither path running.
  useEffect(() => {
    if (gpsTrackingWanted !== true) return;

    let cancelled = false;

    async function check() {
      const healthy = await isBackgroundGpsHealthy(id, gpsPhase);
      if (cancelled) return;
      if (healthy) {
        setGpsMode("background");
        return;
      }
      // Cover the hole first, restart second: if the restart is slow or fails
      // (permission downgraded to "When In Use" in Settings), the interval is
      // already running rather than waiting on the answer.
      setGpsMode((mode) => (mode === "background" ? "foreground" : mode));
      if (cancelled) return;
      await restartBackgroundGps(id, gpsPhase);
    }

    check();
    const timer = setInterval(check, GPS_HEALTH_CHECK_MS);
    const sub = AppState.addEventListener("change", (next) => {
      if (next === "active") check();
    });

    return () => {
      cancelled = true;
      clearInterval(timer);
      sub.remove();
    };
  }, [gpsTrackingWanted, gpsPhase, id]);

  // Debug builds only: poll the breadcrumb for the row below.
  useEffect(() => {
    if (!GPS_DEBUG_ROW || gpsTrackingWanted !== true) return;
    let cancelled = false;
    async function read() {
      const crumb = await readBreadcrumb();
      if (!cancelled) setGpsBreadcrumb(crumb);
    }
    read();
    const timer = setInterval(read, GPS_HEALTH_CHECK_MS);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [gpsTrackingWanted]);

  // Foreground fallback — the original path, unchanged in behaviour but now
  // gated on gpsMode instead of hasOpenWorklog, so it only runs when
  // background updates aren't. Every 30s, one immediately on start. Stops on
  // clock-out or unmount; permission denial degrades to a notice only.
  useEffect(() => {
    if (gpsMode !== "foreground") {
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
          // Drops to "off", which both clears this interval on the next run of
          // this effect and swaps the notice to the stronger copy.
          setGpsMode("off");
          return;
        }
        const pos = await Location.getCurrentPositionAsync({ accuracy: Location.Accuracy.Balanced });
        if (cancelled) return;
        await sendGpsPing(id, {
          lat: pos.coords.latitude,
          lng: pos.coords.longitude,
          accuracy: pos.coords.accuracy != null ? Math.round(pos.coords.accuracy) : undefined,
          // The phase wins over the capture mechanism during the enroute
          // window: "enroute" is the value the server's no-worklog exception is
          // keyed on, so a foreground-captured fix sent as "foreground" there
          // would be refused.
          source: gpsPhase === "enroute" ? "enroute" : "foreground",
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
  }, [gpsMode, gpsPhase, id]);

  // Both sides: poll the mid-task asks while the task is live and this screen
  // is focused.
  //
  // This poll is load-bearing rather than cosmetic. The server applies the
  // five-minute expiry on every read of this list, so polling is both how the
  // supporter learns the answer and how "no answer" becomes an answer at all —
  // which is why it runs for the supporter as well as the requester, and why
  // it is fast. useFocusEffect tears it down on blur, so nothing ticks in the
  // background.
  useFocusEffect(
    useCallback(() => {
      if (!isTaskActive || meId === null) return;
      let active = true;
      async function poll() {
        if (!active) return;
        await loadExtensions();
      }
      poll();
      const interval = setInterval(poll, EXTENSIONS_POLL_INTERVAL_MS);
      return () => {
        active = false;
        clearInterval(interval);
      };
    }, [isTaskActive, meId, loadExtensions])
  );

  // The requester's poll now lives in LiveTrackingCard, which is mounted only
  // while there is something live to watch and owns both the 15s cadence and
  // the "stop when the app is backgrounded" rule.

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

  // Taken down mid-session and no longer readable by this user: a plain
  // statement, not a retry loop — refetching will keep saying the same thing.
  if (removedGone) {
    return (
      <Screen scroll={false}>
        <HeaderRow onBack={() => router.back()} />
        <EmptyState
          icon={ShieldAlert}
          title="This task has been removed"
          caption="The HO:RA team took this task down. More tasks are coming — have a look at what's available."
          actionLabel="Back to tasks"
          onAction={() => router.back()}
        />
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
  // "Post again" — my own finished task, completed or cancelled. Same rule as
  // My Tasks' swipe action (isRepostable there): never on a `removed` task, and
  // never for a supporter, who sees this screen for tasks they only worked on.
  const canRepost = isRequester && (task.status === "completed" || task.status === "cancelled");
  // For the length of the Traction 3 round the questionnaire replaces the
  // classic review sheet, and the supporter gets one of their own — the only
  // time either side of a completed task is asked anything. When the window
  // passes, both revert on their own: the flag is a date check, nothing else.
  const questionnaireActive = isTractionWindowActive();
  const canGiveFeedback = isAssignee && task.status === "completed" && questionnaireActive;
  const elapsedMs = openWorklog ? now - new Date(openWorklog.start_at).getTime() : 0;
  const elapsedLabel = openWorklog ? formatElapsed(elapsedMs) : null;

  // ── Multi-session ────────────────────────────────────────────────────────
  //
  // The sessions and the free gaps between them, and whether this supporter is
  // between sessions right now. `now` already ticks every second while the
  // clock runs, so the running session's row stays live for free.
  const sessions = (worklogs?.worklogs ?? []).map((wl) => ({
    id: wl.id,
    startAt: wl.start_at,
    endAt: wl.end_at,
  }));
  const timeline = buildSessionTimeline(sessions, { nowMs: now });
  const paused = isPausedBetweenSessions({
    hasOpenWorklog,
    sessionCount: sessions.length,
    status: task.status,
  });

  // Logged time across EVERY session, running one included — the number the
  // ceiling is actually measured against.
  //
  // This replaced a comparison of the CURRENT session's elapsed time against
  // the estimate, which multi-session quietly broke: four sessions of twenty
  // minutes never trip a per-session check against a thirty-minute estimate,
  // and the task is an hour over. The server's figure wins where there is one
  // (capState.logged_minutes is what timecap.go warned on); the local sum
  // covers the seconds before the refetch lands and the tasks with no estimate
  // to have a cap state at all.
  const loggedMinutes =
    capState?.logged_minutes ?? cumulativeLoggedMinutes(sessions, { nowMs: now });
  const isOvertime = !!task.estimated_minutes && loggedMinutes > task.estimated_minutes;

  // One line, one category. Every category supports clocking out mid-task — a
  // plain errand simply taps Complete afterwards — but laundry is where the
  // wait is long enough that staying on the clock is the expensive instinct.
  const showWaitHint = isAssignee && task.status === "open" && showsWaitHint(task.category);

  // Whether the itemized settlement card is going to render. Progress defers to
  // it entirely (same rule web has always had for its running-cost card): the
  // settlement carries the sessions AND the totals once a task is over, and
  // rendering Progress alongside it put the identical session timeline on the
  // screen twice, one card apart. Caught on the simulator, not by any check.
  const showSettlementCard = Boolean(settlement && worklogs?.cost && task.status !== "open");

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

        {/* A question with a five-minute fuse on it, so it goes above
            everything else on the screen — a requester who has to scroll past
            the address to find it will not answer it in time. */}
        {isRequester && isTaskActive && pendingExtension ? (
          <ApprovalCard
            request={pendingExtension}
            approvedBudgetCents={approvedBudgetCents}
            supporterName={firstName(supporter?.name)}
            busy={extensionBusy}
            error={extensionError}
            nowMs={now}
            onApprove={() => handleResolve(pendingExtension, "approve")}
            onDeny={() => handleResolve(pendingExtension, "deny")}
          />
        ) : null}

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

        {/* What is reserved, for the requester of a live task. The whole
            failure this addresses is an off-session pre-auth being silent: a
            requester who cannot see that money was held assumes the post
            failed and cancels it. Gone once the task closes — the settlement
            card then says what became of it. */}
        {isRequester && holdLine && task.status === "open" ? (
          <View className="mb-4 gap-2 rounded-card border border-line bg-surface p-4">
            <View className="flex-row items-center justify-between">
              <Text className="text-caption font-semibold text-muted">On hold</Text>
              <Text className="text-caption text-ink">{holdLine}</Text>
            </View>
            <Text className="text-caption text-muted">
              Not a charge. You're billed for actual time and purchases when the task completes,
              and anything unused is released automatically.
            </Text>
            {task.prepay_amount_cents && task.prepay_amount_cents > 0 ? (
              <Text className="text-caption text-muted">
                The hold also covers up to {formatCost(task.prepay_amount_cents)} of shopping,
                reimbursed against the receipt.
              </Text>
            ) : null}
          </View>
        ) : null}

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
            {!hasOpenWorklog && canGoEnroute ? (
              <>
                {/* The step before the clock, and the first thing a supporter
                    does after accepting. Nothing is shared with the requester
                    until this is pressed — that is the privacy default: opt in,
                    once, deliberately. Primary here rather than beside Clock
                    in, because while it is on screen it is the only solid CTA
                    (DESIGN.md §1: one per screen) — Clock out, this screen's
                    usual one, cannot exist yet. */}
                <Button
                  label="On my way"
                  onPress={handleEnroute}
                  loading={enrouteBusy}
                />
                <Text className="text-caption text-muted">
                  Lets {firstName(requester?.name)} see you approaching. Sharing stops when you
                  clock out.
                </Text>
                {enrouteError ? (
                  <Text className="text-caption text-danger">{enrouteError}</Text>
                ) : null}
              </>
            ) : null}
            {!hasOpenWorklog && isEnrouteSharing ? (
              <>
                <Text className="text-caption text-muted">
                  Sharing your location with {firstName(requester?.name)} until you clock out.
                </Text>
                {/* The same permission notice the clocked-in branch shows. A
                    supporter whose location is off here is sharing NOTHING
                    while a requester watches an empty card, which is the worst
                    version of this feature — so it is said in both phases. */}
                {gpsNotice ? <Text className="text-caption text-muted">{gpsNotice}</Text> : null}
              </>
            ) : null}
            {hasOpenWorklog ? (
              <>
                <Text className={`text-title font-semibold ${isOvertime ? "text-danger" : "text-ink"}`}>
                  {elapsedLabel}
                </Text>
                {/* The session's own clock is above; this is about the TASK.
                    Measured on every session's minutes added together, because
                    that is what the ceiling clamps and what the bill is made
                    of — a supporter on their fourth short session is just as
                    far over the estimate as one on a single long one. */}
                {isOvertime ? (
                  <Text className="text-caption text-danger">
                    Over the estimated time — {formatMinutes(loggedMinutes)} logged in total
                  </Text>
                ) : null}
                <Button label="Clock out" onPress={handleClockOut} loading={clockLoading} />
                {showWaitHint ? (
                  <Text className="text-caption text-muted">{LAUNDRY_WAIT_HINT}</Text>
                ) : null}
                {gpsNotice ? <Text className="text-caption text-muted">{gpsNotice}</Text> : null}
                {/* Debug builds only — the headless task's own breadcrumb, so a
                    field test can separate "never fired" from "fired, POST
                    failed" without Xcode attached. */}
                {GPS_DEBUG_ROW && gpsBreadcrumb ? (
                  <Text className="text-caption text-muted">
                    {`gps ${gpsMode} · ${gpsBreadcrumb.events} events (${formatAge(gpsBreadcrumb.lastEventAt, now)}) · ` +
                      `${gpsBreadcrumb.pings} pings (${formatAge(gpsBreadcrumb.lastPingAt, now)})` +
                      (gpsBreadcrumb.note ? ` · ${gpsBreadcrumb.note}` : "")}
                  </Text>
                ) : null}
              </>
            ) : paused ? (
              <>
                {/* PAUSED — clocked out, task still live. The screen used to
                    end here, which is what made the first clock-out feel like
                    the last one. Both exits now sit together, and the line
                    above them answers the question a supporter standing
                    outside a launderette actually has: am I still being paid?

                    Said plainly and calmly (DESIGN.md §6): not billing is the
                    NORMAL state between sessions, not a fault, so it is muted
                    text and not a danger colour. */}
                <Text className="text-body text-ink">{PAUSED_SUPPORTER}</Text>
                {showWaitHint ? (
                  <Text className="text-caption text-muted">{LAUNDRY_WAIT_HINT}</Text>
                ) : null}
                {/* Complete is the solid one — the single CTA on this screen
                    (DESIGN.md §1) — because a paused task that is finished is
                    the common case, and clocking back in is the deliberate
                    one. Clock out was the solid button a moment ago; the slot
                    moves rather than multiplying. */}
                <Button label="Complete task" onPress={() => setCompleteOpen(true)} />
                <Button
                  label="Clock back in"
                  variant="secondary"
                  onPress={handleClockIn}
                  loading={clockLoading}
                />
              </>
            ) : (
              <>
                {showWaitHint ? (
                  <Text className="text-caption text-muted">{LAUNDRY_WAIT_HINT}</Text>
                ) : null}
                <Button
                  label="Clock in"
                  onPress={handleClockIn}
                  loading={clockLoading}
                  variant={canGoEnroute ? "secondary" : "primary"}
                />
              </>
            )}
            {clockError ? <Text className="text-caption text-danger">{clockError}</Text> : null}
          </View>
        ) : null}

        {/* The supporter's side of the same conversation: what they are
            currently allowed to spend and how long they are being paid for,
            and the two ways to ask for more of either. */}
        {isAssignee && isTaskActive ? (
          <SupporterAskCard
            approvedBudgetCents={approvedBudgetCents}
            capState={capState}
            pending={pendingExtension}
            latest={latestExtension}
            timeChoices={extensions?.time_choices ?? []}
            timeoutMinutes={extensions?.timeout_minutes ?? 0}
            busy={extensionBusy}
            error={extensionError}
            nowMs={now}
            onAskBudget={() => setBudgetOpen(true)}
            onAskTime={handleTimeRequest}
          />
        ) : null}

        {/* Requester: where their supporter is, right now. Replaces the
            last-known-position row that used to live in Progress — which only
            appeared once the supporter had CLOCKED IN, i.e. once they had
            already arrived, and which rendered their coordinates as text. */}
        {canWatchLive ? <LiveTrackingCard taskId={id} /> : null}

        {/* Progress — the live view, and only while there is something live to
            view. Hidden once the settlement card can render, which owns the
            same timeline and the final numbers. */}
        {task.assigned_to_id && worklogs && !showSettlementCard ? (
          <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
            <Text className="text-caption font-semibold text-muted">Progress</Text>

            {/* The requester's half of the pause. Their supporter has stepped
                away from the clock, and the only thing they need told is that
                it costs them nothing — said before the numbers, so it is read
                before the total is. */}
            {isRequester && paused ? (
              <Text className="text-body text-ink">{PAUSED_REQUESTER}</Text>
            ) : null}

            {timeline.length === 0 ? (
              <Text className="text-caption text-muted">No time logged yet.</Text>
            ) : (
              <SessionList timeline={timeline} />
            )}
            <View className="flex-row justify-between border-t border-line pt-2">
              <Text className="text-caption text-muted">Total time</Text>
              <Text className="text-caption text-ink">{formatMinutes(worklogs.total_minutes)}</Text>
            </View>
            {/* Only when there is a gap to explain. On a task worked in one
                sitting this line would be answering a question nobody asked. */}
            {gapsNote(timeline) ? (
              <Text className="-mt-2 text-caption text-muted">{gapsNote(timeline)}</Text>
            ) : null}
            {/* Why the rows above can add up to more than the total beside
                them. The server bills CLOSED sessions only (billing.go
                totalClosedMinutes), so the session still running is genuinely
                not in that figure yet — which was invisible when there was one
                timer and no list, and is not once the rows are on screen. */}
            {hasOpenWorklog ? (
              <Text className="-mt-2 text-caption text-muted">
                The session running now is added when it ends.
              </Text>
            ) : null}
            {/* The running total while the task is live. Once it is over, the
                settlement card below owns the number — showing both would be
                two totals on one screen, and they are the same total. */}
            {task.status === "open" ? (
              <View className="gap-1">
                <View className="flex-row justify-between">
                  <Text className="text-caption font-semibold text-ink">Total cost so far</Text>
                  <Text className="text-caption font-semibold text-ink">
                    {formatCost(worklogs.total_cost_cents)}
                  </Text>
                </View>
                {/* The requester's copy of the line the supporter sees on their
                    own card, so both sides read one set of numbers. Requester
                    only — for the supporter it would be the same sentence
                    twice on one screen. */}
                {isRequester && timeBasisNote(capState?.cap) ? (
                  <Text className="text-caption text-muted">{timeBasisNote(capState?.cap)}</Text>
                ) : null}
              </View>
            ) : null}
          </View>
        ) : null}

        {/* What was charged, itemized, for both sides. The one settlement
            surface — the same numbers the requester's card was billed for and
            the supporter was paid from, so neither has to take the other's
            word for it. */}
        {settlement && worklogs?.cost && task.status !== "open" ? (
          <SettlementCard
            cost={worklogs.cost}
            settlement={settlement}
            timeline={timeline}
            isRequester={isRequester}
            onReportProblem={handleReportProblem}
          />
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

        {/* Completing now lives in the work-session card above, as the solid
            half of the "Complete task / Clock back in" pair.
            Nothing is lost by its moving: the standalone button's condition
            (assignee, task open, no open session, at least one closed one) and
            the paused branch's are the SAME condition — a supporter with a
            closed session and no open one is, by definition, between sessions
            — so a copy down here could only ever render alongside that pair.
            Two Complete buttons on one screen, one solid and one not, is worse
            than none. */}

        {/* Review */}
        {canReview || canGiveFeedback ? (
          myReview ? (
            <View className="mb-4 gap-2 rounded-card border border-line bg-surface p-4">
              <Text className="text-caption font-semibold text-muted">
                {myReview.stars === null ? "Your feedback" : "Your review"}
              </Text>
              {/* A supporter's questionnaire carries no stars, so there is
                  nothing to draw — the heading above is the whole receipt. */}
              {myReview.stars !== null ? (
                <Text className="text-body text-ink">{"★".repeat(myReview.stars)}{"☆".repeat(5 - myReview.stars)}</Text>
              ) : (
                <Text className="text-caption text-muted">Thanks — this round closes {TRACTION_3_CONFIG.window}.</Text>
              )}
              {myReview.comment ? <Text className="text-caption text-ink">{myReview.comment}</Text> : null}
            </View>
          ) : (
            <Button
              label={canReview ? "Leave a review" : "Share your feedback"}
              variant="secondary"
              onPress={() => setReviewOpen(true)}
              className="mb-4"
            />
          )
        ) : null}

        {/* Post again. Placed with the other requester actions, and it is the
            only one a finished task offers: `cancellable` is open-only, so this
            and the cancel button below never appear together. Secondary, like
            every action in this stack — the screen's solid CTA is Accept. */}
        {canRepost ? (
          <Button
            label="Post again"
            variant="secondary"
            onPress={() => router.push(`/post-task?duplicate=${task.id}`)}
            className="mb-4"
          />
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

        {task.status === "removed" ? (
          <View className="mb-8 rounded-card border border-line bg-surface p-4">
            <View className="flex-row items-center gap-2">
              <ShieldAlert color={color.danger} size={18} strokeWidth={size.iconStroke} />
              <Text className="text-caption font-semibold text-muted">Task removed</Text>
            </View>
            <Text className="mt-1 text-body text-ink">{removalNotice(task.removal_reason)}</Text>
          </View>
        ) : null}

        {task.status === "cancelled" && task.cancel_reason ? (
          <View className="mb-8 rounded-card border border-line bg-surface p-4">
            <Text className="text-caption font-semibold text-muted">Cancellation reason</Text>
            <Text className="mt-1 text-body text-ink">{task.cancel_reason}</Text>
          </View>
        ) : null}

        {/* Accept action. acceptError stays inside: those are failures that
            leave the task acceptable, so the button is still here to retry. */}
        {isAvailableToAccept ? (
          <View className="mb-8 gap-2">
            {/* The payouts gate takes the screen's one solid CTA when it is
                up (DESIGN.md §1): "Accept task" cannot succeed until payouts
                are set up, so offering both would be offering a button that
                is guaranteed to fail. Brand tint rather than danger — nothing
                went wrong, there is just a step missing. */}
            {payoutsGate !== null ? (
              <View className="gap-3 rounded-card bg-brand-tint p-4">
                <Text className="text-body font-semibold text-brand">
                  Set up payouts to start earning
                </Text>
                <Text className="text-caption text-brand">
                  {payoutsGate ||
                    "Add your bank details through Stripe. It only takes a couple of minutes."}
                </Text>
                <Button label="Set up payouts" onPress={() => router.push("/profile/earnings")} />
                <PressableScale
                  onPress={() => setPayoutsGate(null)}
                  hitSlop={8}
                  className="min-h-11 justify-center"
                >
                  <Text className="text-caption font-semibold text-brand">Not now</Text>
                </PressableScale>
              </View>
            ) : (
              <>
                <Button label="Accept task" onPress={handleAccept} loading={accepting} />
                {acceptError ? (
                  <Text className="text-caption text-danger">{acceptError}</Text>
                ) : null}
              </>
            )}
          </View>
        ) : null}

        {/* Lost the accept race. Outside the block above because that one is
            gone by now — the refetch turned this into an assigned task. Brand
            tint, not danger: nothing went wrong, someone else was quicker. */}
        {acceptLost ? (
          <View className="mb-8 rounded-card bg-brand-tint p-4">
            <Text className="text-body text-brand">{ACCEPT_LOST_COPY}</Text>
          </View>
        ) : null}
      </ScrollView>

      <CancelTaskSheet
        visible={cancelOpen}
        willReleaseMessage={holdWillBeReleasedMessage(task.payment)}
        onClose={() => setCancelOpen(false)}
        onConfirm={handleCancelConfirm}
      />
      {questionnaireActive ? (
        <TractionReviewSheet
          visible={reviewOpen}
          role={isRequester ? "requester" : "supporter"}
          onClose={() => setReviewOpen(false)}
          onSubmit={handleReviewSubmit}
        />
      ) : (
        <ReviewSheet visible={reviewOpen} onClose={() => setReviewOpen(false)} onSubmit={handleReviewSubmit} />
      )}
      <CompleteTaskSheet
        visible={completeOpen}
        approvedBudgetCents={approvedBudgetCents}
        toleranceCents={extensions?.tolerance_cents ?? 0}
        onClose={() => setCompleteOpen(false)}
        onSubmit={handleCompleteSubmit}
      />
      <BudgetIncreaseSheet
        visible={budgetOpen}
        approvedBudgetCents={approvedBudgetCents}
        timeoutMinutes={extensions?.timeout_minutes ?? 0}
        reasons={extensions?.budget_reasons ?? []}
        onClose={() => setBudgetOpen(false)}
        onSubmit={handleBudgetRequest}
      />
    </Screen>
  );
}

// How long is left on a request, in the words someone glancing at a lock
// screen needs. Counts against the server's own deadline (expires_at), never
// the phone's idea of five minutes from when the screen loaded.
function formatCountdown(expiresAt: string, nowMs: number): string {
  const remaining = new Date(expiresAt).getTime() - nowMs;
  if (remaining <= 0) return "time's up";
  const seconds = Math.ceil(remaining / 1000);
  if (seconds < 60) return `${seconds}s left`;
  return `${Math.ceil(seconds / 60)} min left`;
}

// What the supporter is asking for, in one phrase, for whichever kind it is.
function askPhrase(request: ExtensionRequest): string {
  if (request.kind === "budget") {
    return `${formatCost(request.requested_cents ?? 0)} more`;
  }
  return `${request.requested_minutes ?? 0} more minutes`;
}

// The requester's one-tap decision.
//
// Approve is the only solid button on the screen while this is up (DESIGN.md
// §5) — a requester's active task otherwise has no primary CTA, and this is
// unambiguously the thing they opened the app to do.
function ApprovalCard({
  request,
  approvedBudgetCents,
  supporterName,
  busy,
  error,
  nowMs,
  onApprove,
  onDeny,
}: {
  request: ExtensionRequest;
  approvedBudgetCents: number;
  supporterName: string;
  busy: boolean;
  error: string | null;
  nowMs: number;
  onApprove: () => void;
  onDeny: () => void;
}) {
  const isBudget = request.kind === "budget";
  return (
    <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
      <Text className="text-caption font-semibold text-muted">
        {isBudget ? "Budget request" : "Time request"}
      </Text>
      <Text className="text-body text-ink">
        {supporterName} is asking for {askPhrase(request)}.
      </Text>
      {/* reason_label, not reason: the stored value is a slug, and
          "item_unavailable" is not something to show somebody deciding whether
          to spend money. Falls back to `reason` for requests written before
          the presets existed, which hold free text. */}
      {request.reason_label || request.reason ? (
        <Text className="text-caption text-ink">{request.reason_label || request.reason}</Text>
      ) : null}
      {isBudget ? (
        <View className="flex-row justify-between">
          <Text className="text-caption text-muted">New budget if you approve</Text>
          <Text className="text-caption text-ink">
            {formatCost(approvedBudgetCents + (request.requested_cents ?? 0))}
          </Text>
        </View>
      ) : null}
      <Text className="text-caption text-muted">
        {formatCountdown(request.expires_at, nowMs)} — no answer counts as a no.
      </Text>
      {error ? <Text className="text-caption text-danger">{error}</Text> : null}
      <View className="gap-2">
        <Button label="Approve" onPress={onApprove} loading={busy} />
        <Button label="Not this time" variant="secondary" onPress={onDeny} disabled={busy} />
      </View>
    </View>
  );
}

// The supporter's standing picture of what they may spend and how long they
// are paid for, plus the two ways to ask for more.
//
// The approved budget is shown AT ALL TIMES on a shopping task, not only when
// something is outstanding: it is the number they are about to be held to at
// the till, and a supporter who has to remember it is a supporter who will pay
// the difference themselves.
function SupporterAskCard({
  approvedBudgetCents,
  capState,
  pending,
  latest,
  timeChoices,
  timeoutMinutes,
  busy,
  error,
  nowMs,
  onAskBudget,
  onAskTime,
}: {
  approvedBudgetCents: number;
  capState: TimeCapState | null;
  pending: ExtensionRequest | null;
  latest: ExtensionRequest | null;
  timeChoices: number[];
  timeoutMinutes: number;
  busy: boolean;
  error: string | null;
  nowMs: number;
  onAskBudget: () => void;
  onAskTime: (minutes: number) => void;
}) {
  const hasBudget = approvedBudgetCents > 0;
  // A resolved request is worth showing only until it has been superseded —
  // and above all when it was DENIED or EXPIRED, because that is the moment
  // the supporter's own fallback becomes the instruction.
  const showResolved =
    !pending && latest && (latest.status === "denied" || latest.status === "expired");

  if (!hasBudget && !capState && !pending && !showResolved) return null;

  return (
    <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
      <Text className="text-caption font-semibold text-muted">What you're covered for</Text>

      {hasBudget ? (
        <View className="flex-row justify-between">
          <Text className="text-caption text-muted">Approved budget</Text>
          <Text className="text-caption text-ink">{formatCost(approvedBudgetCents)}</Text>
        </View>
      ) : null}

      {capState && capState.cap.cap_minutes > 0 ? (
        <View className="gap-1">
          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">Paid time</Text>
            <Text className="text-caption text-ink">
              {formatMinutes(capState.logged_minutes)} of {formatMinutes(capState.cap.cap_minutes)}
            </Text>
          </View>
          {/* Where the ceiling above comes from. Without it, 45 is a second
              unexplained number sitting next to the 30 the requester was
              quoted. Same line the requester sees. */}
          {timeBasisNote(capState.cap) ? (
            <Text className="text-caption text-muted">{timeBasisNote(capState.cap)}</Text>
          ) : null}
        </View>
      ) : null}

      {/* Layer 1, in the supporter's hands. Never an instruction to stop —
          they decide when it is safe to wrap up — only a statement that the
          meter has stopped. */}
      {capState?.reached ? (
        <Text className="text-caption text-danger">
          Time cap reached — anything past this isn't billed. Ask for more time, or wrap up
          whenever you judge it right. The task can still be completed at any point.
        </Text>
      ) : capState?.warning ? (
        <Text className="text-caption text-muted">
          About {formatMinutes(capState.remaining_minutes)} left on the time that was agreed.
        </Text>
      ) : null}

      {pending ? (
        <View className="gap-1 border-t border-line pt-3">
          <Text className="text-caption text-ink">
            Waiting on an answer: {askPhrase(pending)}.
          </Text>
          <Text className="text-caption text-muted">
            {formatCountdown(pending.expires_at, nowMs)}
            {pending.fallback_instruction ? "" : ` — no answer after ${timeoutMinutes} min counts as a no.`}
          </Text>
        </View>
      ) : null}

      {showResolved && latest ? (
        <View className="gap-1 border-t border-line pt-3">
          <Text className="text-caption text-ink">
            {latest.status === "expired"
              ? `No response to your request for ${askPhrase(latest)}.`
              : `Your request for ${askPhrase(latest)} wasn't approved.`}
          </Text>
          {latest.fallback_instruction ? (
            <Text className="text-caption font-semibold text-ink">
              {latest.fallback_instruction}
            </Text>
          ) : null}
        </View>
      ) : null}

      {error ? <Text className="text-caption text-danger">{error}</Text> : null}

      {/* Secondary throughout: Clock out is this screen's one solid CTA. */}
      {!pending ? (
        <View className="gap-2 border-t border-line pt-3">
          {hasBudget ? (
            <Button
              label="Ask for more budget"
              variant="secondary"
              onPress={onAskBudget}
              disabled={busy}
            />
          ) : null}
          {/* Offered only once the ceiling is in sight. Before that it is an
              answer to a question nobody has asked. */}
          {(capState?.warning || capState?.reached) && timeChoices.length > 0 ? (
            <View className="flex-row gap-2">
              {timeChoices.map((minutes) => (
                <Button
                  key={minutes}
                  label={`Ask for +${minutes} min`}
                  variant="secondary"
                  onPress={() => onAskTime(minutes)}
                  disabled={busy}
                  className="flex-1"
                />
              ))}
            </View>
          ) : null}
        </View>
      ) : null}
    </View>
  );
}

// The settlement, itemized, for both roles.
//
// Every number here was computed in Go and is rendered verbatim (S-05). The
// only thing this component decides is which of them a given reader sees: the
// receipt photo is the requester's evidence of what their money bought and the
// supporter's own upload, so both see it — but only the requester is offered
// the "something's wrong" route, because they are the one who was charged.
/**
 * The sessions, and the free gaps between them, in order.
 *
 * ONE component for the running task and the finished settlement — the same
 * rows in both places, which is the point: a requester who watched the
 * timeline build up during the task should meet exactly that timeline again on
 * the receipt, not a different summary of it. Forking this into a
 * "settlement breakdown" is how the two start disagreeing.
 *
 * A gap is visually quieter than a session (muted, no time range emphasis) and
 * carries the reason on its own row, because "free" is the fact a reader is
 * scanning for and it should not need to be inferred from two adjacent rows.
 */
function SessionList({ timeline }: { timeline: readonly TimelineEntry[] }) {
  return (
    <View className="gap-2">
      {timeline.map((entry) =>
        entry.kind === "gap" ? (
          <View key={`gap-${entry.startAt}`} className="flex-row justify-between">
            <Text className="text-caption text-muted">{GAP_LABEL}</Text>
            <Text className="text-caption text-muted">{formatMinutes(entry.minutes)} free</Text>
          </View>
        ) : (
          <View key={entry.id} className="flex-row justify-between">
            <Text className="text-caption text-ink">
              {formatClock(entry.startAt)} –{" "}
              {entry.endAt ? formatClock(entry.endAt) : "in progress"}
            </Text>
            <Text className="text-caption text-muted">
              {formatMinutes(entry.minutes)}
              {entry.running ? " so far" : ""}
            </Text>
          </View>
        )
      )}
    </View>
  );
}

function SettlementCard({
  cost,
  settlement,
  timeline,
  isRequester,
  onReportProblem,
}: {
  cost: TaskCost;
  settlement: Settlement;
  timeline: readonly TimelineEntry[];
  isRequester: boolean;
  onReportProblem: () => void;
}) {
  const overran =
    cost.cap_minutes !== undefined &&
    cost.billed_minutes !== undefined &&
    cost.total_minutes > cost.billed_minutes;

  return (
    <View className="mb-4 gap-3 rounded-card border border-line bg-surface p-4">
      <Text className="text-caption font-semibold text-muted">
        {settlement.state === "captured" ? "What was charged" : "Settlement"}
      </Text>

      <View className="flex-row justify-between">
        <Text className="text-caption text-muted">Base fee</Text>
        <Text className="text-caption text-ink">{formatCost(cost.base_fee_cents)}</Text>
      </View>
      <Text className="-mt-2 text-caption text-muted">
        Covers the first {cost.included_minutes} minutes.
      </Text>

      <View className="flex-row justify-between">
        <Text className="text-caption text-muted">
          {cost.billable_minutes} billable min × {formatCost(cost.per_minute_rate_cents)}
        </Text>
        <Text className="text-caption text-ink">{formatCost(cost.time_cost_cents)}</Text>
      </View>

      {/* WHERE THOSE MINUTES CAME FROM. Only when the task was worked in more
          than one sitting — on a single-session task the timeline restates the
          line above it and earns nothing.

          The same rows the Progress card showed while the task was running,
          from the same builder, so the receipt agrees with what both parties
          watched being assembled. The gaps are the reason a requester might
          otherwise read "3 hours on site, 40 billable minutes" as an error. */}
      {timeline.filter((e) => e.kind === "session").length > 1 ? (
        <View className="gap-2 border-t border-line pt-3">
          <Text className="text-caption font-semibold text-muted">Sessions</Text>
          <SessionList timeline={timeline} />
          {/* Logged, not billable — the two differ by the 15-minute inclusion
              and by any time past the ceiling, and a reader adding the rows up
              needs the number their addition should produce. */}
          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">Total time</Text>
            <Text className="text-caption text-ink">{formatMinutes(cost.total_minutes)}</Text>
          </View>
          {gapsNote(timeline) ? (
            <Text className="-mt-1 text-caption text-muted">{gapsNote(timeline)}</Text>
          ) : null}
        </View>
      ) : null}

      {/* Said plainly rather than buried: the supporter worked longer than the
          requester agreed to pay for, and both of them should see that in the
          same words. */}
      {overran ? (
        <Text className="-mt-2 text-caption text-muted">
          {formatMinutes(cost.total_minutes)} logged; billed to the agreed{" "}
          {formatMinutes(cost.billed_minutes ?? 0)}.
        </Text>
      ) : null}

      {settlement.approved_budget_cents > 0 ? (
        <View className="flex-row justify-between">
          <Text className="text-caption text-muted">
            Receipt {settlement.approved_budget_cents > 0
              ? `(budget ${formatCost(settlement.approved_budget_cents)})`
              : ""}
          </Text>
          <Text className="text-caption text-ink">
            {formatCost(cost.shopping_receipt_cents ?? 0)}
          </Text>
        </View>
      ) : null}

      <View className="flex-row justify-between border-t border-line pt-2">
        <Text className="text-body font-semibold text-ink">
          {settlement.state === "captured" ? "Total charged" : "Total"}
        </Text>
        <Text className="text-body font-semibold text-ink">{formatCost(cost.total_cents)}</Text>
      </View>

      {settlement.state === "not_charged" ? (
        <Text className="text-caption text-muted">
          Nothing has been charged — payments are not switched on for this task.
        </Text>
      ) : null}
      {/* Deliberately calm, and deliberately not an action. Ops have already
          been told; there is nothing for either party to do, and a red alarm
          here would send both of them chasing something already in hand. */}
      {settlement.state === "capture_failed" ? (
        <Text className="text-caption text-muted">
          We couldn't complete the payment for this task. The HO:RA team has been notified and will
          sort it out — there's nothing you need to do.
        </Text>
      ) : null}

      {/* What the SUPPORTER earned, and only ever on their own copy: the
          server attaches `earned` behind an assignment check, so a requester's
          settlement has no such key. The requester's version of this card says
          what they were CHARGED — one settlement, two disjoint views, and
          neither is ever shown to the other party. */}
      {settlement.earned ? (
        <View className="gap-1 border-t border-line pt-3">
          <Text className="text-caption font-semibold text-muted">You earned</Text>
          <View className="flex-row justify-between">
            <Text className="flex-1 pr-2 text-caption text-muted">
              {formatCost(settlement.earned.time_cents)} (time)
              {settlement.earned.reimbursement_cents > 0
                ? ` + ${formatCost(settlement.earned.reimbursement_cents)} (reimbursement)`
                : ""}
            </Text>
            <Text className="text-body font-semibold text-ink">
              {formatCost(settlement.earned.total_cents)}
            </Text>
          </View>
          {/* "On its way", never "paid". Stripe executes the transfer when the
              requester's charge settles, and the bank deposit is a further step
              on its daily payout schedule — telling somebody the money is in
              their account when it is two days out is how tickets get made. */}
          {settlement.earned.payout_status === "paid" ? (
            <Text className="text-caption text-muted">On its way to your bank.</Text>
          ) : null}
          {settlement.earned.payout_status === "failed" ? (
            <Text className="text-caption text-muted">
              We couldn&apos;t send this yet. The HO:RA team has been notified — there&apos;s
              nothing you need to do.
            </Text>
          ) : null}
        </View>
      ) : null}

      {settlement.receipt_photo_url ? (
        <View className="gap-2">
          <Text className="text-caption text-muted">Receipt</Text>
          <Image
            source={{ uri: settlement.receipt_photo_url }}
            className="h-40 w-full rounded-sm"
            resizeMode="cover"
          />
        </View>
      ) : null}

      {isRequester ? (
        <PressableScale onPress={onReportProblem} hitSlop={8} className="min-h-11 justify-center">
          <Text className="text-caption font-semibold text-brand">Report a problem</Text>
        </PressableScale>
      ) : null}
    </View>
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
