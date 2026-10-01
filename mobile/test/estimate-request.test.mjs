// What re-quotes the Post Task estimate. The form keys its fetch on the
// serialized request, so "these two forms produce different requests" IS
// "the second one fetches a new quote".
import test from "node:test";
import assert from "node:assert/strict";
import { estimateRequest } from "../src/lib/estimate-request.ts";

const base = {
  category: "grocery",
  estimatedMinutes: "60",
  shoppingBudget: "",
  isImmediate: false,
  scheduledDate: new Date("2026-10-02T18:00:00-04:00"),
};
const key = (form, promo) => JSON.stringify(estimateRequest(form, promo));

test("moving only the scheduled time is a new quote", () => {
  // 6 PM is the standard rate, 10 PM the evening one. Nothing else changes.
  const evening = { ...base, scheduledDate: new Date("2026-10-02T22:00:00-04:00") };
  assert.notEqual(key(base), key(evening));
  assert.equal(estimateRequest(evening).scheduled_at, "2026-10-03T02:00:00.000Z");
});

test("switching between ASAP and scheduled is a new quote", () => {
  const asap = { ...base, isImmediate: true };
  assert.notEqual(key(base), key(asap));
  assert.equal(estimateRequest(asap).scheduled_at, "");
});

test("duration, budget, category and promo each re-quote", () => {
  assert.notEqual(key(base), key({ ...base, estimatedMinutes: "90" }));
  assert.notEqual(key(base), key({ ...base, shoppingBudget: "30" }));
  assert.notEqual(key(base), key({ ...base, category: "companionship" }));
  assert.notEqual(key(base), key(base, "WELCOME10"));
});

test("sub-minute noise in the picked time does not re-quote", () => {
  const noisy = { ...base, scheduledDate: new Date("2026-10-02T18:00:42.123-04:00") };
  assert.equal(key(base), key(noisy));
});

test("nothing to quote without a category or a positive duration", () => {
  assert.equal(estimateRequest({ ...base, category: undefined }), null);
  assert.equal(estimateRequest({ ...base, estimatedMinutes: "" }), null);
  assert.equal(estimateRequest({ ...base, estimatedMinutes: "0" }), null);
});
