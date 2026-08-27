import * as Location from "expo-location";
import * as SecureStore from "expo-secure-store";
import * as TaskManager from "expo-task-manager";
import { getWorklogs, sendGpsPing } from "./api";
import { ApiError } from "./api-error";

// Background GPS while a supporter is clocked in. The foreground `setInterval`
// on the task screen only runs while the app is awake, so a locked phone
// produced a 15–60 min hole in the trail between clock-in and clock-out. iOS
// delivers location updates to a TaskManager task in *both* states, so this
// replaces the interval whenever "Always" permission is granted; the interval
// stays as the fallback for "When In Use" only (see task/[id].tsx `gpsMode`).
//
// Everything here runs headless — the task executor can fire with no React
// tree mounted at all — so no hooks, no component state, no navigation. The
// only cross-launch state is the active task id in SecureStore.
//
// Auth needs no plumbing: apiFetch sends the `hora_session` cookie via
// `credentials: "include"`, and that cookie lives in the native cookie store,
// which the headless task shares with the app.

export const GPS_TRACKING_TASK = "hora-gps-tracking";

// The task the supporter is currently clocked in on. Written before the
// updates start so the first delivered fix already finds it.
const ACTIVE_TASK_KEY = "hora_active_gps_task";

// iOS ignores `timeInterval` (it is Android-only in expo-location); real
// cadence comes from `distanceInterval` plus CoreLocation's own batching, so
// a stationary supporter yields a sparse — but continuous — trail. Accepted
// deliberately; tune after reading real trails.
const TRACKING_OPTIONS: Location.LocationTaskOptions = {
  accuracy: Location.Accuracy.Balanced,
  timeInterval: 30_000,
  distanceInterval: 25,
  pausesUpdatesAutomatically: false,
  // The blue status bar while we track in the background. Non-negotiable for
  // this feature: the supporter must be able to see that it is on.
  showsBackgroundLocationIndicator: true,
};

function log(...args: unknown[]): void {
  if (__DEV__) {
    // eslint-disable-next-line no-console
    console.log("[gps-bg]", ...args);
  }
}

async function readActiveTaskId(): Promise<string | null> {
  return SecureStore.getItemAsync(ACTIVE_TASK_KEY).catch(() => null);
}

// Defined at module scope, imported for side effect from the root layout, so
// the executor is registered before iOS can hand us a headless launch.
TaskManager.defineTask<{ locations: Location.LocationObject[] }>(
  GPS_TRACKING_TASK,
  async ({ data, error }) => {
    // Never throw out of here — an unhandled rejection in a headless task is
    // a crash the user cannot see and cannot recover from.
    try {
      if (error) {
        log("executor error", error.message);
        return;
      }
      const locations = data?.locations ?? [];
      if (locations.length === 0) return;

      const taskId = await readActiveTaskId();
      if (!taskId) {
        // Updates running with nothing to attribute them to — orphaned.
        log("no active task id, stopping");
        await stopBackgroundGps();
        return;
      }

      // POST /tasks/:id/gps-ping takes one fix per request (no batch shape on
      // the endpoint), so a multi-fix callback goes out sequentially.
      for (const location of locations) {
        try {
          await sendGpsPing(taskId, {
            lat: location.coords.latitude,
            lng: location.coords.longitude,
            accuracy: location.coords.accuracy != null ? Math.round(location.coords.accuracy) : undefined,
            source: "background",
          });
        } catch (e) {
          if (e instanceof ApiError && e.status === 403) {
            // "not clocked in" — the worklog was closed somewhere this device
            // never saw (web, another device, an admin action). The backend
            // guard is the source of truth, so let it self-heal the orphan.
            log("403 from ping, worklog closed — stopping");
            await stopBackgroundGps();
            return;
          }
          // Anything else (offline, 5xx) is transient: drop this fix, keep
          // tracking. Pings are best-effort by design.
          log("ping failed", e instanceof Error ? e.message : e);
        }
      }
    } catch (e) {
      log("unexpected executor failure", e instanceof Error ? e.message : e);
    }
  }
);

/**
 * Ask for "Always" location and start background updates for `taskId`.
 * Returns true only if updates are actually running — the caller keeps the
 * foreground interval when this returns false. Never throws.
 */
export async function startBackgroundGps(taskId: string): Promise<boolean> {
  try {
    // Foreground first: on iOS "Always" is only offered once When-In-Use is
    // held, and requesting background alone can resolve to denied outright.
    const foreground = await Location.requestForegroundPermissionsAsync();
    if (foreground.status !== Location.PermissionStatus.GRANTED) return false;

    const background = await Location.requestBackgroundPermissionsAsync();
    if (background.status !== Location.PermissionStatus.GRANTED) return false;

    const alreadyStarted = await Location.hasStartedLocationUpdatesAsync(GPS_TRACKING_TASK).catch(() => false);
    if (alreadyStarted) {
      // Remount of the task screen during an ongoing clock-in. Re-starting a
      // running task throws, so just re-assert the id and report success.
      await SecureStore.setItemAsync(ACTIVE_TASK_KEY, taskId).catch(() => {});
      return true;
    }

    // Persist before starting: the first fix can arrive immediately, and a
    // task id it can't read would stop tracking as orphaned.
    await SecureStore.setItemAsync(ACTIVE_TASK_KEY, taskId).catch(() => {});
    await Location.startLocationUpdatesAsync(GPS_TRACKING_TASK, TRACKING_OPTIONS);
    log("started for task", taskId);
    return true;
  } catch (e) {
    log("start failed", e instanceof Error ? e.message : e);
    await SecureStore.deleteItemAsync(ACTIVE_TASK_KEY).catch(() => {});
    return false;
  }
}

/**
 * Stop background updates and forget the active task. Idempotent and safe to
 * call when nothing was ever started — the `hasStarted` guard keeps
 * `stopLocationUpdatesAsync` from throwing on an unstarted task. Never throws.
 */
export async function stopBackgroundGps(): Promise<void> {
  await SecureStore.deleteItemAsync(ACTIVE_TASK_KEY).catch(() => {});
  const started = await Location.hasStartedLocationUpdatesAsync(GPS_TRACKING_TASK).catch(() => false);
  if (!started) return;
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
  const active = await readActiveTaskId();
  // `active === null` with updates still running is itself an orphan, so fall
  // through and clean it up.
  if (active !== null && active !== taskId) return;
  await stopBackgroundGps();
}

/**
 * App-launch guard against tracking that outlived its worklog: a crash, a
 * force-quit mid-task, or a clock-out that happened on another device. Asks
 * the backend whether the persisted task still has an open worklog and stops
 * if it does not. No-op when nothing is running. Never throws.
 */
export async function reconcileBackgroundGps(): Promise<void> {
  try {
    const started = await Location.hasStartedLocationUpdatesAsync(GPS_TRACKING_TASK).catch(() => false);
    if (!started) {
      // Nothing running; drop any id left behind so a later start is clean.
      await SecureStore.deleteItemAsync(ACTIVE_TASK_KEY).catch(() => {});
      return;
    }
    const taskId = await readActiveTaskId();
    if (!taskId) {
      await stopBackgroundGps();
      return;
    }
    const summary = await getWorklogs(taskId);
    const open = summary?.worklogs?.some((wl) => wl.end_at === null) ?? false;
    if (!open) {
      log("no open worklog for", taskId, "— stopping");
      await stopBackgroundGps();
    }
  } catch (e) {
    // A definite answer from the server (task gone, session invalid) means the
    // tracking can never be legitimate again — stop. A network failure is not
    // an answer, so leave it running and reconcile at the next launch.
    if (e instanceof ApiError) {
      log("reconcile got", e.status, "— stopping");
      await stopBackgroundGps();
      return;
    }
    log("reconcile skipped", e instanceof Error ? e.message : e);
  }
}
