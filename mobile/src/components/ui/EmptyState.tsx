import type { LucideIcon } from "lucide-react-native";
import { View, Text } from "react-native";
import { Button } from "./Button";
import { color, size } from "../../theme/tokens";

export interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  caption: string;
  actionLabel?: string;
  onAction?: () => void;
  className?: string;
}

// One icon (24, muted), one title line, one caption line, one optional button
// (DESIGN.md §5). Invitation, not apology — omit the button where there's no
// real action yet rather than inventing one.
export function EmptyState({
  icon: Icon,
  title,
  caption,
  actionLabel,
  onAction,
  className,
}: EmptyStateProps) {
  return (
    <View className={`items-center gap-3 py-12 ${className ?? ""}`}>
      <Icon color={color.muted} size={24} strokeWidth={size.iconStroke} />
      <Text className="text-title font-semibold text-ink">{title}</Text>
      <Text className="text-center text-caption text-muted">{caption}</Text>
      {actionLabel && onAction ? (
        <Button label={actionLabel} variant="secondary" onPress={onAction} className="mt-2" />
      ) : null}
    </View>
  );
}
