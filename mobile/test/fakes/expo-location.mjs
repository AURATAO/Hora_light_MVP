// expo-location, reduced to what gps-tracking.ts calls. One session, the
// same "already started" throw the native module makes, and a permission read
// the tests can hold open to stage a start that overlaps a stop.
export const PermissionStatus = { GRANTED: "granted", DENIED: "denied" };
export const Accuracy = { Balanced: 3 };
export const ActivityType = { OtherNavigation: 3 };

export const __state = {
  started: false,
  startCalls: 0,
  stopCalls: 0,
  options: null,
  /** Replace to hold the background permission read open. */
  backgroundPermission: async () => ({ status: PermissionStatus.GRANTED }),
};

export function __reset() {
  __state.started = false;
  __state.startCalls = 0;
  __state.stopCalls = 0;
  __state.options = null;
  __state.backgroundPermission = async () => ({ status: PermissionStatus.GRANTED });
}

export async function requestForegroundPermissionsAsync() {
  return { status: PermissionStatus.GRANTED };
}
export async function requestBackgroundPermissionsAsync() {
  return __state.backgroundPermission();
}
export async function getBackgroundPermissionsAsync() {
  return __state.backgroundPermission();
}
export async function getForegroundPermissionsAsync() {
  return { status: PermissionStatus.GRANTED };
}
export async function hasStartedLocationUpdatesAsync() {
  return __state.started;
}
export async function startLocationUpdatesAsync(_name, options) {
  if (__state.started) throw new Error("Location updates already started");
  __state.started = true;
  __state.startCalls += 1;
  __state.options = options;
}
export async function stopLocationUpdatesAsync() {
  __state.started = false;
  __state.stopCalls += 1;
}
