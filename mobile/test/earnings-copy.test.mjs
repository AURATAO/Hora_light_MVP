// The platform fee, said out loud (D-14). Mirrors app/src/lib/earningsCopy.test.mjs.
import test from "node:test";
import assert from "node:assert/strict";
import {
  platformFeePercent,
  platformFeeExplainer,
  earnedBreakdownLine,
  transferBreakdownLine,
} from "../src/lib/earnings-copy.ts";

test("platformFeePercent never rounds a rate it did not charge", () => {
  assert.equal(platformFeePercent(2000), "20%");
  assert.equal(platformFeePercent(250), "2.5%");
  assert.equal(platformFeePercent(undefined), "0%");
});

test("the explainer reads the backend rate, and ships with 20% when there is none", () => {
  assert.equal(
    platformFeeExplainer(2000),
    "HO:RA takes 20% of service fees; purchase reimbursements are always paid back in full.",
  );
  assert.equal(platformFeeExplainer(), platformFeeExplainer(2000));
});

test("a settlement with a fee names the fee beside the service, and the reimbursement whole", () => {
  assert.equal(
    earnedBreakdownLine({
      time_cents: 1960, reimbursement_cents: 1240, total_cents: 3200,
      service_gross_cents: 2450, platform_fee_cents: 490, platform_fee_bps: 2000,
    }),
    "$19.60 service (after 20% platform fee) + $12.40 reimbursement",
  );
});

test("a row paid before the fee is never described as discounted", () => {
  assert.equal(
    transferBreakdownLine({
      task_id: "t", task_title: "x", amount_cents: 3940, time_cents: 2700, receipt_cents: 1240,
      status: "paid", created_at: "2026-09-01T00:00:00Z",
    }),
    "$27.00 service + $12.40 reimbursement",
  );
});
