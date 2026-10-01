// The copy-only price schedule, pinned to the server's. Reads
// server/billing.go so a change to BillingConfig fails here until
// src/lib/pricing.ts matches.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { PRICING, serviceCents, supporterPayoutCents, supporterPayPitch } from "../src/lib/pricing.ts";

const billing = readFileSync(new URL("../../server/billing.go", import.meta.url), "utf8");
const config = billing.slice(billing.indexOf("var Billing = BillingConfig{"));

function serverValue(field) {
  const m = config.match(new RegExp(`\\b${field}:\\s*(\\d+)`));
  assert.ok(m, `BillingConfig.${field} not found in server/billing.go`);
  return Number(m[1]);
}

test("mirrors BillingConfig field for field", () => {
  assert.equal(PRICING.baseFeeDefaultCents, serverValue("BaseFeeDefaultCents"));
  assert.equal(PRICING.baseFeeCompanionshipCents, serverValue("BaseFeeCompanionshipCents"));
  assert.equal(PRICING.perMinuteRateCents, serverValue("PerMinuteRateCents"));
  assert.equal(PRICING.includedMinutes, serverValue("IncludedMinutes"));
  assert.equal(PRICING.platformFeeBps, serverValue("PlatformFeeBps"));
});

test("an hour's work, priced and paid the way the server does it", () => {
  // $12 base + 45 billable minutes at $0.50, less the 20% fee.
  assert.equal(serviceCents(PRICING.baseFeeDefaultCents, 60), 3450);
  assert.equal(supporterPayoutCents(3450), 2760);
  // Inside the included minutes, only the base fee.
  assert.equal(serviceCents(PRICING.baseFeeDefaultCents, 10), 1200);
});

test("the Home pitch states the current schedule and no retired tier", () => {
  assert.equal(
    supporterPayPitch(),
    "Every task pays a base fee that covers the first 15 minutes, plus $0.50 per minute of " +
      "actual work after that — and you keep **80%** of it all. A one-hour grocery run pays " +
      "**$27.60**. Errands and deliveries start at a $12 base, companionship at $25.",
  );
});
