import type { ReactNode } from "react";
import { Pressable, type PressableProps } from "react-native";
import Animated, {
  ReduceMotion,
  useAnimatedStyle,
  useSharedValue,
  withSpring,
} from "react-native-reanimated";
import { motion } from "../../theme/tokens";

export interface PressableScaleProps extends PressableProps {
  children?: ReactNode;
}

// Shared press feedback for every interactive component (DESIGN.md §4).
// Never re-implement scale/opacity press styling on a raw Pressable.
//
// className is applied to the outer, plain `Pressable` (NativeWind supports
// it natively) rather than to an Animated.createAnimatedComponent(Pressable)
// wired through cssInterop's className->style remap: combining that remap
// with our own explicit `style={[style, animatedStyle]}` on the SAME node
// put both writing into the `style` prop, and the className-derived
// background-color silently lost that fight every time (border/padding
// still came through, which is what made this so easy to miss — every
// primary Button, selected Pill, and onPress Card rendered with the
// right shape but no fill, e.g. white text on no background at all).
// The scale/opacity press animation now lives on an inner, className-free
// Animated.View so the two systems never touch the same prop.
export function PressableScale({
  children,
  onPressIn,
  onPressOut,
  className,
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
    <Pressable
      className={className}
      style={style}
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
      <Animated.View style={animatedStyle}>{children}</Animated.View>
    </Pressable>
  );
}
