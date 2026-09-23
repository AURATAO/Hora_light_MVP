// The network layer, replaced. Each test installs the answers it wants with
// __set; anything unconfigured throws, so a test that reaches the server
// unexpectedly fails loudly instead of passing on a default.
export const __state = {
  me: null,
  task: null,
  worklogs: null,
  calls: [],
};

export function __reset() {
  __state.me = null;
  __state.task = null;
  __state.worklogs = null;
  __state.calls = [];
}

/** Values, or functions (sync or async) — a function that throws simulates the server refusing. */
export function __set({ me, task, worklogs }) {
  if (me !== undefined) __state.me = me;
  if (task !== undefined) __state.task = task;
  if (worklogs !== undefined) __state.worklogs = worklogs;
}

async function answer(name, configured, ...args) {
  __state.calls.push(name);
  if (configured === null) throw new Error(`api.${name} not configured for this test`);
  return typeof configured === "function" ? configured(...args) : configured;
}

export function getMe() {
  return answer("getMe", __state.me);
}
export function getTask(id) {
  return answer("getTask", __state.task, id);
}
export function getWorklogs(id) {
  return answer("getWorklogs", __state.worklogs, id);
}
export async function sendGpsPing() {
  __state.calls.push("sendGpsPing");
  return {};
}
