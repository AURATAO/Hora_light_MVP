import { Text } from "react-native";
import { PressableScale } from "./PressableScale";

export interface PillProps {
  label: string;
  selected?: boolean;
  onPress?: () => void;
  className?: string;
}

export function Pill({ label, selected = false, onPress, className }: PillProps) {
  return (
    <PressableScale
      onPress={onPress}
      className={`h-[36px] flex-row items-center justify-center rounded-pill px-4 ${
        selected ? "bg-brand-tint" : "border border-line"
      } ${className ?? ""}`}
    >
      <Text className={`text-caption ${selected ? "font-semibold text-brand" : "text-muted"}`}>
        {label}
      </Text>
    </PressableScale>
  );
}
