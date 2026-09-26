// The OTP resend cooldown the login screen counts down from. Mirrors
// app/src/lib/otpResend.test.mjs: the two helpers must agree to the second.
import test from "node:test";
import assert from "node:assert/strict";
import { RESEND_COOLDOWN_MS, resendSecondsLeft, resendLabel } from "../src/lib/otp-resend.ts";

test("cooldown is thirty seconds", () => {
  assert.equal(RESEND_COOLDOWN_MS, 30_000);
});

test("counts down in whole seconds and stops at zero", () => {
  const readyAt = 100_000;
  assert.equal(resendSecondsLeft(readyAt, 70_000), 30);
  assert.equal(resendSecondsLeft(readyAt, 99_001), 1);
  assert.equal(resendSecondsLeft(readyAt, 100_000), 0);
  assert.equal(resendSecondsLeft(0, 200_000), 0);
});

test("label says how long, then says the action", () => {
  assert.equal(resendLabel(100_000, 88_500), "Resend in 12s");
  assert.equal(resendLabel(100_000, 100_000), "Resend code");
});
