import AsyncStorage from "@react-native-async-storage/async-storage";
import * as Location from "expo-location";
import * as SecureStore from "expo-secure-store";
import * as TaskManager from "expo-task-manager";
import { getWorklogs, sendGpsPing } from "./api";
import { ApiError } from "./api-error";

// Background GPS while a supporter is clocked in. The foreground `setInterval`
// on the task screen only runs while the app is awake, so a locked phone
// produced a 15-60 min hole in the trail between clock-in and clock-out. iOS
// delivers location updates to a TaskManager task in *both* states, so this
// replaces the interval whenever "Always" permission is granted; the interval
// stays as the fallback for "When In Use" only (see task/[id].tsx `gpsMode`).
//
// Everything here runs headless — the task executor can fire with no React
// tree mounted at all — so no hooks, no component state, no navigation.
//
// Storage is AsyncStorage, NOT SecureStore. This is the fix for the bug that
// killed every session on the first screen lock: SecureStore writes with
// kSecAttrAccessibleWhenUnlocked, so reading it on a locked device *throws*
// (errSecInteractionNotAllowed). The old code caught that throw as `null`,
// read `null` as "nothing is being tracked", and stopped the session for
// good. AsyncStorage has no lock semantics, and none of this is secret — the
// task id is already in the URL of the screen the supporter is looking at.
//
// The second half of that fix is the invariant below, which holds even if a
// read fails for some other reason.
//
// Auth needs no plumbing: apiFetch sends the `hora_session` cookie via
// `credentials: "include"`, and that cookie lives in the native cookie store,
// which the headless task shares with the app.

export const GPS_TRACKING_TASK = "hora-gps-tracking";

// The task the supporter is currently clocked in on, plus when tracking for it
// started. Written before the updates start so the first delivered fix already
// finds it.
const ACTIVE_SLOT_KEY = "hora_active_gps_task";

// Where build 5 kept the same value. Read paths no longer look here; reconcile
// deletes it once so a stale keychain entry can't outlive the app.
const LEGACY_SECURE_SLOT_KEY = "hora_active_gps_task";

// What the background task leaves behind so a field test can tell "the task
// never fired" from "the task fired but the POST failed" — without Xcode.
const BREADCRUMB_KEY = "hora_gps_breadcrumb";

// No event for this long means the session is dead (or iOS stopped feeding
// it) and the foreground interval should take over while we restart.
export const GPS_STALE_MS = 90_000;

// iOS ignores `timeInterval` (it is Android-only in expo-location); real
// cadence comes from `distanceInterval` plus CoreLocation's own batching, so
// a stationary supporter yields a sparse — but continuous — trail.
const TRACKING_OPTIONS: Location.LocationTaskOptions = {
  accuracy: Location.Accuracy.Balanced,
  timeInterval: 30_000,
  distanceInterval: 25,
  pausesUpdatesAutomatically: false,
  // Native defaults to ActivityType.Other. OtherNavigation tells CoreLocation
  // this is someone travelling to and around a job, which keeps the GPS a
  // little warmer. A cheap hint, not something the fix relies on.
  activityType: Location.ActivityType.OtherNavigation,
  // The blue status bar while we track in the background. Non-negotiable for
  // this feature: the supporter must be able to see that it is on.
  showsBackgroundLocationIndicator: true,
};

export interface GpsBreadcrumb {
  /** Epoch ms of the last time the task executor ran at all. */
  lastEventAt: number | null;
  /** Epoch ms of the last ping the backend accepted. */
  lastPingAt: number | null;
  /** `location.timestamp` of the newest fix already posted — the dedupe key. */
  lastPostedAt: number | null;
  events: number;
  pings: number;
  /** Last thing worth reading in a field test ("403 - stopped", "start busy"). */
  note: string | null;
}

const EMPTY_BREADCRUMB: GpsBreadcrumb = {
  lastEventAt: null,
  lastPingAt: null,
  lastPostedAt: null,
  events: 0,
  pings: 0,
  note: null,
};

interface ActiveSlot {
  taskId: string;
  startedAt: number;
}

// THE INVARIANT: a read that FAILED is not the same as a read that came back
// empty, and only the latter may ever trigger a stop. Every read path below
// returns this shape so the difference cannot be flattened away by a
// `.catch(() => null)` again — that flattening is precisely what killed every
// session in the Aug 28 field tests.
type SlotRead = { ok: true; slot: ActiveSlot | null } | { ok: false };

function log(...args: unknown[]): void {
  if (__DEV__) {
    // eslint-disable-next-line no-console
    console.log("[gps-bg]", ...args);
  }
}

async function readActiveSlot(): Promise<SlotRead> {
  let raw: string | null;
  try {
    raw = await AsyncStorage.getItem(ACTIVE_SLOT_KEY);
  } catch (e) {
    log("slot read FAILED", e instanceof Error ? e.message : e);
    return { ok: false };
  }
  if (raw === null) return { ok: true, slot: null };
  try {
    const parsed = JSON.parse(raw) as ActiveSlot;
    if (typeof parsed?.taskId !== "string" || parsed.taskId === "") return { ok: false };
    return { ok: true, slot: parsed };
  } catch {
    // Garbage in the slot is a broken read, not an empty one. Never a stop.
    log("slot unparseable, treating as unknown");
    return { ok: false };
  }
}

async function writeActiveSlot(slot: ActiveSlot): Promise<void> {
  await AsyncStorage.setItem(ACTIVE_SLOT_KEY, JSON.stringify(slot)).catch((e) => {
    log("slot write failed", e instanceof Error ? e.message : e);
  });
}

async function clearActiveSlot(): Promise<void> {
  await AsyncStorage.removeItem(ACTIVE_SLOT_KEY).catch(() => {});
}

export async function readBreadcrumb(): Promise<GpsBreadcrumb | null> {
  try {
    const raw = await AsyncStorage.getItem(BREADCRUMB_KEY);
    return raw ? (JSON.parse(raw) as GpsBreadcrumb) : null;
  } catch {
    return null;
  }
}

async function writeBreadcrumb(patch: Partial<GpsBreadcrumb>): Promise<void> {
  try {
    const current = (await readBreadcrumb()) ?? EMPTY_BREADCRUMB;
    await AsyncStorage.setItem(BREADCRUMB_KEY, JSON.stringify({ ...current, ...patch }));
  } catch {
    // Observability must never be able to break tracking.
  }
}

// A one-at-a-time gate. Two of these below, for the two places where
// concurrent callers corrupt each other.
function makeGate() {
  let chain: Promise<unknown> = Promise.resolve();
  return function run<T>(fn: () => Promise<T>): Promise<T> {
    const result = chain.then(fn, fn);
    chain = result.catch(() => {});
    return result;
  };
}

// Two CoreLocation subscriptions feed one delegate (expo-location's task
// consumer calls both startUpdatingLocation and
// startMonitoringSignificantLocationChanges), so the same fix can arrive
// twice and run this executor twice concurrently — prod showed pairs of
// identical rows 254us apart. Serialising the runs is what makes the
// timestamp dedupe below actually see the earlier run's result.
const executorGate = makeGate();

// The task screen's start effect and its health check can both reach
// startUpdatesFor at once. Without this, both see "not running" and the loser
// throws "already started".
const startGate = makeGate();

// Defined at module scope, imported for side effect from the root layout, so
// the executor is registered before iOS can hand us a headless launch.
TaskManager.defineTask<{ locations: Location.LocationObject[] }>(
  GPS_TRACKING_TASK,
  async ({ data, error }) =>
    executorGate(async () => {
      // Never throw out of here — an unhandled rejection in a headless task is
      // a crash the user cannot see and cannot recover from.
      try {
        if (error) {
          log("executor error", error.message);
          await writeBreadcrumb({ lastEventAt: Date.now(), note: `event error: ${error.message}` });
          return;
        }
        const locations = data?.locations ?? [];
        const now = Date.now();
        const seen = (await readBreadcrumb()) ?? EMPTY_BREADCRUMB;
        await writeBreadcrumb({ lastEventAt: now, events: seen.events + 1 });
        if (locations.length === 0) return;

        const read = await readActiveSlot();
        if (!read.ok) {
          // Unknown, not empty. Drop these fixes and keep the session — this
          // is the branch whose old behaviour ("stop") caused the outage.
          await writeBreadcrumb({ note: "slot unreadable, session kept" });
          return;
        }
        if (read.slot === null) {
          // An explicit empty slot with updates still running is a genuine
          // orphan — the only read result allowed to stop the session.
          log("slot explicitly empty, stopping");
          await writeBreadcrumb({ note: "slot empty, stopped" });
          await stopBackgroundGps();
          return;
        }

        const { taskId } = read.slot;

        // Dedupe on the fix's own timestamp, both within this batch and
        // against what earlier runs already posted. `lastPostedAt` is
        // monotonic, so a redelivered older fix is dropped too.
        let lastPostedAt = seen.lastPostedAt ?? 0;
        const fresh = locations
          .filter((l) => l.timestamp > lastPostedAt)
          .sort((a, b) => a.timestamp - b.timestamp);
        if (fresh.length === 0) {
          await writeBreadcrumb({ note: "duplicate batch, nothing new" });
          return;
        }

        // POST /tasks/:id/gps-ping takes one fix per request (no batch shape on
        // the endpoint), so a multi-fix callback goes out sequentially.
        let posted = 0;
        for (const location of fresh) {
          try {
            await sendGpsPing(taskId, {
              lat: location.coords.latitude,
              lng: location.coords.longitude,
              accuracy: location.coords.accuracy != null ? Math.round(location.coords.accuracy) : undefined,
              source: "background",
            });
            posted += 1;
            lastPostedAt = location.timestamp;
            await writeBreadcrumb({
              lastPingAt: Date.now(),
              lastPostedAt,
              pings: seen.pings + posted,
              note: null,
            });
          } catch (e) {
            if (e instanceof ApiError && e.status === 403) {
              // "not clocked in" — the worklog was closed somewhere this device
              // never saw (web, another device, an admin action). The backend
              // guard is the source of truth, so let it self-heal the orphan.
              log("403 from ping, worklog closed — stopping");
              await writeBreadcrumb({ note: "403 not clocked in, stopped" });
              await stopBackgroundGps();
              return;
            }
            // Anything else (offline, 5xx) is transient: drop this fix, keep
            // tracking. Pings are best-effort by design.
            log("ping failed", e instanceof Error ? e.message : e);
            await writeBreadcrumb({ note: `ping failed: ${e instanceof Error ? e.message : "unknown"}` });
          }
        }
      } catch (e) {
        log("unexpected executor failure", e instanceof Error ? e.message : e);
      }
    })
);

async function updatesRunning(): Promise<boolean> {
  return Location.hasStartedLocationUpdatesAsync(GPS_TRACKING_TASK).catch(() => false);
}

// Claim the session for `taskId` when that is safe. Returns false when some
// other task already owns a running session — the caller falls back to the
// foreground interval rather than silently relabelling the other task's pings,
// which is how Thomas's two overlapping worklogs got mixed up on Aug 28.
async function claimRunningSession(taskId: string): Promise<boolean> {
  const read = await readActiveSlot();
  if (!read.ok) {
    // Can't tell whose session it is. Don't clobber, don't stop, don't claim.
    log("running session with unreadable slot — leaving it alone");
    await writeBreadcrumb({ note: "start: slot unreadable" });
    return false;
  }
  if (read.slot === null) {
    // Running with no owner: adopt it rather than restart it.
    await writeActiveSlot({ taskId, startedAt: Date.now() });
    return true;
  }
  if (read.slot.taskId === taskId) return true;
  log("session belongs to task", read.slot.taskId, "— not claiming for", taskId);
  await writeBreadcrumb({ note: `start: busy with ${read.slot.taskId}` });
  return false;
}

/**
 * Ask for "Always" location and start background updates for `taskId`.
 * Returns true only if this task's fixes will flow from the background
 * session. Never throws.
 */
export async function startBackgroundGps(taskId: string): Promise<boolean> {
  try {
    // Foreground first: on iOS "Always" is only offered once When-In-Use is
    // held, and requesting background alone can resolve to denied outright.
    const foreground = await Location.requestForegroundPermissionsAsync();
    if (foreground.status !== Location.PermissionStatus.GRANTED) return false;

    const background = await Location.requestBackgroundPermissionsAsync();
    if (background.status !== Location.PermissionStatus.GRANTED) return false;

    return await startUpdatesFor(taskId);
  } catch (e) {
    log("start failed", e instanceof Error ? e.message : e);
    await writeBreadcrumb({ note: `start failed: ${e instanceof Error ? e.message : "unknown"}` });
    // Deliberately NOT clearing the slot here. A throw from
    // startLocationUpdatesAsync can mean "already started", and deleting the
    // slot would leave a live session with no owner that kills itself on its
    // next event. A slot with no session is harmless — reconcile clears it.
    return false;
  }
}

/**
 * Bring the session back without prompting. Used by the health check, which
 * runs on a timer and on every foreground — prompting there would be a
 * permission dialog every 30 seconds. Never throws.
 */
export async function restartBackgroundGps(taskId: string): Promise<boolean> {
  try {
    const background = await Location.getBackgroundPermissionsAsync();
    if (background.status !== Location.PermissionStatus.GRANTED) return false;
    return await startUpdatesFor(taskId);
  } catch (e) {
    log("restart failed", e instanceof Error ? e.message : e);
    await writeBreadcrumb({ note: `restart failed: ${e instanceof Error ? e.message : "unknown"}` });
    return false;
  }
}

function startUpdatesFor(taskId: string): Promise<boolean> {
  return startGate(async () => {
    if (await updatesRunning()) return claimRunningSession(taskId);

    // Persist before starting: the first fix can arrive immediately, and a
    // slot it can't read would drop that fix.
    await writeActiveSlot({ taskId, startedAt: Date.now() });
    await Location.startLocationUpdatesAsync(GPS_TRACKING_TASK, TRACKING_OPTIONS);
    await writeBreadcrumb({ note: `started for ${taskId}` });
    log("started for task", taskId);
    return true;
  });
}

/**
 * Stop background updates and forget the active task. Idempotent and safe to
 * call when nothing was ever started — the running guard keeps
 * `stopLocationUpdatesAsync` from throwing on an unstarted task. Never throws.
 */
export async function stopBackgroundGps(): Promise<void> {
  await clearActiveSlot();
  if (!(await updatesRunning())) return;
  await Location.stopLocationUpdatesAsync(GPS_TRACKING_TASK).catch((e) => {
    log("stop failed", e instanceof Error ? e.message : e);
  });
  log("stopped");
}

/**
 * Stop only if `taskId` is the task being tracked. The task screen calls this
 * whenever it renders a task with no open worklog — without the ownership
 * check, opening task B would kill live tracking for the task A the supporter
 * is actually clocked in on.
 */
export async function stopBackgroundGpsFor(taskId: string): Promise<void> {
  const read = await readActiveSlot();
  // Unknown ownership is never grounds for stopping (the invariant).
  if (!read.ok) return;
  // Explicitly empty: a running session with no owner is an orphan.
  if (read.slot !== null && read.slot.taskId !== taskId) return;
  await stopBackgroundGps();
}

/**
 * Is the background session actually alive and feeding this task? Drives the
 * health check on the task screen. Optimistic on an unreadable slot so a
 * storage hiccup can't cause a restart loop.
 */
export async function isBackgroundGpsHealthy(taskId: string): Promise<boolean> {
  if (!(await updatesRunning())) return false;
  const read = await readActiveSlot();
  if (!read.ok) return true;
  if (read.slot === null || read.slot.taskId !== taskId) return false;

  const breadcrumb = await readBreadcrumb();
  const now = Date.now();
  // Grace period: a session that just started hasn't had time to deliver yet.
  if (now - read.slot.startedAt < GPS_STALE_MS) return true;
  if (breadcrumb?.lastEventAt == null) return false;
  return now - breadcrumb.lastEventAt < GPS_STALE_MS;
}

/**
 * Guard against tracking that outlived its worklog: a crash, a force-quit
 * mid-task, or a clock-out that happened on another device. Asks the backend
 * whether the persisted task still has an open worklog and stops if it does
 * not. Runs at launch and on foreground — deliberately NOT on Supabase auth
 * events, where a routine token refresh used to be able to end a live session.
 * No-op when nothing is running. Never throws.
 */
export async function reconcileBackgroundGps(): Promise<void> {
  try {
    // Build 5 kept the active task in the keychain. Nothing reads it now; drop
    // it so it doesn't linger on upgraded devices.
    SecureStore.deleteItemAsync(LEGACY_SECURE_SLOT_KEY).catch(() => {});

    if (!(await updatesRunning())) {
      // Nothing running; drop any slot left behind so a later start is clean.
      await clearActiveSlot();
      return;
    }
    const read = await readActiveSlot();
    if (!read.ok) return;
    if (read.slot === null) {
      await stopBackgroundGps();
      return;
    }
    const summary = await getWorklogs(read.slot.taskId);
    const open = summary?.worklogs?.some((wl) => wl.end_at === null) ?? false;
    if (!open) {
      log("no open worklog for", read.slot.taskId, "— stopping");
      await stopBackgroundGps();
    }
  } catch (e) {
    // A definite answer from the server (task gone, session invalid) means the
    // tracking can never be legitimate again — stop. A network failure is not
    // an answer, so leave it running and reconcile at the next foreground.
    if (e instanceof ApiError) {
      log("reconcile got", e.status, "— stopping");
      await stopBackgroundGps();
      return;
    }
    log("reconcile skipped", e instanceof Error ? e.message : e);
  }
}
