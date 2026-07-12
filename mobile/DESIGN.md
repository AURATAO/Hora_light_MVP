# HO:RA Mobile — Design Constitution

> Scope: everything under `mobile/`. This document is LAW for UI work.
> Read this before writing or modifying any screen or component.
> Companion files: `mobile/src/theme/tokens.ts` (single source of truth for values)
> and `mobile/tailwind.config.js` (NativeWind mapping).

## 0. Design read

HO:RA mobile is a **premium-consumer utility**: Uber-level restraint, softened by
HO:RA's human warmth. The aesthetic is earned through *consistency and hierarchy*,
not decoration. If a screen looks "designed", we've gone too far; it should look
inevitable.

One-line test for every screen: **could this pass as an Uber screen that happens
to use HO:RA's palette?** If not, remove things until it does.

## 1. Color — every color has ONE job

All colors come from `tokens.ts`. **Never write a hex value in a component.**

| Token | Hex | Job | Never |
|---|---|---|---|
| `ink` | `#222831` | All primary text. Primary (solid) buttons. Active tab icon. | Never pure `#000`. |
| `brand` | `#3A5A2D` | Brand voice: links, active/selected states, logo text, tertiary text actions ("See all"). | Never large fills. Never primary buttons. |
| `brandTint` | `#EAF2DF` | Background of selected pills, success chips, subtle highlights. Always paired with `brand` text. | Never text. Never borders. |
| `gold` | `#E1B145` | The colon in the logo. Icons/dots for tips, Human Project, achievements. | Never text on white. Never buttons. **Max once per screen.** |
| `goldTint` / `goldText` | `#FBF3DD` / `#7A5D14` | Gold badge background / gold badge text, as a pair. | Never separately. |
| `muted` | `#8A8F87` | Secondary text: metadata, timestamps, distances, captions. | Never for tappable text. |
| `line` | `#E5E7EB` | ALL borders, dividers, skeleton blocks, disabled fills. Hairline only (`StyleSheet.hairlineWidth` or 1px max). | Never darker borders "for emphasis" — use spacing or weight instead. |
| `page` | `#F6F7F4` | Screen background. | — |
| `surface` | `#FFFFFF` | Cards, sheets, inputs, tab bar. | — |
| `danger` | `#C24E3A` | Destructive actions and errors only. | Never warnings/info. |

Hard rules:
- **One solid CTA per screen** (`ink` fill). Everything else is outline, text, or pill.
- **`brand` appears where the brand speaks**; if green is everywhere, it means nothing.
- **`gold` is the rarest color in the system** — it keeps the same scarcity it has in the logo.
- Depth comes from `page` vs `surface` contrast, not shadows. **No shadows** except the tab bar (one soft top hairline is enough) and bottom sheets.

## 2. Typography — two weights, five sizes

Font: system default (SF Pro on iOS). Weights: **400 and 600 only.** Never 500, 700, or bold-on-bold.

| Token | Size / line | Weight | Use |
|---|---|---|---|
| `display` | 26 / 32 | 600 | One per screen max. The screen's question or headline. |
| `title` | 18 / 24 | 600 | Section headers, card titles, prices. |
| `body` | 15 / 22 | 400 | Default text, inputs, buttons (600 in buttons). |
| `caption` | 13 / 18 | 400 | Metadata, always in `muted`. |
| `micro` | 11 / 14 | 600 | Tab labels, badges only. |

Hierarchy is made by **weight + color contrast** (`ink` 600 vs `muted` 400), not by adding sizes. If a screen needs a sixth size, the layout is wrong.

## 3. Spacing & radius — locked values

- Spacing: **4pt grid only** — 4, 8, 12, 16, 24, 32, 48. Nothing else, ever.
- Screen horizontal padding: **24** everywhere. No exceptions per screen.
- Section gap: 24–32. Element gap within a group: 8–12. Card internal padding: 16.
- Radius: exactly three — `sm: 8` (inputs, small elements), `card: 16` (cards, sheets), `pill: 999` (buttons, pills, badges).
- Minimum tap target: 44×44.

## 4. Motion — cheap but mandatory

Motion is half of "app feel". Every interactive element responds.

- **Press:** every `Pressable` scales to `0.97` + opacity `0.9`, spring back on release. Use one shared `PressableScale` component — never re-implement.
- **Durations:** micro 150ms, standard 250ms, sheets 350ms. Easing: ease-out.
- **Loading:** skeleton blocks (`line` color, subtle pulse) matching real layout. **Never a centered spinner** except full-screen boot.
- **Lists:** new items fade+slide in 12px. No infinite/looping animation anywhere.
- Respect `prefers-reduced-motion` (Reanimated `ReducedMotion`).

## 5. Component recipes

Build these ONCE in `mobile/src/components/ui/`, then only compose. Never inline-style a variant of an existing component.

- **Button** — height 52, radius `pill`, text `body`/600.
  `primary`: `ink` bg, white text. `secondary`: transparent, hairline `line` border, `ink` text. `text`: no bg, `brand` text. Disabled: `line` bg, `muted` text — prefer keeping enabled and validating on press.
- **Pill (filter/category)** — height 36, radius `pill`, `caption` size.
  Selected: `brandTint` bg + `brand` 600 text. Unselected: hairline border + `muted` text.
- **Card** — `surface` bg, hairline `line` border, radius `card`, padding 16, no shadow. Row layout: title (`body`/600 `ink`) left, price right, metadata line below in `caption`/`muted`.
- **Badge** — radius `pill`, padding 5×10, `micro` text. Success: `brandTint`+`brand`. Gold: `goldTint`+`goldText` (tips / Human Project only).
- **Input** — height 52, radius `sm`, `surface` bg, hairline border; focus: border becomes `ink` (not brand, not glow). Label above in `caption`/`muted`. Error text below in `danger`.
- **Tab bar** — `surface` bg, top hairline. Active: `ink` icon + `micro` 600 label. Inactive: `line`-adjacent gray (`#B4B7B2`). Icons: outline style, one family (e.g. lucide-react-native), stroke 1.8 everywhere.
- **Empty state** — one icon (24, `muted`), one `title` line, one `caption` line, one button. Invitation, not apology ("Post your first task", not "Nothing here yet").
- **Screen template** — `page` bg, `display` headline, content on `surface` cards. Every screen composes from this.

## 6. Copy inside UI

- Sentence case everywhere. No exclamation marks in system copy. No emoji in UI chrome.
- Buttons: verb first, 1–3 words ("Post a task", "Accept task").
- Errors: what happened + what to do, one sentence.

## 7. Anti-slop list (never do)

- No gradients, glassmorphism, glows, or decorative shadows.
- No hex values, font sizes, spacing numbers, or radii outside `tokens.ts`.
- No two solid CTAs on one screen. No gold twice on one screen.
- No borders darker than `line`. No mixing icon families or stroke widths.
- No new component variant when composing existing ones would do.

## 8. Machine checks (run before every UI PR)

```bash
# 1. No raw hex outside the theme folder
grep -rn --include='*.tsx' --include='*.ts' -E '#[0-9a-fA-F]{3,8}\b' mobile/src --exclude-dir=theme && echo 'FAIL: raw hex' || echo 'OK'

# 2. No raw fontSize / fontWeight in screens or components (must come from tokens/variants)
grep -rn --include='*.tsx' -E 'fontSize:\s*[0-9]|fontWeight:\s*["'\'']?[0-9]' mobile/src/app mobile/src/components --exclude-dir=ui && echo 'FAIL: raw type values' || echo 'OK'

# 3. Forbidden weights anywhere
grep -rn --include='*.tsx' --include='*.ts' -E 'font-(bold|medium)|fontWeight:\s*["'\'']?(500|700|800|900)' mobile/src && echo 'FAIL: forbidden weight' || echo 'OK'
```

These belong in `skills/` verify flow as UI standards. New UI code must pass all three (ratchet policy: applies to `mobile/` in full).
