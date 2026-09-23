// Module resolution for the tests, in two parts:
//
//   1. Every native module the lib code imports (expo-location, TaskManager,
//      notifications, SecureStore, AsyncStorage) and the network layer
//      (./api) resolve to a fake in ./fakes. The fakes are plain ESM with a
//      `__state` the tests read and a `__reset()` they call between cases.
//
//   2. The lib code imports its siblings without extensions ("./api-error"),
//      which Metro allows and Node's ESM loader does not. Extensionless
//      relative specifiers try `.ts` first.
//
// Node strips the types itself (v23.6+), so the modules under test are the
// real files in src/lib, unmodified.

const FAKES = new Map([
  ["expo-location", "./fakes/expo-location.mjs"],
  ["expo-task-manager", "./fakes/expo-task-manager.mjs"],
  ["expo-notifications", "./fakes/expo-notifications.mjs"],
  ["expo-secure-store", "./fakes/expo-secure-store.mjs"],
  ["@react-native-async-storage/async-storage", "./fakes/async-storage.mjs"],
  ["./api", "./fakes/api.mjs"],
]);

export async function resolve(specifier, context, next) {
  const fake = FAKES.get(specifier);
  if (fake) {
    return { url: new URL(fake, import.meta.url).href, shortCircuit: true };
  }
  const relative = specifier.startsWith("./") || specifier.startsWith("../");
  if (relative && !/\.[a-z]+$/i.test(specifier)) {
    try {
      return await next(`${specifier}.ts`, context);
    } catch {
      // Fall through to the normal resolution and its own error.
    }
  }
  return next(specifier, context);
}
