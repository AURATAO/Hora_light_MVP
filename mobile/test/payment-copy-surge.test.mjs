// The evening & overnight note. Mirrors app/src/lib/paymentCopy.test.mjs.
import test from "node:test";
import assert from "node:assert/strict";
import { surgeRateNote } from "../src/lib/payment-copy.ts";

test("names the window in the server's words, and falls back to the shipped one", () => {
  assert.equal(
    surgeRateNote({ surge_rate: true, per_minute_rate_cents: 100, included_minutes: 15, surge_window: "9 PM–9 AM" }),
    "Evening & overnight rate: $1.00/min after the first 15 minutes (9 PM–9 AM).",
  );
  assert.equal(
    surgeRateNote({ surge_rate: true, per_minute_rate_cents: 100, included_minutes: 15 }),
    "Evening & overnight rate: $1.00/min after the first 15 minutes (9 PM–9 AM).",
  );
  assert.equal(surgeRateNote({ surge_rate: false, per_minute_rate_cents: 50 }), null);
  assert.equal(surgeRateNote(null), null);
});
