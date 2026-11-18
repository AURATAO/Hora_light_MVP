// "I can't do this task": the server's copy or no button at all, and a
// payload that never assumes an answer.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { buildCantDoPayload, cantDoFromConfig } from "../src/lib/cant-do.ts";

const server = JSON.parse(readFileSync(new URL("../../server/cant_do.json", import.meta.url), "utf8"));

test("the server's file is usable as shipped", () => {
  const copy = cantDoFromConfig(server);
  assert.ok(copy, "server/cant_do.json does not produce a button");
  assert.equal(copy.button, server.button);
  assert.equal(copy.reasons.length, server.reasons.length);
  assert.equal(copy.doneBody, server.done_body);
});

test("nothing usable from the server means no button", () => {
  for (const raw of [undefined, null, "", {}, { ...server, reasons: [] }, { ...server, submit: " " }, { ...server, button: undefined }]) {
    assert.equal(cantDoFromConfig(raw), null);
  }
});

test("reasons without a code or a label are dropped, and a repeated code is shown once", () => {
  const copy = cantDoFromConfig({
    ...server,
    reasons: [{ code: "a", label: "A" }, { code: "", label: "x" }, { code: "b" }, null, { code: "a", label: "again" }],
  });
  assert.deepEqual(copy.reasons, [{ code: "a", label: "A" }]);
});

test("the optional lines can be missing", () => {
  const { button_caption, intro, note_placeholder, ...rest } = server;
  const copy = cantDoFromConfig(rest);
  assert.ok(copy);
  assert.equal(copy.buttonCaption, "");
});

test("both questions must be answered; 'bought anything' is never assumed", () => {
  assert.ok("error" in buildCantDoPayload(null, false, ""));
  assert.ok("error" in buildCantDoPayload("unsafe", null, ""));
  assert.deepEqual(buildCantDoPayload("unsafe", false, "  "), { payload: { reason: "unsafe", bought_items: false } });
  assert.deepEqual(buildCantDoPayload("other", true, " key left at desk "), {
    payload: { reason: "other", bought_items: true, note: "key left at desk" },
  });
});
