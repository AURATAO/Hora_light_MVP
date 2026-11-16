// The platform fee, said out loud (D-14). Mirrors app/src/lib/earningsCopy.test.mjs.
import test from "node:test";
import assert from "node:assert/strict";
import {
  platformFeePercent,
  platformFeeExplainer,
  earnedBreakdownLine,
  transferBreakdownLine,
  earningsBreakdownLine,
} from "../src/lib/earnings-copy.ts";

test("platformFeePercent never rounds a rate it did not charge", () => {
  assert.equal(platformFeePercent(2000), "20%");
  assert.equal(platformFeePercent(250), "2.5%");
  assert.equal(platformFeePercent(undefined), "0%");
});

test("the explainer reads the backend rate, and ships with 30% when there is none", () => {
  assert.equal(
    platformFeeExplainer(2000),
    "HO:RA takes 20% of service fees; purchase reimbursements are always paid back in full.",
  );
  assert.equal(platformFeeExplainer(), platformFeeExplainer(3000));
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

test("a row says the rate it was cut at, and no number when it has none", () => {
  assert.equal(
    earningsBreakdownLine({ time: 1960, reimbursement: 0, feeCents: 490, feeBps: 2000 }),
    "$19.60 service (after 20% platform fee)",
  )
  assert.equal(
    earningsBreakdownLine({ time: 1960, reimbursement: 0, feeCents: 490 }),
    "$19.60 service (after platform fee)",
  )
})

// ── Money text worded by the server (PLAN 13c) ─────────────────────────────
// Shown as sent when present; this file's own copy when not. That is what
// lets a description change (a flat price, a bonus, a reimbursement paid
// early) without an app release.
{
  const { serverText, transferBreakdownLine, earnedBreakdownLine } = await import("../src/lib/earnings-copy.ts");

  test("server money text: a row shows the server's breakdown when it sends one", () => {
    const row = { time_cents: 1960, receipt_cents: 1240, platform_fee_cents: 840, platform_fee_bps: 3000 };
    const sent = "$19.60 service (after the 30% platform fee) + $12.40 reimbursement paid Nov 20";
    assert.equal(transferBreakdownLine({ ...row, breakdown_label: sent }), sent);
    assert.equal(earnedBreakdownLine({ time_cents: 1960, reimbursement_cents: 1240, total_cents: 3200, breakdown_label: ` ${sent} ` }), sent);
  });

  test("server money text: an older backend or an odd value falls back to the app's own line", () => {
    const row = { time_cents: 1960, receipt_cents: 1240, platform_fee_cents: 840, platform_fee_bps: 3000 };
    const own = transferBreakdownLine(row);
    assert.match(own, /\$19\.60 service/);
    for (const odd of [undefined, null, "", "   ", 42, {}]) {
      assert.equal(transferBreakdownLine({ ...row, breakdown_label: odd }), own);
      assert.equal(serverText(odd), "");
    }
  });
}
