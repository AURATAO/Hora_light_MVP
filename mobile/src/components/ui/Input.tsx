import { useState } from "react";
import { View, Text, TextInput, type TextInputProps } from "react-native";
import { color } from "../../theme/tokens";

export interface InputProps extends TextInputProps {
  label?: string;
  error?: string;
}

export function Input({ label, error, className, onFocus, onBlur, ...props }: InputProps) {
  const [focused, setFocused] = useState(false);

  return (
    <View>
      {label ? <Text className="mb-1 text-caption text-muted">{label}</Text> : null}
      <TextInput
        className={`h-[52px] rounded-sm border bg-surface px-4 text-body text-ink ${
          focused ? "border-ink" : "border-line"
        } ${className ?? ""}`}
        placeholderTextColor={color.muted}
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
