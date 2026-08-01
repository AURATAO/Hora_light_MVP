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

const CATEGORY_BY_VALUE: Record<TaskCategory, CategoryMeta> = Object.fromEntries(
  CATEGORIES.map((c) => [c.value, c])
) as Record<TaskCategory, CategoryMeta>;

export function getCategoryMeta(category: TaskCategory): CategoryMeta {
  return CATEGORY_BY_VALUE[category];
}
