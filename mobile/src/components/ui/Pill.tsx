import { Text } from "react-native";
import { PressableScale } from "./PressableScale";
import { opacity } from "../../theme/tokens";
import { cn } from "../../theme/cn";

export interface PillProps {
  label: string;
  selected?: boolean;
  onPress?: () => void;
  className?: string;
  /**
   * Visible but locked: dimmed and inert, keeping its place in the row so the
   * option still reads as part of the set. Not the `line`/`muted` swap a
   * disabled Button uses — an unselected pill is already muted-on-hairline,
   * so there is nothing further to grey.
   */
  disabled?: boolean;
}

export function Pill({ label, selected = false, onPress, className, disabled = false }: PillProps) {
  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled}
      style={disabled ? { opacity: opacity.disabled } : undefined}
      className={cn(
        "h-[36px] flex-row items-center justify-center rounded-pill px-4",
        selected ? "bg-brand-tint" : "border border-line",
        className
      )}
    >
      <Text className={`text-caption ${selected ? "font-semibold text-brand" : "text-muted"}`}>
        {label}
      </Text>
    </PressableScale>
  );
}
