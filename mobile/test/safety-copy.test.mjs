// Report / Block (App Store Guideline 1.2). The reasons are the server's closed
// set (server/safety.go); the mobile fallback only covers a failed fetch, and
// every value in it must be one the server accepts, or a report sent from the
// fallback would be refused with "invalid_reason".
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import {
  FALLBACK_REPORT_REASONS,
  CHAT_BLOCKED_LINE,
  blockConfirmTitle,
  safetyCounterpart,
  safetyTargetName,
} from "../src/lib/safety.ts";

const serverSafety = readFileSync(
  fileURLToPath(new URL("../../server/safety.go", import.meta.url)),
  "utf8"
);
const serverValues = [...serverSafety.matchAll(/\{Value: "([a-z_]+)", Label: "([^"]+)"\}/g)].map((m) => ({
  value: m[1],
  label: m[2],
}));

test("the server's closed set was found", () => {
  assert.ok(serverValues.length >= 5, `parsed ${serverValues.length} server reasons`);
});

test("every fallback reason is a server reason, with the same label", () => {
  for (const r of FALLBACK_REPORT_REASONS) {
    const s = serverValues.find((x) => x.value === r.value);
    assert.ok(s, `fallback reason ${r.value} is not in server/safety.go`);
    assert.equal(r.label, s.label, `label drift for ${r.value}`);
  }
});

test("the fallback always offers a catch-all", () => {
  assert.ok(FALLBACK_REPORT_REASONS.some((r) => r.value === "other"));
});

test("names degrade to a neutral phrase, never an email or blank", () => {
  assert.equal(safetyTargetName("  Maria "), "Maria");
  assert.equal(safetyTargetName(""), "this person");
  assert.equal(safetyTargetName(null), "this person");
  assert.equal(blockConfirmTitle("Maria"), "Block Maria?");
});

test("the read-only line says why and what to do", () => {
  assert.match(CHAT_BLOCKED_LINE, /read-only/);
  assert.match(CHAT_BLOCKED_LINE, /blocked/);
  assert.doesNotMatch(CHAT_BLOCKED_LINE, /!/);
});

// Who the "⋯" menu targets. The one non-obvious case is the App Review
// sandbox's self-accepted task, where both seats are the viewer: the menu must
// still render (a reviewer opens it to find Report), so the counterpart is the
// viewer and the server does the refusing.
test("the safety menu targets the other seat, and the viewer's own seat when both are theirs", () => {
  const base = { requesterId: "r", assignedToId: "s", requesterName: " Rita ", supporterName: "Otto" };
  assert.deepEqual(safetyCounterpart({ ...base, meId: "r" }), { id: "s", name: "Otto" });
  assert.deepEqual(safetyCounterpart({ ...base, meId: "s" }), { id: "r", name: "Rita" });
  assert.equal(safetyCounterpart({ ...base, meId: "x" }), null, "a non-party gets no menu");
  assert.equal(safetyCounterpart({ ...base, meId: "r", assignedToId: null }), null, "no supporter yet");
  assert.equal(safetyCounterpart({ ...base, meId: null }), null, "signed out");
  assert.deepEqual(
    safetyCounterpart({ meId: "me", requesterId: "me", assignedToId: "me", requesterName: "Hora Review", supporterName: "" }),
    { id: "me", name: null },
    "self-accepted: the counterpart is the viewer, with no name pretending otherwise"
  );
});
