import { View, Text } from "react-native";

export type BadgeVariant = "success" | "gold" | "neutral";

export interface BadgeProps {
  label: string;
  variant?: BadgeVariant;
}

const VARIANT_STYLES: Record<BadgeVariant, { container: string; text: string }> = {
  success: { container: "bg-brand-tint", text: "text-brand" },
  gold: { container: "bg-gold-tint", text: "text-gold-text" },
  // For terminal states that are neither achievements nor faults —
  // "Cancelled", "Removed". They used to render in brand-tint green beside
  // "Completed", which read as a success; `danger` would be worse still,
  // because DESIGN.md §1 reserves it for destructive actions and errors and a
  // cancelled task is neither. `line` on `muted` is the system's way of
  // saying "this is over, and that is all" (§1, §6).
  neutral: { container: "bg-line", text: "text-muted" },
};

// Gold variant is for tips / Human Project only — gold stays the rarest color (DESIGN.md §1).
export function Badge({ label, variant = "success" }: BadgeProps) {
  const styles = VARIANT_STYLES[variant];

  return (
    <View className={`rounded-pill px-[10px] py-[5px] ${styles.container}`}>
      <Text className={`text-micro ${styles.text}`}>{label}</Text>
    </View>
  );
}
