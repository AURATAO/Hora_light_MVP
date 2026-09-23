import type { LucideIcon } from "lucide-react-native";
import {
  CalendarClock,
  ClipboardList,
  Clock,
  HeartHandshake,
  Hourglass,
  Shirt,
  ShoppingCart,
  Sparkles,
  Sun,
  Truck,
  Users,
  Zap,
} from "lucide-react-native";
import type { TaskCategory } from "./types";

export interface CategoryMeta {
  value: TaskCategory;
  label: string;
  icon: LucideIcon;
}

// Single source of truth for how a TaskCategory renders (DESIGN.md §5 icon family).
export const CATEGORIES: readonly CategoryMeta[] = [
  { value: "task", label: "Task", icon: ClipboardList },
  { value: "companion", label: "Companion", icon: Users },
  { value: "quick_errand", label: "Quick Errand", icon: Zap },
  { value: "standard", label: "Standard", icon: Clock },
  { value: "half_day", label: "Half Day", icon: Sun },
  { value: "full_day", label: "Full Day", icon: CalendarClock },
  { value: "delivery", label: "Delivery", icon: Truck },
  { value: "grocery", label: "Grocery", icon: ShoppingCart },
  { value: "laundry", label: "Laundry", icon: Shirt },
  { value: "queue", label: "Queue", icon: Hourglass },
  { value: "anything_else", label: "Anything Else", icon: Sparkles },
  // Label only — the submitted category value stays "companionship" (Post Task
  // normalizes it to "companion"). "Companion" keeps the shortcut label to one
  // line and matches what the picker already calls the same thing.
  { value: "companionship", label: "Companion", icon: HeartHandshake },
];

/**
 * THE ORDER, in one place. Home's "For you" strip and the post form's category
 * picker both render exactly these, in exactly this order.
 *
 * They used to be two lists in two files — Home had one order, the picker
 * another, and the picker also carried "Anything else" while Home did not —
 * so the same person saw the categories in one order on the way in and
 * another once they got there (build 11). Any surface that lists categories
 * reads this; a test in app/src/lib/categoryOrder.test.mjs pins web's copy to
 * it byte for byte, so the two clients cannot drift again either.
 *
 * "companionship" is the display value; post-task normalizes it to the
 * submitted "companion" (see post-task.tsx). Companionship is LIVE as of build
 * 13 (beta-notice.ts DISABLED_CATEGORIES is empty); while it was locked it
 * stayed listed here, dimmed — an option that vanishes is a question, an
 * option that says "Coming soon" is an answer.
 */
export const POST_CATEGORY_ORDER: readonly TaskCategory[] = [
  "quick_errand",
  "delivery",
  "laundry",
  "grocery",
  "queue",
  "companionship",
];

const CATEGORY_BY_VALUE: Record<TaskCategory, CategoryMeta> = Object.fromEntries(
  CATEGORIES.map((c) => [c.value, c])
) as Record<TaskCategory, CategoryMeta>;

export function getCategoryMeta(category: TaskCategory): CategoryMeta {
  return CATEGORY_BY_VALUE[category];
}
