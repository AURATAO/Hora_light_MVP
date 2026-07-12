import { Text } from "react-native";
import { PressableScale } from "./PressableScale";

export type ButtonVariant = "primary" | "secondary" | "text";

export interface ButtonProps {
  label: string;
  onPress?: () => void;
  variant?: ButtonVariant;
  disabled?: boolean;
  className?: string;
}

const VARIANT_STYLES: Record<ButtonVariant, { container: string; text: string }> = {
  primary: { container: "bg-ink", text: "text-white" },
  secondary: { container: "border border-line bg-transparent", text: "text-ink" },
  text: { container: "bg-transparent", text: "text-brand" },
};

// DESIGN.md §5: one solid (primary) CTA per screen. Never inline a new variant.
export function Button({
  label,
  onPress,
  variant = "primary",
  disabled = false,
  className,
}: ButtonProps) {
  const styles = disabled ? { container: "bg-line", text: "text-muted" } : VARIANT_STYLES[variant];

  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled}
      className={`h-[52px] flex-row items-center justify-center rounded-pill px-6 ${styles.container} ${className ?? ""}`}
    >
      <Text className={`text-body font-semibold ${styles.text}`}>{label}</Text>
    </PressableScale>
  );
}
