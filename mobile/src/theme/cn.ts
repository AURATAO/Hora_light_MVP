import { extendTailwindMerge } from "tailwind-merge";

/**
 * Class-string merger for every component that composes its own defaults with a
 * caller's `className`.
 *
 * Plain interpolation — `` `bg-ink ${className}` `` — does not let a caller
 * override anything. Both classes survive into the compiled stylesheet, and
 * which one wins is decided by their order *there*, not by their order in the
 * string. That is why `<Button className="bg-danger">` rendered ink: `bg-ink`
 * simply sorted later. `cn` drops the losing class instead of shipping both, so
 * the last one written wins, which is what every call site already assumed.
 *
 * The custom groups below are not optional. tailwind.config.js replaces
 * Tailwind's scales wholesale, and tailwind-merge only knows the stock ones:
 *
 * - `text-body` / `text-caption` are font sizes here, but they match no stock
 *   size, so tailwind-merge would file them under text-*color* — the same group
 *   as `text-ink`. `text-caption text-muted`, which this codebase writes
 *   everywhere, would then lose its size and render at the default. Registering
 *   the five literals puts them back in the font-size group (an exact match is
 *   checked before any validator, so these win over the colour fallback).
 * - `rounded-card` / `rounded-pill` are likewise unknown, and unknown classes
 *   are never merged — two of them would both survive and reintroduce the
 *   original bug on radius.
 *
 * Colours need no entry: an unrecognised `text-`/`bg-`/`border-` value already
 * falls through to the colour group, which is correct for all of them.
 * Keep this in step with tailwind.config.js.
 */
export const cn = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [{ text: ["display", "title", "body", "caption", "micro"] }],
      rounded: [{ rounded: ["card", "pill"] }],
    },
  },
});
