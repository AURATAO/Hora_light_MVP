// The availability card states the server's number, never its own.
import test from "node:test";
import assert from "node:assert/strict";
import { availabilityOffLine, availabilityOnLine, availabilityWindow } from "../src/lib/availability-copy.ts";

test("the window is worded from the server's minutes", () => {
  assert.equal(availabilityWindow(240), "4 hours");
  assert.equal(availabilityWindow(480), "8 hours");
  assert.equal(availabilityWindow(60), "1 hour");
  assert.equal(availabilityWindow(90), "90 minutes");
});

test("an older backend sends no number, and the card names none", () => {
  for (const odd of [undefined, null, 0, -5, "240", NaN]) {
    assert.equal(availabilityWindow(odd), "");
    assert.equal(availabilityOffLine(odd), "Get new tasks as notifications. Turns off by itself.");
  }
  assert.equal(availabilityOffLine(480), "Get new tasks as notifications. Turns off after 8 hours.");
});

test("on: until when", () => {
  assert.equal(availabilityOnLine("9:40 PM"), "Until 9:40 PM");
  assert.equal(availabilityOnLine(""), "On for now");
});
