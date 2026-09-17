import type { ReactNode } from "react";
import { Pressable, type PressableProps } from "react-native";
import Animated, {
  ReduceMotion,
  useAnimatedStyle,
  useSharedValue,
  withSpring,
} from "react-native-reanimated";
import { motion } from "../../theme/tokens";

const AnimatedPressable = Animated.createAnimatedComponent(Pressable);

export interface PressableScaleProps extends PressableProps {
  children?: ReactNode;
}

// Shared press feedback for every interactive component (DESIGN.md §4).
// Never re-implement scale/opacity press styling on a raw Pressable.
//
// ONE node: className and the animated style both land on this Pressable, and
// `children` are its DIRECT children. That last part is the whole point.
//
// From 2026-07-14 to 2026-09-17 the animation lived on an inner, className-free
// `<Animated.View>` wrapping the children, to dodge a NativeWind bug where a
// className-derived background-color lost to an explicit `style` array on the
// same node (every primary Button and selected Pill rendered with the right
// shape and no fill). That workaround bought a correct background and paid for
// it with layout: the wrapper is a plain View with RN's default
// `flexDirection: column`, so a caller writing
//
//     <PressableScale className="flex-row items-center"><Icon/><Text/></PressableScale>
//
// got its two children STACKED. The className was styling a box with exactly
// one child — the wrapper — and never reached what the caller meant by it.
// Thirteen call sites were affected; the visible ones were the Earnings back
// button (a chevron above the word "Back"), the profile rows, the home cards,
// and every justify-between row in the app.
//
// NativeWind 4.2.6 + reanimated 4.5 no longer drop the background, so the
// workaround is gone. Verified on the simulator against four canaries before
// removing it — background fill, `absolute` positioning, `self-start`, and the
// press animation itself (pin the shared values to 0.6/0.35, full-relaunch,
// and confirm everything renders small and faded; Fast Refresh preserves hook
// state, so a hot reload will NOT show it).
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
    <AnimatedPressable
      className={className}
      style={[style, animatedStyle]}
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
