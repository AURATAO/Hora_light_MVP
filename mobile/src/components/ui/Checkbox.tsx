import { View, Pressable } from "react-native";
import { Check } from "lucide-react-native";
import { color, size } from "../../theme/tokens";

export interface CheckboxProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  accessibilityLabel: string;
  disabled?: boolean;
}

const BOX = 22;

// The visible box is 22pt but the pressable is a full 44pt tap target
// (DESIGN.md §4), so the box is centered inside it rather than padded.
export function Checkbox({ checked, onChange, accessibilityLabel, disabled }: CheckboxProps) {
  return (
    <Pressable
      onPress={() => onChange(!checked)}
      disabled={disabled}
      accessibilityRole="checkbox"
      accessibilityState={{ checked, disabled: !!disabled }}
      accessibilityLabel={accessibilityLabel}
      hitSlop={4}
      style={{
        width: size.tapTarget,
        height: size.tapTarget,
        alignItems: "center",
        justifyContent: "center",
        opacity: disabled ? 0.5 : 1,
      }}
    >
      <View
        className={`items-center justify-center rounded-sm border ${
          checked ? "border-brand bg-brand" : "border-line bg-surface"
        }`}
        style={{ width: BOX, height: BOX }}
      >
        {checked && <Check size={16} color={color.white} strokeWidth={size.iconStroke + 0.7} />}
      </View>
    </Pressable>
  );
}
