// The supporter rules: the server's list when it sends one, the built-in
// list otherwise.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { BUILT_IN_RULES, rulesFromConfig } from "../src/lib/supporter-rules.ts";

test("the server's rules are shown as sent", () => {
  const rules = rulesFromConfig({ version: "x", items: [{ icon: "ban", title: " A ", body: " b " }] });
  assert.deepEqual(rules, [{ icon: "ban", title: "A", body: "b" }]);
});

test("nothing usable from the server means the built-in list", () => {
  for (const raw of [undefined, null, "", {}, { items: [] }, { items: [{ title: "only a title" }, null] }]) {
    assert.equal(rulesFromConfig(raw), BUILT_IN_RULES);
  }
});

test("the built-in list is the server's file, word for word", () => {
  const server = JSON.parse(readFileSync(new URL("../../server/supporter_rules.json", import.meta.url), "utf8"));
  assert.deepEqual(BUILT_IN_RULES, server.items);
});
