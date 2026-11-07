// Item questions (server/item.go). Mirrors app/src/lib/paymentCopy.test.mjs.
import test from "node:test";
import assert from "node:assert/strict";
import { extensionReaskLabel, extensionResolutionDetail, extensionResolutionTitle } from "../src/lib/payment-copy.ts";

test("item questions resolve in item words", () => {
  const base = { kind: "item", item_name: "oat milk", alternative_name: "almond milk" };
  assert.equal(extensionResolutionTitle({ ...base, status: "approved" }), "Alternative chosen");
  assert.equal(extensionResolutionTitle({ ...base, status: "denied" }), "Skip it");
  assert.equal(extensionResolutionTitle({ ...base, status: "expired" }), "No answer");
  assert.equal(extensionResolutionTitle({ ...base, status: "pending" }), null);
  assert.equal(
    extensionResolutionDetail({ ...base, status: "approved", fallback_instruction: "Buy almond milk instead of oat milk." }),
    "Buy almond milk instead of oat milk.",
  );
  assert.equal(extensionReaskLabel({ ...base, status: "denied" }), "Problem with another item");
});

test("budget and time keep their words", () => {
  assert.equal(extensionResolutionTitle({ kind: "budget", status: "approved" }), null);
  assert.equal(extensionResolutionTitle({ kind: "budget", status: "expired" }), "No response");
  assert.equal(extensionReaskLabel({ kind: "time" }), "Ask for more time again");
});
