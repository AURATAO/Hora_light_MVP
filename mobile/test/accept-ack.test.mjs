// What a supporter confirms before accepting (PLAN 13c). The words are the
// server's; this pins that whatever arrives is safe to render and that the
// accept sends back exactly what was shown.
import test from "node:test";
import assert from "node:assert/strict";
import {
  ACK_FALLBACK_BUTTON,
  ACK_FALLBACK_HEADING,
  ackParagraphs,
  acknowledgedPayload,
  normalizeAcceptRequirements,
} from "../src/lib/accept-ack.ts";

test("a server payload is shown as sent", () => {
  const reqs = normalizeAcceptRequirements({
    heading: "Before you accept",
    button: "I understand and accept",
    items: [
      { key: "baseline", version: "1", title: "Before your first task", body: "A.\n\nB." },
      { key: "laundry", version: "3", title: "Laundry", body: "Check every pocket." },
    ],
  });
  assert.equal(reqs.items.length, 2);
  assert.deepEqual(acknowledgedPayload(reqs.items), [
    { key: "baseline", version: "1" },
    { key: "laundry", version: "3" },
  ]);
});

test("an older backend, a failed read or an odd payload is nothing to confirm", () => {
  for (const raw of [undefined, null, "", 404, {}, { items: null }, { items: "x" }, { error: "not found" }]) {
    const reqs = normalizeAcceptRequirements(raw);
    assert.deepEqual(reqs.items, []);
    assert.equal(reqs.heading, ACK_FALLBACK_HEADING);
    assert.equal(reqs.button, ACK_FALLBACK_BUTTON);
  }
});

test("an item with no key, no version or no words is dropped, never shown blank", () => {
  const reqs = normalizeAcceptRequirements({
    items: [
      { key: "", version: "1", title: "t", body: "b" },
      { key: "a", version: "", title: "t", body: "b" },
      { key: "b", version: "1", title: " ", body: "" },
      { key: "c", version: "1", title: "", body: "Only a body." },
      null,
      "x",
    ],
  });
  assert.deepEqual(reqs.items.map((i) => i.key), ["c"]);
});

test("the body reads as paragraphs", () => {
  assert.deepEqual(ackParagraphs("One.\n\nTwo\nstill two.\n\n\n\nThree."), ["One.", "Two still two.", "Three."]);
  assert.deepEqual(ackParagraphs(""), []);
});
