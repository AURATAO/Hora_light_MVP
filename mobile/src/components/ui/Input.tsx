import { useState } from "react";
import { View, Text, TextInput, type TextInputProps } from "react-native";
import { color } from "../../theme/tokens";

export interface InputProps extends TextInputProps {
  label?: string;
  error?: string;
  /**
   * For single-line *data* that regularly needs more than one line of *room* —
   * task titles, street addresses. The box wraps and grows with the text (up to
   * three lines, then it scrolls) instead of running past its own border, while
   * the value stays one line: Return submits rather than inserting a newline,
   * and newlines arriving by paste collapse to a space.
   */
  grow?: boolean;
}

export function Input({
  label,
  error,
  className,
  grow,
  multiline,
  onChangeText,
  onFocus,
  onBlur,
  ...props
}: InputProps) {
  const [focused, setFocused] = useState(false);
  const wraps = grow || multiline;

  return (
    <View>
      {label ? <Text className="mb-1 text-caption text-muted">{label}</Text> : null}
      <TextInput
        multiline={wraps}
        // DESIGN.md §5's 52 is the floor, not a clamp. As a fixed `height` it
        // clipped every wrapping field to one line's worth of frame and drew the
        // rest outside the border; as a min-height a one-line field still lands
        // on exactly 52 and a wrapping one grows. Callers bound that growth with
        // `max-h-*`, or pin a fixed box with `h-*` (which wins over the floor).
        className={`min-h-[52px] rounded-sm border bg-surface px-4 text-body text-ink ${
          wraps ? "py-3" : ""
        } ${grow ? "max-h-[90px]" : ""} ${focused ? "border-ink" : "border-line"} ${className ?? ""}`}
        placeholderTextColor={color.muted}
        // Android-only; wrapped text should start at the top of the box rather
        // than stay vertically centered as the box grows.
        textAlignVertical={wraps ? "top" : undefined}
        submitBehavior={grow ? "blurAndSubmit" : undefined}
        returnKeyType={grow ? "done" : undefined}
        onChangeText={
          grow && onChangeText
            ? (text) => onChangeText(text.replace(/\s*\n+\s*/g, " "))
            : onChangeText
        }
        onFocus={(e) => {
          setFocused(true);
          onFocus?.(e);
        }}
        onBlur={(e) => {
          setFocused(false);
          onBlur?.(e);
        }}
        {...props}
      />
      {error ? <Text className="mt-1 text-caption text-danger">{error}</Text> : null}
    </View>
  );
}
