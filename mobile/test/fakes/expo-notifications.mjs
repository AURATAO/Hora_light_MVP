// expo-notifications' scheduler as an in-memory queue. `__queue` IS the OS
// queue the tests assert on: a reminder that would fire on the device is a
// request still in this map.
export const SchedulableTriggerInputTypes = { TIME_INTERVAL: "timeInterval" };

export const __queue = new Map();
export const __state = {
  /** Replace to hold the permission read open. */
  permissions: async () => ({ granted: true, status: "granted" }),
};
let counter = 0;

export function __reset() {
  __queue.clear();
  counter = 0;
  __state.permissions = async () => ({ granted: true, status: "granted" });
}

export async function getPermissionsAsync() {
  return __state.permissions();
}
export async function requestPermissionsAsync() {
  return __state.permissions();
}
export async function scheduleNotificationAsync({ content, trigger }) {
  const identifier = `notif-${++counter}`;
  __queue.set(identifier, { identifier, content, trigger });
  return identifier;
}
export async function cancelScheduledNotificationAsync(identifier) {
  __queue.delete(identifier);
}
export async function cancelAllScheduledNotificationsAsync() {
  __queue.clear();
}
export async function getAllScheduledNotificationsAsync() {
  return [...__queue.values()];
}
export function setNotificationHandler() {}
export function addNotificationReceivedListener() {
  return { remove() {} };
}
