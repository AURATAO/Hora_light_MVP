import { useEffect } from "react";
import type { ViewProps } from "react-native";
import Animated, {
  Easing,
  useAnimatedStyle,
  useSharedValue,
  withRepeat,
  withTiming,
} from "react-native-reanimated";
import { cn } from "../../theme/cn";

export interface SkeletonProps extends ViewProps {
  className?: string;
}

// Loading state matching real layout — never a centered spinner (DESIGN.md §4).
export function Skeleton({ className, style, ...props }: SkeletonProps) {
  const opacity = useSharedValue(0.5);

  useEffect(() => {
    opacity.value = withRepeat(withTiming(1, { duration: 700, easing: Easing.ease }), -1, true);
  }, [opacity]);

  const animatedStyle = useAnimatedStyle(() => ({ opacity: opacity.value }));

  return (
    <Animated.View
      className={cn("rounded-sm bg-line", className)}
      style={[style as object, animatedStyle]}
      {...props}
    />
  );
}
