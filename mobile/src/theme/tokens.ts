// Single source of truth for HO:RA mobile design values (DESIGN.md §1-4).
// Never hardcode a hex value, spacing number, radius, or type size outside this file.
import rawColors from "../../../design-tokens/colors";

export const color = {
  ink: rawColors.ink,
  brand: rawColors.brand,
  brandTint: rawColors["brand-tint"],
  gold: rawColors.gold,
  goldTint: rawColors["gold-tint"],
  goldText: rawColors["gold-text"],
  muted: rawColors.muted,
  line: rawColors.line,
  inactive: rawColors.inactive,
  page: rawColors.page,
  surface: rawColors.surface,
  danger: rawColors.danger,
  transparent: rawColors.transparent,
  white: rawColors.white,
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

export const motion = {
  pressScale: 0.97,
  duration: {
    micro: 150,
    standard: 250,
    sheets: 350,
  },
} as const;
