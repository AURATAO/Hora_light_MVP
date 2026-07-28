import type { ImageSourcePropType } from "react-native";
import type { Href } from "expo-router";

// Hero card rotating placeholder lines, cycled ~6s (see home.tsx). Edit or add
// lines freely — this is the single source of truth. "{name}" is replaced with
// the user's first name; any line containing it is dropped when no name is known.
export const HERO_ROTATION: string[] = [
  "What do you need?",
  "Hi, {name}",
  "Groceries? A queue? Company?",
];

// Resolves HERO_ROTATION for the current user: drops "{name}" lines when we have
// no name, otherwise substitutes it in.
export function buildHeroLines(firstName: string | null): string[] {
  return HERO_ROTATION.filter((line) => firstName || !line.includes("{name}")).map((line) =>
    firstName ? line.replace("{name}", firstName) : line
  );
}

// Educational / news cards on Home — this file is the single source of truth
// for every word of them. A card taps through in exactly one of two ways:
// `action` routes somewhere, or a matching HOME_CARD_DETAILS entry opens the
// explainer sheet. A card with neither is simply not tappable.
export interface HomeCard {
  slug: string;
  title: string;
  caption: string;
  action?: Href;
}

// Post Task's describe step shows the AI tip banner only when it is opened
// with ?hint=ai — i.e. only from the "Post in seconds" card. The hero card and
// the category circles push without the param and never see it.
export const POST_TASK_AI_HINT = "ai";

export const POST_TASK_AI_HINT_COPY =
  "Just type what you need, like you'd text a friend — 'Pick up my laundry on Main St, " +
  "about 30 min.' AI turns it into a complete task, and you can edit everything before posting.";

export const HOME_CARDS: HomeCard[] = [
  {
    slug: "post-in-seconds",
    title: "Post in seconds",
    caption: "Describe your task in plain words — AI fills in the rest",
    action: { pathname: "/post-task", params: { hint: POST_TASK_AI_HINT } },
  },
  {
    slug: "track-real-time",
    title: "Track in real time",
    caption: "See your Supporter clock in, work, and complete",
  },
  {
    slug: "pay-for-time",
    title: "Earn on your schedule",
    caption: "Per-minute billing based on actual work time",
  },
];

// Explainer sheet behind a card, keyed by card slug (see HomeCardSheet). The
// sheet reuses the card's own illustration, so there is no image field here.
export interface HomeCardDetail {
  title: string;
  /** Paragraphs, rendered with a gap between them. "**…**" marks bold spans. */
  body: string[];
  /** Solid CTA. Dismisses the sheet, then routes when `primaryAction` is set. */
  primaryLabel: string;
  primaryAction?: Href;
  /** Text-style dismiss below the CTA. Omit when the CTA is itself the dismiss. */
  dismissLabel?: string;
}

export const HOME_CARD_DETAILS: Record<string, HomeCardDetail> = {
  "track-real-time": {
    title: "Track in real time",
    body: [
      "Every task on HO:RA is transparent from start to finish. Your Supporter clocks in when " +
        "they begin — you'll get a notification the moment it happens. While they work, you can " +
        "see their last known location and the time on the clock. When it's done, you get a " +
        "completion photo and the final cost, calculated from actual minutes worked. No " +
        "guessing, no surprises.",
    ],
    primaryLabel: "Got it",
  },
  "pay-for-time": {
    title: "Earn on your schedule",
    body: [
      "Every task pays a base fee plus $0.50 per minute of actual work — and you keep **80%** " +
        "of it all. A one-hour grocery run pays **$33.60**. Errands and deliveries start at a " +
        "$12 base, extended tasks at $18, companionship at $25.",
      "You choose which tasks to accept, clock in when you start, clock out when you're done. " +
        "Paid for real minutes, never estimates.",
      "Getting started: apply in a few minutes with your basic info. Applications are reviewed " +
        "within 1–3 business days — you'll be able to accept tasks as soon as you're verified. " +
        "US-based with a valid government ID required.",
    ],
    primaryLabel: "Apply now",
    // The Work tab owns the supporter application banner in all of its states,
    // so the CTA lands there rather than deep-linking /supporter-apply — an
    // already-applied user should see their status, not a second form.
    primaryAction: "/(tabs)/work",
    dismissLabel: "Maybe later",
  },
};

export type CopySpan = { text: string; strong: boolean };

// Splits "plain **bold** plain" into spans for nested <Text> rendering. Only
// the paired "**" marker is supported — the sheet copy above is the only
// consumer, and DESIGN.md §2 allows exactly one emphasis weight (600).
export function parseEmphasis(paragraph: string): CopySpan[] {
  return paragraph
    .split("**")
    .map((text, i) => ({ text, strong: i % 2 === 1 }))
    .filter((span) => span.text.length > 0);
}

// Static require map — Metro resolves these at bundle time, so a card whose
// slug is missing here simply renders the brandTint placeholder (see NewsCard).
// Drop a matching PNG in assets/illustrations/ and add the require to wire art.
export const HOME_CARD_IMAGES: Record<string, ImageSourcePropType> = {
  "post-in-seconds": require("../../assets/illustrations/post-in-seconds.png"),
  "track-real-time": require("../../assets/illustrations/track-real-time.png"),
  "pay-for-time": require("../../assets/illustrations/pay-for-time.png"),
};
