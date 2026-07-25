import * as Notifications from "expo-notifications";
import * as SecureStore from "expo-secure-store";
import { OVERTIME_REMINDER, computeFirstReminderDelayMinutes } from "./task-utils";

// Local-only "forgot to clock out" nudges — no server involvement (see
// skills/decisions for the deferred server-side watchdog). Notification IDs
// are persisted per task so a clock-out can cancel them even if the app was
// closed and reopened since the reminders were scheduled.
//
// The notification handler (foreground presentation) lives in ./push — the
// app's single setNotificationHandler, registered at app start by the root
// layout. These local reminders present through that same handler.

function storageKey(taskId: string): string {
  return `hora_overtime_reminders_${taskId}`;
}

// DEV ONLY — flip to true to verify end-to-end *delivery* without waiting for
// the real 45-min-plus cadence. When on (and __DEV__), the reminder delays are
// compressed to seconds: the four notifications fire at ~10s, 20s, 30s, 40s
// after clock-in. Clock in, background the app, and you should see the banner.
// MUST be false for any real build — leaving it on would spam supporters.
const DEV_FAST_REMINDERS: boolean = false;

export async function scheduleOvertimeReminders(
  taskId: string,
  taskTitle: string,
  estimatedMinutes: number
): Promise<void> {
  let perm = await Notifications.getPermissionsAsync();
  if (!perm.granted) {
    perm = await Notifications.requestPermissionsAsync();
  }
  if (__DEV__) {
    // Full permission object, not just `.granted`: on iOS `granted` can be
    // true under *provisional* authorization, which delivers notifications
    // silently to the list with no banner/sound — a plausible cause of
    // "nothing appeared" even when scheduling succeeds. `ios.status` tells
    // them apart (2 = authorized, 4 = provisional; 3 = denied).
    // eslint-disable-next-line no-console
    console.log("[overtime] permission after request:", {
      granted: perm.granted,
      status: perm.status,
      ios: perm.ios,
    });
  }
  if (!perm.granted) {
    if (__DEV__) {
      // eslint-disable-next-line no-console
      console.log("[overtime] NOT scheduling — permission not granted");
    }
    return;
  }

  const firstDelayMinutes = computeFirstReminderDelayMinutes(estimatedMinutes);
  let delaysMinutes = [firstDelayMinutes];
  for (let i = 1; i <= OVERTIME_REMINDER.repeatCount; i++) {
    delaysMinutes.push(firstDelayMinutes + OVERTIME_REMINDER.repeatIntervalMinutes * i);
  }

  if (__DEV__ && DEV_FAST_REMINDERS) {
    // 10s, 20s, 30s, 40s — expressed in minutes so the rest of the pipeline
    // (Math.round(delayMinutes * 60)) is untouched.
    delaysMinutes = delaysMinutes.map((_, i) => ((i + 1) * 10) / 60);
    // eslint-disable-next-line no-console
    console.log("[overtime] DEV_FAST_REMINDERS on — compressing delays to ~10/20/30/40s");
  }

  if (__DEV__) {
    const nowMs = Date.now();
    // eslint-disable-next-line no-console
    console.log(
      "[overtime] scheduling",
      delaysMinutes.length,
      "reminders for task",
      taskId,
      "(estimated",
      estimatedMinutes,
      "min) — fire times:",
      delaysMinutes.map((m) => {
        const seconds = Math.round(m * 60);
        return {
          delayMinutes: m,
          seconds,
          firesAt: new Date(nowMs + seconds * 1000).toLocaleString(),
        };
      })
    );
  }

  let ids: string[];
  try {
    ids = await Promise.all(
      delaysMinutes.map((delayMinutes) =>
        Notifications.scheduleNotificationAsync({
          content: {
            title: "Clock-out reminder",
            body: `Still working on "${taskTitle}"? Don't forget to clock out.`,
          },
          trigger: {
            type: Notifications.SchedulableTriggerInputTypes.TIME_INTERVAL,
            seconds: Math.round(delayMinutes * 60),
            repeats: false,
          },
        })
      )
    );
  } catch (e) {
    // Surface in dev — the caller does `.catch(() => {})`, so a throw here
    // (e.g. a rejected trigger shape on device) would otherwise vanish and
    // look exactly like "scheduled fine but never fired".
    if (__DEV__) {
      // eslint-disable-next-line no-console
      console.error("[overtime] scheduleNotificationAsync FAILED:", e);
    }
    throw e;
  }

  await SecureStore.setItemAsync(storageKey(taskId), JSON.stringify(ids)).catch(() => {});

  if (__DEV__) {
    // eslint-disable-next-line no-console
    console.log("[overtime] scheduled ids:", ids);
    // Read the OS's own queue back — this is the source of truth for whether
    // the notifications actually landed on the device. If this list is empty
    // or missing our ids, the schedule call didn't stick regardless of what
    // it returned.
    const all = await Notifications.getAllScheduledNotificationsAsync().catch((e) => {
      // eslint-disable-next-line no-console
      console.error("[overtime] getAllScheduledNotificationsAsync FAILED:", e);
      return [];
    });
    // eslint-disable-next-line no-console
    console.log(
      "[overtime] OS queue now holds",
      all.length,
      "scheduled notification(s):",
      all.map((n) => ({ id: n.identifier, trigger: n.trigger }))
    );
  }
}

export async function cancelOvertimeReminders(taskId: string): Promise<void> {
  const key = storageKey(taskId);
  const raw = await SecureStore.getItemAsync(key).catch(() => null);
  if (raw) {
    try {
      const ids: string[] = JSON.parse(raw);
      await Promise.all(ids.map((id) => Notifications.cancelScheduledNotificationAsync(id).catch(() => {})));
    } catch {
      // Malformed storage — nothing to cancel from it.
    }
  }
  await SecureStore.deleteItemAsync(key).catch(() => {});
}

// Overtime reminders are the only local notifications this app schedules, so
// on logout it's simpler and more reliable to clear everything scheduled
// than to enumerate every task that might have an open reminder.
export async function cancelAllOvertimeReminders(): Promise<void> {
  await Notifications.cancelAllScheduledNotificationsAsync().catch(() => {});
}
