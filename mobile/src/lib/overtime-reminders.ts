import * as Notifications from "expo-notifications";
import * as SecureStore from "expo-secure-store";
import { OVERTIME_REMINDER, computeFirstReminderDelayMinutes } from "./task-utils";

// Local-only "forgot to clock out" nudges — no server involvement (see
// skills/decisions for the deferred server-side watchdog). Notification IDs
// are persisted per task so a clock-out can cancel them even if the app was
// closed and reopened since the reminders were scheduled.
Notifications.setNotificationHandler({
  handleNotification: async () => ({
    shouldShowBanner: true,
    shouldShowList: true,
    shouldPlaySound: false,
    shouldSetBadge: false,
  }),
});

function storageKey(taskId: string): string {
  return `hora_overtime_reminders_${taskId}`;
}

export async function scheduleOvertimeReminders(
  taskId: string,
  taskTitle: string,
  estimatedMinutes: number
): Promise<void> {
  let perm = await Notifications.getPermissionsAsync();
  if (!perm.granted) {
    perm = await Notifications.requestPermissionsAsync();
  }
  if (!perm.granted) return;

  const firstDelayMinutes = computeFirstReminderDelayMinutes(estimatedMinutes);
  const delaysMinutes = [firstDelayMinutes];
  for (let i = 1; i <= OVERTIME_REMINDER.repeatCount; i++) {
    delaysMinutes.push(firstDelayMinutes + OVERTIME_REMINDER.repeatIntervalMinutes * i);
  }

  const ids = await Promise.all(
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

  await SecureStore.setItemAsync(storageKey(taskId), JSON.stringify(ids)).catch(() => {});
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
