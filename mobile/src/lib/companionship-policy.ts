import type { TaskCategory } from "./types";

// Verbatim copy of web's COMP_POLICY_TEXT (app/src/pages/NewTask.jsx). Kept
// character-for-character identical so the two clients never show users
// different versions of the same policy — edit both files together or not at
// all. The mobile sheet renders this by parsing the structure below rather
// than re-typing the copy.
export const COMPANIONSHIP_POLICY_TEXT = `Companionship (Accompaniment) Policy

What it IS:
• Public-route accompaniment only (e.g., walking together in public areas, escorting someone to a nearby appointment, accompanying someone to a subway/bus stop).
• Non-medical, non-caregiving, and strictly for general presence/support in public.

What it is NOT:
• No medical or personal care (no medication handling, bathing, lifting, or physical assistance).
• No services involving minors.
• No intimate/sexual services, no dating positioning.
• No overnight stays.
• No staying inside a private residence.

Safety & boundaries:
• Keep the route in public places; either party can end the task anytime if uncomfortable.
• If a request involves restricted activities, it must be declined and reported to the platform.
`;

export type PolicyLine =
  | { kind: "heading"; text: string }
  | { kind: "bullet"; text: string };

// Both category values reach Post Task: "companion" is what web submits and
// what CATEGORY_PICKS uses, "companionship" is the label-flavored value that
// Home's shortcut row and the AI parser can still produce (see post-task.tsx
// parseCategory). The gate must fire for either.
const COMPANION_CATEGORIES: readonly TaskCategory[] = ["companion", "companionship"];

export function isCompanionCategory(category: TaskCategory | undefined): boolean {
  return !!category && COMPANION_CATEGORIES.includes(category);
}

// Structure the verbatim text for mobile rendering: the first line is the
// sheet title (rendered by the sheet itself), "…:" lines are section headings,
// "• " lines are bullets. Derived from the string above so the copy has exactly
// one source.
export function parseCompanionshipPolicy(text = COMPANIONSHIP_POLICY_TEXT): PolicyLine[] {
  return text
    .split("\n")
    .slice(1) // first line is the title
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) =>
      line.startsWith("•")
        ? { kind: "bullet" as const, text: line.replace(/^•\s*/, "") }
        : { kind: "heading" as const, text: line }
    );
}
