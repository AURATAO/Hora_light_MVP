// The sheets, as VoiceOver meets them.
//
// A React Native Pressable is `accessible` by default, which makes it ONE
// element to a screen reader: everything inside it collapses into a single
// focus stop that reads as a button. EditProfileSheet and CancelTaskSheet
// wrapped their whole sheet in one — backdrop Pressable around content
// Pressable — so the heading, every field, every reason pill and both buttons
// were a single "button" under VoiceOver; nothing inside could be reached or
// edited. NamePromptSheet and BetaNoticeSheet use the other shape: the backdrop
// is a self-closing Pressable SIBLING of the sheet, and the sheet is a View.
//
// This pins that shape for every bottom sheet in components/. It is a static
// read of the JSX rather than a rendered tree — there is no React test
// renderer in this project — and the property it checks is structural: no
// Pressable element may CONTAIN a sheet's controls. A Pressable that opens and
// closes on the same line contains nothing.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const read = (rel) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");

// Every component that renders a <Modal> bottom sheet with its own backdrop.
const SHEETS = ["EditProfileSheet", "CancelTaskSheet", "NamePromptSheet", "BetaNoticeSheet"];

/** Source with JSX comments and string literals stripped, so prose about the
 *  old shape cannot trip the check. */
function markup(src) {
  return src.replace(/\{\/\*[\s\S]*?\*\/\}/g, "").replace(/\/\/[^\n]*/g, "");
}

/**
 * True when a `<Pressable` at `start` has a matching `</Pressable>` — i.e. it
 * wraps children — rather than closing on itself with `/>`.
 */
function pressableWrapsChildren(src, start) {
  // Find the end of the opening tag, respecting `{...}` expressions.
  let depth = 0;
  for (let i = start; i < src.length; i++) {
    const ch = src[i];
    if (ch === "{") depth++;
    else if (ch === "}") depth--;
    else if (ch === ">" && depth === 0) {
      return src[i - 1] !== "/";
    }
  }
  return false;
}

for (const name of SHEETS) {
  test(`${name}: the backdrop is a sibling of the sheet, never its parent`, () => {
    const src = markup(read(`../src/components/${name}.tsx`));
    let idx = src.indexOf("<Pressable");
    assert.ok(idx >= 0, `${name} has no backdrop Pressable at all`);
    while (idx >= 0) {
      assert.equal(
        pressableWrapsChildren(src, idx),
        false,
        `${name} wraps content in a Pressable — VoiceOver collapses everything inside it into one element`
      );
      idx = src.indexOf("<Pressable", idx + 1);
    }
    // And the backdrop announces itself as the one control it is.
    assert.match(src, /<Pressable[\s\S]*?accessibilityLabel=/, `${name}'s backdrop has no accessibility label`);
  });
}

test("the rebuilt sheets keep their headings and controls individually reachable", () => {
  for (const name of ["EditProfileSheet", "CancelTaskSheet"]) {
    const src = markup(read(`../src/components/${name}.tsx`));
    // The sheet body is a plain View directly inside the flex-end column.
    assert.match(src, /<View className="flex-1 justify-end">[\s\S]*?<Pressable[\s\S]*?\/>[\s\S]*?<View className="rounded-t-card bg-surface/,
      `${name}: expected backdrop Pressable then a View sheet inside the flex-end column`);
    // Its heading is announced as one.
    assert.match(src, /accessibilityRole="header"/, `${name}: the sheet heading has no header role`);
    // And it still has the controls a person needs: at least one Input or
    // Pill and a Button.
    assert.match(src, /<(Input|Pill)\b/, `${name} lost its inputs`);
    assert.match(src, /<Button\b/, `${name} lost its buttons`);
  }
});
