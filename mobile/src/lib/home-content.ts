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

// Educational / news cards on Home. Copy and images are placeholders finalized
// later — this is the single source of truth for the cards. `action` is an
// optional route the whole card links to; omit it for a non-tappable card.
export interface HomeCard {
  slug: string;
  title: string;
  caption: string;
  action?: Href;
}

export const HOME_CARDS: HomeCard[] = [
  {
    slug: "post-in-seconds",
    title: "Post in seconds",
    caption: "Describe your task in plain words — AI fills in the rest",
  },
  {
    slug: "track-real-time",
    title: "Track in real time",
    caption: "See your Supporter clock in, work, and complete",
  },
  {
    slug: "pay-for-time",
    title: "Pay only for time used",
    caption: "Per-minute billing based on actual work time",
  },
];

// Static require map — Metro resolves these at bundle time, so a card whose
// slug is missing here simply renders the brandTint placeholder (see NewsCard).
// Drop a matching PNG in assets/illustrations/ and add the require to wire art.
export const HOME_CARD_IMAGES: Record<string, ImageSourcePropType> = {
  "post-in-seconds": require("../../assets/illustrations/post-in-seconds.png"),
  "track-real-time": require("../../assets/illustrations/track-real-time.png"),
  "pay-for-time": require("../../assets/illustrations/pay-for-time.png"),
};
