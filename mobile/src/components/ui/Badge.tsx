import { View, Text } from "react-native";

export type BadgeVariant = "success" | "gold";

export interface BadgeProps {
  label: string;
  variant?: BadgeVariant;
}

const VARIANT_STYLES: Record<BadgeVariant, { container: string; text: string }> = {
  success: { container: "bg-brand-tint", text: "text-brand" },
  gold: { container: "bg-gold-tint", text: "text-gold-text" },
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
