import { ActivityIndicator, Text } from "react-native";
import { PressableScale } from "./PressableScale";
import { color } from "../../theme/tokens";

export type ButtonVariant = "primary" | "secondary" | "text";

export interface ButtonProps {
  label: string;
  onPress?: () => void;
  variant?: ButtonVariant;
  disabled?: boolean;
  loading?: boolean;
  className?: string;
}

const INDICATOR_COLOR: Record<ButtonVariant, string> = {
  primary: color.white,
  secondary: color.ink,
  text: color.brand,
};

// DESIGN.md §5: one solid (primary) CTA per screen. Never inline a new variant.
//
// The container/text classes below are written as inline literal ternaries
// (not looked up from an object keyed by `variant`) because NativeWind's
// static extraction reads the `className` JSX attribute's own expression —
// an object-property indirection like `styles.container` sourced from a
// Record defined elsewhere in the file is invisible to it, so e.g. "bg-ink"
// silently never made it into the compiled stylesheet (visible as white
// button text on no background at all). Inline ternaries put every literal
// class name directly in the attribute NativeWind scans, matching the
// pattern already used successfully in Pill.tsx.
export function Button({
  label,
  onPress,
  variant = "primary",
  disabled = false,
  loading = false,
  className,
}: ButtonProps) {
  const indicatorColor = disabled ? color.muted : INDICATOR_COLOR[variant];

  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled || loading}
      className={`h-[52px] flex-row items-center justify-center rounded-pill px-6 ${
        disabled
          ? "bg-line"
          : variant === "primary"
            ? "bg-ink"
            : variant === "secondary"
              ? "border border-line bg-transparent"
              : "bg-transparent"
      } ${className ?? ""}`}
    >
      {loading ? (
        <ActivityIndicator color={indicatorColor} />
      ) : (
        <Text
          className={`text-body font-semibold ${
            disabled
              ? "text-muted"
              : variant === "primary"
                ? "text-white"
                : variant === "secondary"
                  ? "text-ink"
                  : "text-brand"
          }`}
        >
          {label}
        </Text>
      )}
    </PressableScale>
  );
}
