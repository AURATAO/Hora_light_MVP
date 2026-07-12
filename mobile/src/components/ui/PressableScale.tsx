import type { ReactNode } from "react";
import { Pressable, type PressableProps } from "react-native";
import Animated, {
  ReduceMotion,
  useAnimatedStyle,
  useSharedValue,
  withSpring,
} from "react-native-reanimated";
import { cssInterop } from "nativewind";
import { motion } from "../../theme/tokens";

const AnimatedPressable = Animated.createAnimatedComponent(Pressable);
cssInterop(AnimatedPressable, { className: "style" });

export interface PressableScaleProps extends PressableProps {
  children?: ReactNode;
}

// Shared press feedback for every interactive component (DESIGN.md §4).
// Never re-implement scale/opacity press styling on a raw Pressable.
export function PressableScale({
  children,
  onPressIn,
  onPressOut,
  style,
  ...props
}: PressableScaleProps) {
  const scale = useSharedValue(1);
  const opacity = useSharedValue(1);

  const animatedStyle = useAnimatedStyle(() => ({
    transform: [{ scale: scale.value }],
    opacity: opacity.value,
  }));

  return (
    <AnimatedPressable
      style={[style as object, animatedStyle]}
      onPressIn={(e) => {
        scale.value = withSpring(motion.pressScale, { reduceMotion: ReduceMotion.System });
        opacity.value = withSpring(0.9, { reduceMotion: ReduceMotion.System });
        onPressIn?.(e);
      }}
      onPressOut={(e) => {
        scale.value = withSpring(1, { reduceMotion: ReduceMotion.System });
        opacity.value = withSpring(1, { reduceMotion: ReduceMotion.System });
        onPressOut?.(e);
      }}
      {...props}
    >
      {children}
    </AnimatedPressable>
  );
}
