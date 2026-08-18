// Single source of truth for HO:RA mobile design values (DESIGN.md §1-4).
// Never hardcode a hex value, spacing number, radius, or type size outside this file.
//
// `color` mirrors ../../../design-tokens/colors.js (S-30) by value, not by
// import: Metro refuses to resolve modules outside mobile/'s project root,
// so a runtime (non-className) consumer here can't cross that boundary the
// way tailwind.config.js's Node `require()` can (see decisions/D-03, which
// hit the same wall for a single color). Keep these two files in sync by hand.
export const color = {
  ink: "#222831",
  brand: "#3A5A2D",
  brandTint: "#EAF2DF",
  gold: "#E1B145",
  goldTint: "#FBF3DD",
  goldText: "#7A5D14",
  muted: "#8A8F87",
  line: "#E5E7EB",
  inactive: "#B4B7B2",
  page: "#F6F7F4",
  surface: "#FFFFFF",
  danger: "#C24E3A",
  transparent: "transparent",
  white: "#FFFFFF",
} as const;

export const space = {
  1: 4,
  2: 8,
  3: 12,
  4: 16,
  6: 24,
  8: 32,
  12: 48,
} as const;

export const radius = {
  sm: 8,
  card: 16,
  pill: 999,
} as const;

export const type = {
  display: { fontSize: 26, lineHeight: 32, fontWeight: "600" },
  title: { fontSize: 18, lineHeight: 24, fontWeight: "600" },
  body: { fontSize: 15, lineHeight: 22, fontWeight: "400" },
  bodyStrong: { fontSize: 15, lineHeight: 22, fontWeight: "600" },
  caption: { fontSize: 13, lineHeight: 18, fontWeight: "400" },
  micro: { fontSize: 11, lineHeight: 14, fontWeight: "600" },
} as const;

export const size = {
  buttonHeight: 52,
  pillHeight: 36,
  tapTarget: 44,
  iconStroke: 1.8,
} as const;

// Floating pill tab bar geometry (DESIGN.md §5 tab bar, restyled as a floating
// pill). All values sit on the 4pt grid. `bottomGap` is measured above the
// device safe-area inset; screens add `height + bottomGap + a grid step` of
// bottom padding so scroll content never hides behind the bar.
export const layout = {
  tabBarHeight: 64,
  tabBarInset: 24,
  tabBarBottomGap: 12,
} as const;

// Disabled affordances that stay visible rather than dropping out of the
// layout — a locked category circle, for one. Buttons and inputs do NOT use
// this: they swap to the `line`/`muted` pair instead (DESIGN.md §5).
export const opacity = {
  disabled: 0.4,
} as const;

export const motion = {
  pressScale: 0.97,
  duration: {
    micro: 150,
    standard: 250,
    sheets: 350,
  },
} as const;
