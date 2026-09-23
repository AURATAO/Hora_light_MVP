import { getMe, getTask, getWorklogs } from "./api";
import { ApiError } from "./api-error";
import {
  activeBackgroundGpsTask,
  stopBackgroundGps,
  stopBackgroundGpsFor,
  type GpsPhase,
} from "./gps-tracking";
import { broadcastPhase } from "./live-tracking";
import {
  cancelAllOvertimeReminders,
  cancelOvertimeReminders,
  taskIdsWithPendingReminders,
} from "./overtime-reminders";
import type { NotificationType } from "./types";

/**
 * Everything this device keeps running on a supporter's behalf while a task
 * is live, and the ONE place it is all switched off.
 *
 *   - the background location task (expo-location + TaskManager), with its
 *     blue "HO:RA is using your location" indicator;
 *   - the local "still working?" reminders queued with the OS;
 *   - the task screen's own sharing indicator (gpsMode), which subscribes
 *     here so it follows a teardown from anywhere.
 *
 * WHY ONE FUNCTION. Build 12 leaked both of the first two after a task was
 * completed through the multi-session path (clock in, pause, clock back in,
 * clock out, complete). Each transition stopped what it happened to know
 * about, from the place it happened to run: the clock-out cancelled reminders
 * by stored id, a render effect stopped GPS, completion cancelled reminders
 * again and assumed the effect had done the rest. Three partial teardowns,
 * no whole one, and two ways for a start to land after a stop
 * (gps-tracking.ts stopEpoch, overtime-reminders.ts cancelEpochs). The server
 * was right throughout — it had closed the worklog and was refusing pings —
 * and the phone was wrong until the app was killed.
 *
 * So: every transition out of "this device is tracking this task" calls
 * endTaskTracking, regardless of the screen or path — completion,
 * cancellation (either side, either screen), removal, reassignment, a pause
 * between sessions, a push that says any of those happened elsewhere, and the
 * render effect on the task screen whenever its predicate says "not ours".
 * And resyncTaskTracking runs at every launch and foreground to catch the
 * transition this device never saw.
 *
 * Headless-safe: no hooks, no navigation. Never throws.
 */

export type TeardownReason =
  | "completed"
  | "cancelled"
  | "removed"
  | "reassigned"
  | "paused"
  | "inactive"
  | "notification"
  | "resync"
  | "logout";

type TeardownListener = (taskId: string, reason: TeardownReason) => void;
const listeners = new Set<TeardownListener>();

function log(...args: unknown[]): void {
  if (__DEV__) {
    // eslint-disable-next-line no-console
    console.log("[teardown]", ...args);
  }
}

/**
 * Hear about every teardown, from any path. The task screen uses this to
 * clear its own sharing indicator when the teardown did not originate on it —
 * a push, the foreground re-sync, another screen.
 */
export function onTaskTeardown(listener: TeardownListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function notify(taskId: string, reason: TeardownReason): void {
  for (const listener of listeners) {
    try {
      listener(taskId, reason);
    } catch {
      // A listener's failure is its own; the teardown has already happened.
    }
  }
}

/**
 * THE teardown. Stops background location if it belongs to `taskId` (or to
 * nobody — an orphan), cancels every local reminder for `taskId`, and tells
 * the screens. Ownership-scoped on the GPS side on purpose: opening task B
 * must never end the tracking that belongs to the task A the supporter is
 * actually clocked in on. Idempotent, so calling it twice for the same
 * transition — once from the handler, once from the render effect — is
 * cheap and correct. Never throws.
 */
export async function endTaskTracking(taskId: string, reason: TeardownReason): Promise<void> {
  log(reason, taskId);
  await Promise.all([
    stopBackgroundGpsFor(taskId).catch((e) => log("gps stop failed", e instanceof Error ? e.message : e)),
    cancelOvertimeReminders(taskId).catch((e) =>
      log("reminder cancel failed", e instanceof Error ? e.message : e)
    ),
  ]);
  notify(taskId, reason);
}

/**
 * Logout: no task is ours any more. Stops location whoever owns it and clears
 * every reminder rather than enumerating tasks. Never throws.
 */
export async function endAllTaskTracking(): Promise<void> {
  log("logout — everything");
  // Each task the queue still names goes through the per-task teardown, so its
  // stored reminder record goes with it; then the blanket calls, for whatever
  // has no task id to be found by (a reminder from a build before the tag).
  const pending = await taskIdsWithPendingReminders();
  await Promise.all(pending.map((taskId) => endTaskTracking(taskId, "logout")));
  await Promise.all([stopBackgroundGps().catch(() => {}), cancelAllOvertimeReminders().catch(() => {})]);
}

/**
 * The push types that mean "this task just left the state this device was
 * tracking it in". Receiving one for a task is a teardown trigger, whether or
 * not the task screen is open. The completion pair is here because the
 * REQUESTER can complete a task from the web while the supporter's phone is
 * still tracking it. Chat, clock-in/out and the budget/time asks are not
 * terminal.
 */
export function isTerminalTaskNotification(type: unknown): type is NotificationType {
  return (
    type === "COMPLETED" ||
    type === "COMPLETED_SUPPORTER" ||
    type === "CANCELLED" ||
    type === "TASK_REMOVED" ||
    type === "TASK_REASSIGNED"
  );
}

/**
 * A push arrived while the app was open. Every push carries
 * data: { task_id, type }; when the type is terminal, tear down that task.
 * Anything else is ignored. Never throws.
 */
export async function teardownForNotification(data: unknown): Promise<void> {
  const payload = data as { task_id?: unknown; type?: unknown } | null | undefined;
  const taskId = payload?.task_id;
  if (typeof taskId !== "string" || taskId === "") return;
  if (!isTerminalTaskNotification(payload?.type)) return;
  await endTaskTracking(taskId, "notification");
}

/**
 * Foreground re-sync: at launch and on every return to the foreground, find
 * what this device is still doing for a task — a running location session, a
 * queued reminder — and ask the server whether that task is still one this
 * user should be tracking. If not, tear it down.
 *
 * "Still active" is the SAME predicate the task screen uses to decide whether
 * to track at all (live-tracking.ts broadcastPhase, mirroring the server's two
 * accept windows): assigned to me, status open, and either an open worklog or
 * the enroute window. That is what makes a completion, a cancellation, a
 * removal, a reassignment or a clock-out that happened on the web, on another
 * device or by an admin all land here as the same answer: "none".
 *
 * Reminders are stricter than the location session: they belong to a
 * clocked-in session and nothing else, so a task in its enroute window keeps
 * its location sharing and loses any reminder it somehow still has.
 *
 * A DEFINITE answer from the server (task gone, not ours, session invalid)
 * tears down. A network failure is not an answer: everything is left running
 * and the next foreground asks again. Deliberately NOT wired to Supabase auth
 * events, where a routine token refresh used to be able to end a live session.
 * Never throws.
 */
export async function resyncTaskTracking(): Promise<void> {
  try {
    const [gps, reminderTasks] = await Promise.all([activeBackgroundGpsTask(), taskIdsWithPendingReminders()]);
    const taskIds = new Set<string>(reminderTasks);
    if (gps) taskIds.add(gps.taskId);
    if (taskIds.size === 0) return;

    for (const taskId of taskIds) {
      await resyncOne(taskId, gps?.taskId === taskId ? gps.phase : null, reminderTasks.includes(taskId));
    }
  } catch (e) {
    log("resync skipped", e instanceof Error ? e.message : e);
  }
}

async function resyncOne(taskId: string, gpsPhase: GpsPhase | null, hasReminders: boolean): Promise<void> {
  let phase: ReturnType<typeof broadcastPhase>;
  let hasOpenWorklog: boolean;
  try {
    const [me, task, summary] = await Promise.all([getMe(), getTask(taskId), getWorklogs(taskId)]);
    const worklogs = summary?.worklogs ?? [];
    hasOpenWorklog = worklogs.some((wl) => wl.end_at === null);
    phase = broadcastPhase({
      isAssignee: me.auth && task.assigned_to_id === me.id,
      status: task.status,
      hasOpenWorklog,
      sessionCount: worklogs.length,
      enrouteAt: task.enroute_at,
    });
  } catch (e) {
    if (e instanceof ApiError) {
      // The server answered, and the answer is that this task can no longer
      // be read as ours: removed, cancelled and detached, or a dead session.
      log("resync got", e.status, "for", taskId, "— tearing down");
      await endTaskTracking(taskId, "resync");
      return;
    }
    log("resync skipped for", taskId, e instanceof Error ? e.message : e);
    return;
  }

  if (phase === "none") {
    await endTaskTracking(taskId, "resync");
    return;
  }
  if (hasReminders && !hasOpenWorklog) {
    // Enroute window still open, but a reminder only ever belongs to an open
    // session. Whatever left it behind, it is stale.
    await cancelOvertimeReminders(taskId);
  }
  if (gpsPhase !== null && gpsPhase !== phase) {
    // The session is running under the other window's phase — its pings
    // carry a `source` the server will refuse now. Stop it; the task screen
    // starts it again under the right phase the moment it renders, and its
    // health check covers the gap.
    log("phase", gpsPhase, "→", phase, "for", taskId, "— stopping stale session");
    await stopBackgroundGpsFor(taskId);
  }
}
