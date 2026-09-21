import { useState, type ReactNode } from "react";
import { LayoutAnimation, Platform, Text, UIManager, View } from "react-native";
import { ChevronDown, ChevronRight } from "lucide-react-native";
import { PressableScale } from "./PressableScale";
import { cn } from "../../theme/cn";
import { color, size } from "../../theme/tokens";

// LayoutAnimation is opt-in on old-architecture Android and a no-op on the new
// one. Enabling it is safe either way and keeps the expand from snapping.
if (Platform.OS === "android" && UIManager.setLayoutAnimationEnabledExperimental) {
  UIManager.setLayoutAnimationEnabledExperimental(true);
}

export interface DisclosureProps {
  /** The always-visible label. "Your request", "On hold". */
  title: string;
  /**
   * The one line of content that survives collapse, on the right of the header
   * row — "$42.00 reserved · Visa ••4242". This is the point of the component:
   * a section is collapsed because its detail is not what the reader needs
   * right now, but the ONE number they might is still worth a glance.
   */
  summary?: string | null;
  /** Open on first render. Collapsed is the default. */
  defaultOpen?: boolean;
  children: ReactNode;
  className?: string;
}

/**
 * A collapsible card section.
 *
 * WHY THIS EXISTS. Once a task is assigned, the requester's screen led with
 * what they had WRITTEN — description, addresses, timing — and buried what was
 * HAPPENING three cards down. Their own words are the one thing on that screen
 * they already know; the supporter, the live position and the money are what
 * they opened it for. Collapsing is how a section stays reachable without
 * taking the top of the screen.
 *
 * Chevron right when closed, down when open — the affordance every list row on
 * this platform already uses. The header is one 44pt tap target (DESIGN.md §3)
 * and carries the summary, so a collapsed section is never a dead end.
 */
export function Disclosure({
  title,
  summary,
  defaultOpen = false,
  children,
  className,
}: DisclosureProps) {
  const [open, setOpen] = useState(defaultOpen);
  const Chevron = open ? ChevronDown : ChevronRight;

  return (
    <View className={cn("rounded-card border border-line bg-surface", className)}>
      <PressableScale
        onPress={() => {
          LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
          setOpen((v) => !v);
        }}
        accessibilityRole="button"
        accessibilityState={{ expanded: open }}
        accessibilityLabel={summary ? `${title}, ${summary}` : title}
        className="min-h-11 flex-row items-center gap-2 p-4"
      >
        <Text className="text-caption font-semibold text-muted">{title}</Text>
        {summary ? (
          <Text className="flex-1 text-right text-caption text-ink" numberOfLines={1}>
            {summary}
          </Text>
        ) : (
          <View className="flex-1" />
        )}
        <Chevron color={color.muted} size={18} strokeWidth={size.iconStroke} />
      </PressableScale>
      {open ? <View className="gap-3 px-4 pb-4">{children}</View> : null}
    </View>
  );
}
