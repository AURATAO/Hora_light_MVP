// Shared color tokens for HO:RA mobile (S-30). Consumed by
// mobile/tailwind.config.js (NativeWind) via Node `require()`.
// mobile/src/theme/tokens.ts duplicates these same values by hand for
// runtime (non-className) use — Metro can't import across this package
// boundary the way Tailwind's Node-side require can (decisions/D-03).
// Canonical spec: mobile/DESIGN.md §1. Keep both files in sync.
module.exports = {
  ink: "#222831",
  brand: "#3A5A2D",
  "brand-tint": "#EAF2DF",
  gold: "#E1B145",
  "gold-tint": "#FBF3DD",
  "gold-text": "#7A5D14",
  muted: "#8A8F87",
  line: "#E5E7EB",
  inactive: "#B4B7B2",
  page: "#F6F7F4",
  surface: "#FFFFFF",
  danger: "#C24E3A",
  transparent: "transparent",
  white: "#FFFFFF",
};
