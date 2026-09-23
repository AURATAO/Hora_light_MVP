// The build-12 teardown leak, as tests.
//
//   npm test   (node's built-in runner; the Expo modules are faked in ./fakes)
//
// What the device showed: a task completed through the multi-session path
// (clock in, pause, clock back in, clock out, complete) left the background
// location task running and the local "still working?" reminders firing,
// while the server had already closed the worklog and was refusing pings.
// Killing the app was the only thing that cleared it.
//
// The rule these tests hold: after ANY transition out of "this device is
// tracking this task", the OS holds nothing for that task — no location
// session, no queued reminder — no matter which path made the transition,
// and no matter what was still in flight when it did.
import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";

import * as Location from "./fakes/expo-location.mjs";
import * as Notifications from "./fakes/expo-notifications.mjs";
import * as SecureStore from "./fakes/expo-secure-store.mjs";
import * as AsyncStorage from "./fakes/async-storage.mjs";
import * as api from "./fakes/api.mjs";

import { ApiError } from "../src/lib/api-error.ts";
import { OVERTIME_REMINDER } from "../src/lib/task-utils.ts";
import {
  activeBackgroundGpsTask,
  restartBackgroundGps,
  startBackgroundGps,
} from "../src/lib/gps-tracking.ts";
import {
  cancelOvertimeReminders,
  scheduleOvertimeReminders,
  taskIdsWithPendingReminders,
} from "../src/lib/overtime-reminders.ts";
import {
  endAllTaskTracking,
  endTaskTracking,
  isTerminalTaskNotification,
  onTaskTeardown,
  resyncTaskTracking,
  teardownForNotification,
} from "../src/lib/task-teardown.ts";

const TASK = "11111111-1111-4111-8111-111111111111";
const OTHER = "22222222-2222-4222-8222-222222222222";
const ME = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const SOMEONE_ELSE = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const REMINDERS_PER_SESSION = 1 + OVERTIME_REMINDER.repeatCount;

function resetAll() {
  Location.__reset();
  Notifications.__reset();
  SecureStore.__reset();
  AsyncStorage.__reset();
  api.__reset();
}

function remindersFor(taskId) {
  return [...Notifications.__queue.values()].filter((r) => r.content.data?.task_id === taskId);
}

/** What the OS holds for a task. The assertion every test ends on. */
async function deviceHoldsNothingFor(taskId) {
  assert.equal(Location.__state.started, false, "background location task still running");
  assert.equal(await activeBackgroundGpsTask(), null, "a GPS slot still names a task");
  assert.deepEqual(remindersFor(taskId), [], "reminders still queued for the task");
  assert.equal(SecureStore.__store.has(`hora_overtime_reminders_${taskId}`), false, "reminder ids still stored");
}

/** Clock in, as the task screen does it: background GPS plus the reminders. */
async function clockIn(taskId = TASK) {
  assert.equal(await startBackgroundGps(taskId, "working"), true, "background GPS should start");
  await scheduleOvertimeReminders(taskId, "Laundry run", 60);
  assert.equal(Location.__state.started, true);
  assert.equal(remindersFor(taskId).length, REMINDERS_PER_SESSION);
}

// A promise the test resolves by hand, to hold a native call open.
function deferred() {
  let resolve;
  const promise = new Promise((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

// ── The two scenarios from the device ───────────────────────────────────────

test("complete from a resumed session: no location task, no pending reminders", async () => {
  resetAll();
  await clockIn();
  await endTaskTracking(TASK, "paused"); // clock out — the pause
  await deviceHoldsNothingFor(TASK);

  await clockIn(); // clock back in — the resumed session
  await endTaskTracking(TASK, "paused"); // clock out (the server requires it before completing)
  await endTaskTracking(TASK, "completed");

  await deviceHoldsNothingFor(TASK);
});

test("complete straight out of a running session is just as clean", async () => {
  resetAll();
  await clockIn();
  await endTaskTracking(TASK, "paused");
  await clockIn();
  // Whatever the server allows in future, completion alone must be enough.
  await endTaskTracking(TASK, "completed");
  await deviceHoldsNothingFor(TASK);
});

test("cancel during a pause: no location task, no pending reminders", async () => {
  resetAll();
  await clockIn();
  await endTaskTracking(TASK, "paused");
  await endTaskTracking(TASK, "cancelled");
  await deviceHoldsNothingFor(TASK);
});

test("cancel while clocked in — the same", async () => {
  resetAll();
  await clockIn();
  await endTaskTracking(TASK, "cancelled");
  await deviceHoldsNothingFor(TASK);
});

test("removal and reassignment are the same teardown", async () => {
  for (const reason of ["removed", "reassigned", "inactive", "notification"]) {
    resetAll();
    await clockIn();
    await endTaskTracking(TASK, reason);
    await deviceHoldsNothingFor(TASK);
  }
});

// ── The races that made the leak ────────────────────────────────────────────

test("a GPS restart requested before the stop cannot revive the session", async () => {
  // The task screen's health check calls restartBackgroundGps every 30s and
  // on every foreground. It awaits a permission read first. Hold that read
  // open, tear down, then let it through: the old code found "not running",
  // wrote a fresh slot and started updates again — for a task that had just
  // closed its worklog.
  resetAll();
  await clockIn();

  const gate = deferred();
  Location.__state.backgroundPermission = async () => {
    await gate.promise;
    return { status: Location.PermissionStatus.GRANTED };
  };
  const restart = restartBackgroundGps(TASK, "working");

  await endTaskTracking(TASK, "paused");
  assert.equal(Location.__state.started, false);

  gate.resolve();
  assert.equal(await restart, false, "the superseded restart must report that it did not start");
  await deviceHoldsNothingFor(TASK);
});

test("a reminder schedule in flight when the cancel lands leaves nothing queued", async () => {
  // Clock in, clock straight back out: the cancel sweeps an empty queue,
  // then the schedule's ids land in it. Same shape as the GPS race.
  resetAll();

  const gate = deferred();
  Notifications.__state.permissions = async () => {
    await gate.promise;
    return { granted: true, status: "granted" };
  };
  const schedule = scheduleOvertimeReminders(TASK, "Laundry run", 60);

  await endTaskTracking(TASK, "paused");
  gate.resolve();
  await schedule;

  await deviceHoldsNothingFor(TASK);
});

test("reminders the stored record has lost are still cancelled", async () => {
  // A pause made on the web leaves this device's reminders alone; the next
  // clock-in here schedules a second set and overwrites the stored ids. The
  // old cancel trusted the record and left the first set firing.
  resetAll();
  await scheduleOvertimeReminders(TASK, "Laundry run", 60);
  await scheduleOvertimeReminders(TASK, "Laundry run", 60);
  assert.equal(remindersFor(TASK).length, 2 * REMINDERS_PER_SESSION);
  assert.equal(JSON.parse(SecureStore.__store.get(`hora_overtime_reminders_${TASK}`)).length, REMINDERS_PER_SESSION);

  await endTaskTracking(TASK, "completed");
  await deviceHoldsNothingFor(TASK);
});

test("a corrupt reminder record does not protect the reminders", async () => {
  resetAll();
  await scheduleOvertimeReminders(TASK, "Laundry run", 60);
  SecureStore.__store.set(`hora_overtime_reminders_${TASK}`, "{not json");
  await cancelOvertimeReminders(TASK);
  await deviceHoldsNothingFor(TASK);
});

// ── Scope ───────────────────────────────────────────────────────────────────

test("tearing down task B leaves task A's session and reminders alone", async () => {
  resetAll();
  await clockIn(TASK);
  await endTaskTracking(OTHER, "completed");
  assert.equal(Location.__state.started, true);
  assert.deepEqual(await activeBackgroundGpsTask(), { taskId: TASK, phase: "working" });
  assert.equal(remindersFor(TASK).length, REMINDERS_PER_SESSION);
});

test("teardown is idempotent and never throws on a device with nothing running", async () => {
  resetAll();
  await endTaskTracking(TASK, "completed");
  await endTaskTracking(TASK, "completed");
  await deviceHoldsNothingFor(TASK);
});

test("listeners hear every teardown, with the task and the reason", async () => {
  resetAll();
  const heard = [];
  const off = onTaskTeardown((taskId, reason) => heard.push([taskId, reason]));
  await endTaskTracking(TASK, "cancelled");
  off();
  await endTaskTracking(TASK, "completed");
  assert.deepEqual(heard, [[TASK, "cancelled"]]);
});

test("logout clears everything, whoever owns it", async () => {
  resetAll();
  await clockIn(TASK);
  await scheduleOvertimeReminders(OTHER, "Other", 30);
  await endAllTaskTracking();
  await deviceHoldsNothingFor(TASK);
  await deviceHoldsNothingFor(OTHER);
});

// ── Pushes ──────────────────────────────────────────────────────────────────

test("a terminal push for the task tears it down; anything else is ignored", async () => {
  for (const type of ["COMPLETED", "COMPLETED_SUPPORTER", "CANCELLED", "TASK_REMOVED", "TASK_REASSIGNED"]) {
    resetAll();
    await clockIn();
    assert.equal(isTerminalTaskNotification(type), true);
    await teardownForNotification({ task_id: TASK, type });
    await deviceHoldsNothingFor(TASK);
  }
  for (const data of [
    { task_id: TASK, type: "NEW_MESSAGE" },
    { task_id: TASK, type: "CLOCK_OUT" },
    { task_id: TASK, type: "BUDGET_INCREASE_REQUESTED" },
    { type: "CANCELLED" },
    { task_id: OTHER, type: "CANCELLED" },
    null,
    "garbage",
  ]) {
    resetAll();
    await clockIn();
    await teardownForNotification(data);
    assert.equal(Location.__state.started, true, `push ${JSON.stringify(data)} must not tear down`);
    assert.equal(remindersFor(TASK).length, REMINDERS_PER_SESSION);
  }
});

// ── The foreground re-sync ──────────────────────────────────────────────────

const me = { auth: true, id: ME };
const openTask = (over = {}) => ({ id: TASK, status: "open", assigned_to_id: ME, enroute_at: null, ...over });
const closedWorklog = { id: "w1", start_at: "2026-09-23T10:00:00Z", end_at: "2026-09-23T10:20:00Z" };
const openWorklog = { id: "w2", start_at: "2026-09-23T10:30:00Z", end_at: null };

test("re-sync: a task the server says is completed is torn down", async () => {
  resetAll();
  await clockIn();
  api.__set({ me, task: openTask({ status: "completed" }), worklogs: { worklogs: [closedWorklog, closedWorklog] } });
  await resyncTaskTracking();
  await deviceHoldsNothingFor(TASK);
});

test("re-sync: a clock-out that happened elsewhere is torn down", async () => {
  resetAll();
  await clockIn();
  api.__set({ me, task: openTask(), worklogs: { worklogs: [closedWorklog] } });
  await resyncTaskTracking();
  await deviceHoldsNothingFor(TASK);
});

test("re-sync: reassigned to someone else is torn down", async () => {
  resetAll();
  await clockIn();
  api.__set({ me, task: openTask({ assigned_to_id: SOMEONE_ELSE }), worklogs: { worklogs: [openWorklog] } });
  await resyncTaskTracking();
  await deviceHoldsNothingFor(TASK);
});

test("re-sync: a removed task (403 task_removed) is torn down", async () => {
  resetAll();
  await clockIn();
  api.__set({
    me,
    task: () => {
      throw new ApiError(403, { error: "task_removed" });
    },
    worklogs: { worklogs: [] },
  });
  await resyncTaskTracking();
  await deviceHoldsNothingFor(TASK);
});

test("re-sync: a live clocked-in session is left running", async () => {
  resetAll();
  await clockIn();
  api.__set({ me, task: openTask(), worklogs: { worklogs: [closedWorklog, openWorklog] } });
  await resyncTaskTracking();
  assert.equal(Location.__state.started, true);
  assert.deepEqual(await activeBackgroundGpsTask(), { taskId: TASK, phase: "working" });
  assert.equal(remindersFor(TASK).length, REMINDERS_PER_SESSION);
});

test("re-sync: an enroute session with no worklog yet is left running", async () => {
  resetAll();
  assert.equal(await startBackgroundGps(TASK, "enroute"), true);
  api.__set({ me, task: openTask({ enroute_at: "2026-09-23T09:50:00Z" }), worklogs: { worklogs: [] } });
  await resyncTaskTracking();
  assert.equal(Location.__state.started, true);
  assert.deepEqual(await activeBackgroundGpsTask(), { taskId: TASK, phase: "enroute" });
});

test("re-sync: an enroute session whose window closed elsewhere is stopped", async () => {
  // The supporter clocked in on the web: the worklog exists, the enroute
  // window is over, and enroute-sourced pings would now be refused.
  resetAll();
  assert.equal(await startBackgroundGps(TASK, "enroute"), true);
  api.__set({ me, task: openTask({ enroute_at: "2026-09-23T09:50:00Z" }), worklogs: { worklogs: [openWorklog] } });
  await resyncTaskTracking();
  assert.equal(Location.__state.started, false);
  assert.equal(await activeBackgroundGpsTask(), null);
});

test("re-sync: reminders without a GPS session are checked too", async () => {
  // The leak the device showed had the location task gone by the time the
  // reminders were still firing — a re-sync that only looks at GPS misses it.
  resetAll();
  await scheduleOvertimeReminders(TASK, "Laundry run", 60);
  assert.deepEqual(await taskIdsWithPendingReminders(), [TASK]);
  api.__set({ me, task: openTask({ status: "completed" }), worklogs: { worklogs: [closedWorklog] } });
  await resyncTaskTracking();
  await deviceHoldsNothingFor(TASK);
});

test("re-sync: a network failure is not an answer — everything stays", async () => {
  resetAll();
  await clockIn();
  api.__set({
    me,
    task: () => {
      throw new TypeError("Network request failed");
    },
    worklogs: { worklogs: [] },
  });
  await resyncTaskTracking();
  assert.equal(Location.__state.started, true);
  assert.equal(remindersFor(TASK).length, REMINDERS_PER_SESSION);
});

test("re-sync: nothing running means no server round-trip", async () => {
  resetAll();
  await resyncTaskTracking();
  assert.deepEqual(api.__state.calls, []);
});

// ── The rule, held statically ───────────────────────────────────────────────
//
// No screen may reach past endTaskTracking to the pieces underneath it. That
// is how build 12 ended up with three partial teardowns and no whole one.

function screenFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const path = `${dir}/${name}`;
    if (statSync(path).isDirectory()) screenFiles(path, out);
    else if (/\.tsx?$/.test(name)) out.push(path);
  }
  return out;
}

test("screens end tracking only through endTaskTracking", () => {
  const appDir = fileURLToPath(new URL("../src/app", import.meta.url));
  const forbidden = /\b(stopBackgroundGps|stopBackgroundGpsFor|cancelOvertimeReminders|cancelAllOvertimeReminders|reconcileBackgroundGps)\b/;
  for (const file of screenFiles(appDir)) {
    const source = readFileSync(file, "utf8");
    const hit = source.match(forbidden);
    assert.equal(hit, null, `${file.slice(appDir.length)} calls ${hit?.[0]} directly; go through endTaskTracking`);
  }
});

test("every screen that ends a task also ends its tracking", () => {
  const appDir = fileURLToPath(new URL("../src/app", import.meta.url));
  for (const file of screenFiles(appDir)) {
    const source = readFileSync(file, "utf8");
    if (!/\b(completeTask|cancelTask|clockOut)\(/.test(source)) continue;
    assert.match(source, /\bendTaskTracking\(/, `${file.slice(appDir.length)} ends a task without endTaskTracking`);
  }
});

test("tearing down a task on an idle device does not supersede another task's pending start", async () => {
  // Task A's start is waiting on the permission prompt; the supporter opens
  // task B, which isn't theirs, and B's screen tears B down. Nothing is
  // running and nothing is claimed, so that must not count as a stop.
  resetAll();
  const gate = deferred();
  Location.__state.backgroundPermission = async () => {
    await gate.promise;
    return { status: Location.PermissionStatus.GRANTED };
  };
  const startA = startBackgroundGps(TASK, "working");
  await endTaskTracking(OTHER, "inactive");
  gate.resolve();
  assert.equal(await startA, true);
  assert.deepEqual(await activeBackgroundGpsTask(), { taskId: TASK, phase: "working" });
});
