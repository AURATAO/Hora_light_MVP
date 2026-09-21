import type { ReactNode } from "react";
import { Text, View } from "react-native";
import { ChevronRight } from "lucide-react-native";
import { PressableScale } from "./ui";
import { color, size } from "../theme/tokens";

/** How many rows a history preview shows before it defers to "See all". */
export const HISTORY_PREVIEW_COUNT = 3;

export interface HistorySectionProps {
  title: string;
  /** How many rows exist in total, server-counted. */
  total: number;
  /** Opens the full, paginated list. Omitted when there is nothing more. */
  onSeeAll?: () => void;
  children: ReactNode;
}

/**
 * A history section: the three most recent, and a way to the rest.
 *
 * WHY IT IS CAPPED. Posted history, working history and the earnings strip all
 * rendered EVERYTHING, unbounded — so a screen whose job is "what is happening
 * now" grew without limit in proportion to how much somebody had used the app,
 * and the active tasks at the top were pushed further away the more loyal the
 * user. Active work stays fully visible; what is finished is a reference, and a
 * reference belongs behind a link.
 *
 * "See all (47)" carries the NUMBER on purpose. "See all" alone makes a reader
 * guess whether there are four or four hundred, which is the thing they are
 * actually asking when they look at a truncated list.
 *
 * The link is absent, not disabled, when the history fits in the preview:
 * there is nothing on the other side of it, and a link that leads to the same
 * three rows is a broken promise.
 */
export function HistorySection({ title, total, onSeeAll, children }: HistorySectionProps) {
  const hasMore = total > HISTORY_PREVIEW_COUNT;

  return (
    <View className="mt-6">
      <View className="mb-3 flex-row items-center justify-between">
        <Text className="text-title font-semibold text-ink">{title}</Text>
        {hasMore && onSeeAll ? (
          <PressableScale
            onPress={onSeeAll}
            accessibilityRole="button"
            accessibilityLabel={`See all ${total} ${title.toLowerCase()}`}
            hitSlop={8}
            className="min-h-11 flex-row items-center gap-1"
          >
            {/* `brand` is the tertiary text action (DESIGN.md §1) — the same
                treatment "See all" gets everywhere else in the app. */}
            <Text className="text-caption text-brand">See all ({total})</Text>
            <ChevronRight color={color.brand} size={16} strokeWidth={size.iconStroke} />
          </PressableScale>
        ) : null}
      </View>
      <View className="gap-3">{children}</View>
    </View>
  );
}
