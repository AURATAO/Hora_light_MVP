import Constants from "expo-constants";
import * as Notifications from "expo-notifications";
import * as SecureStore from "expo-secure-store";
import { Alert, Platform } from "react-native";
import { registerPushToken, unregisterPushToken } from "./api";

// The app's SINGLE notification handler. It governs how a notification is
// presented while the app is in the FOREGROUND — for both remote push (task
// events) and the local overtime clock-out reminders. Set at module load and
// imported by the root layout so it's registered at app start, before any
// notification can arrive. Background / locked-screen presentation (sound,
// banner) is driven by the push payload's own fields, not this handler.
//
// This is the only setNotificationHandler in the app; overtime-reminders.ts
// relies on this one rather than setting its own.
Notifications.setNotificationHandler({
  handleNotification: async () => ({
    shouldShowBanner: true,
    shouldShowList: true,
    shouldPlaySound: false,
    shouldSetBadge: false,
  }),
});

// Cache the token we registered so logout can unregister the exact same value
// even if notification permission was revoked in between.
const PUSH_TOKEN_KEY = "hora_expo_push_token";

function easProjectId(): string | undefined {
  return Constants.expoConfig?.extra?.eas?.projectId;
}

// Request permission (if needed), fetch this device's Expo push token, and
// register it with the Go backend. Idempotent (the server upserts) and entirely
// best-effort: permission denial, a simulator with no push support, or an
// offline register call all resolve to null without throwing, so this can never
// break the login / app-start path it's called from. Returns the token on
// success, else null.
export async function registerForPushNotifications(): Promise<string | null> {
  return registerPush({ request: true });
}

// The app-start variant: registers the token ONLY when permission was already
// granted, and never shows the system prompt. The prompt itself is asked in
// context (askForPushInContext) — after a first post, after a first accept —
// with a line about what will be sent, not at launch with no explanation.
export async function registerPushIfAlreadyGranted(): Promise<string | null> {
  return registerPush({ request: false });
}

// Where the in-context ask has already been made on this device, so a person
// who said "Not now" is not asked on every post. iOS itself only ever shows
// the system prompt once; this guards OUR pre-prompt.
const PUSH_ASKED_KEY = "hora_push_asked";

export type PushAskContext = "task_posted" | "task_accepted";

/** The one-line explanation shown before the system prompt. */
export function pushAskCopy(context: PushAskContext): { title: string; body: string } {
  if (context === "task_accepted") {
    return {
      title: "Turn on notifications?",
      body: "We'll let you know when the requester messages you, approves a request, or cancels — and remind you to clock out.",
    };
  }
  return {
    title: "Turn on notifications?",
    body: "We'll let you know when a supporter accepts your task, when they're arriving, and when something needs your approval.",
  };
}

// Ask for push permission at a moment it makes sense, with a reason. Shows
// our own explanatory alert first; the system prompt follows only on "Turn
// on". Asked at most once per device by this path; a later change of mind
// goes through iOS Settings. Never throws.
export async function askForPushInContext(context: PushAskContext): Promise<void> {
  try {
    const perm = await Notifications.getPermissionsAsync();
    if (perm.granted) {
      await registerPush({ request: false });
      return;
    }
    if (!perm.canAskAgain) return;
    const asked = await SecureStore.getItemAsync(PUSH_ASKED_KEY).catch(() => null);
    if (asked) return;
    await SecureStore.setItemAsync(PUSH_ASKED_KEY, "1").catch(() => {});

    const { title, body } = pushAskCopy(context);
    const turnOn = await new Promise<boolean>((resolve) => {
      Alert.alert(title, body, [
        { text: "Not now", style: "cancel", onPress: () => resolve(false) },
        { text: "Turn on", onPress: () => resolve(true) },
      ]);
    });
    if (turnOn) await registerPush({ request: true });
  } catch (e) {
    if (__DEV__) console.warn("[push] in-context ask failed:", e);
  }
}

async function registerPush({ request }: { request: boolean }): Promise<string | null> {
  try {
    let perm = await Notifications.getPermissionsAsync();
    if (!perm.granted && request) {
      perm = await Notifications.requestPermissionsAsync();
    }
    if (!perm.granted) return null;

    const projectId = easProjectId();
    if (!projectId) {
      if (__DEV__) console.warn("[push] no EAS projectId in expoConfig.extra — cannot mint token");
      return null;
    }

    // Throws on a simulator / when the device has no push transport — caught below.
    const { data: token } = await Notifications.getExpoPushTokenAsync({ projectId });
    if (!token) return null;

    await registerPushToken(token, Platform.OS);
    await SecureStore.setItemAsync(PUSH_TOKEN_KEY, token).catch(() => {});
    if (__DEV__) console.log("[push] registered", token);
    return token;
  } catch (e) {
    if (__DEV__) console.warn("[push] registration failed:", e);
    return null;
  }
}

// Drop this device's token on logout so a signed-out phone stops receiving the
// previous user's task push. Best-effort — logout must never block on it.
export async function unregisterCurrentPushToken(): Promise<void> {
  try {
    const token = await SecureStore.getItemAsync(PUSH_TOKEN_KEY).catch(() => null);
    if (!token) return;
    await unregisterPushToken(token).catch(() => {});
    await SecureStore.deleteItemAsync(PUSH_TOKEN_KEY).catch(() => {});
  } catch {
    // Swallow — a failed unregister leaves a stale token the backend will
    // reap on the next DeviceNotRegistered receipt anyway.
  }
}

// Pull a task id out of a tapped notification's data payload. The backend sends
// data: { task_id, type }; returns null when it's missing or not a string.
export function taskIdFromNotificationResponse(
  response: Notifications.NotificationResponse | null | undefined
): string | null {
  const data = response?.notification?.request?.content?.data as { task_id?: unknown } | undefined;
  const id = data?.task_id;
  return typeof id === "string" && id.length > 0 ? id : null;
}

// Where a tapped notification should land. Every push carries the same
// data: { task_id, type }, so the type alone decides the destination — chat
// pushes (sent by the TalkJS webhook, server/talkjs_webhook.go) open the
// conversation, and every task event opens the task itself. Returns null when
// there is no task id to navigate to.
export function routeFromNotificationResponse(
  response: Notifications.NotificationResponse | null | undefined
): string | null {
  const taskId = taskIdFromNotificationResponse(response);
  if (!taskId) return null;

  const data = response?.notification?.request?.content?.data as { type?: unknown } | undefined;
  if (data?.type === "NEW_MESSAGE") return `/task/${taskId}/chat`;
  return `/task/${taskId}`;
}
