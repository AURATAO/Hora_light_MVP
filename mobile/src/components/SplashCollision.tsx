import React, { useCallback, useEffect, useState } from 'react';
import { Dimensions, StyleSheet, View } from 'react-native';
import Animated, {
  Easing,
  runOnJS,
  useAnimatedStyle,
  useSharedValue,
  withDelay,
  withSequence,
  withTiming,
} from 'react-native-reanimated';

// ─── Brand & tuning ──────────────────────────────────────────────
const GREEN = '#3A5A2D';
const GOLD = '#E1B145';

const DOT = 18; // dot diameter
const GAP = 46; // resting edge-to-edge gap between the two dots
const MEET = GAP / 2; // travel per dot so the edges touch exactly

// Impact timings (ms). Three strikes with damped rebounds: 70% → 40% → stick.
const STRIKE = 170;
const REBOUND_1 = 200; // bounce back to 70% apart
const REBOUND_2 = 170; // bounce back to 40% apart
const STICK = 150;

const BURST_ANTICIPATION = 100; // merged dot contracts before exploding
const BURST_EXPAND = 450;
const REVEAL = 550;

const { width: W, height: H } = Dimensions.get('window');
const DIAG = Math.hypot(W, H);
const BURST_BASE = DOT + 2;

type Props = {
  /** Called when the reveal is finished — unmount the splash here. */
  onFinish: () => void;
};

/**
 * HO:RA opening animation.
 *
 * Two gold colon dots slam into each other with damped elastic
 * rebounds, merge into one, and the merged dot bursts into a gold
 * field that opens into the home screen via a circular reveal.
 *
 * Mount this as the last child of your root layout (absolute fill,
 * above app content). Keep the native splash (solid #3A5A2D with the
 * two static dots) visible via expo-splash-screen until this mounts,
 * then call SplashScreen.hideAsync() — the handoff is seamless
 * because frame 0 here matches the native splash exactly.
 */
export default function SplashCollision({ onFinish }: Props) {
  // Phase 1: green field, dots collide, gold burst covers the screen.
  // Phase 2: gold ring with a growing hole reveals the app beneath.
  const [phase, setPhase] = useState<'collide' | 'reveal'>('collide');

  const y = useSharedValue(0); // 0 → MEET, top dot moves down, bottom mirrors
  const squash = useSharedValue(1); // scaleX = squash, scaleY = 1 / squash
  const dotOpacity = useSharedValue(1);
  const burstScale = useSharedValue(0);
  const hole = useSharedValue(0); // reveal hole diameter

  const startReveal = useCallback(() => {
    setPhase('reveal');
    hole.value = withTiming(
      DIAG * 1.05,
      { duration: REVEAL, easing: Easing.bezier(0.3, 0, 0.2, 1) },
      (done) => {
        if (done) runOnJS(onFinish)();
      },
    );
  }, [hole, onFinish]);

  useEffect(() => {
    const ease = { in: Easing.in(Easing.quad), out: Easing.out(Easing.quad) };

    // Damped mutual collision: strike, rebound smaller, strike, smaller, stick.
    y.value = withSequence(
      withTiming(MEET, { duration: STRIKE, easing: ease.in }),
      withTiming(MEET * 0.3, { duration: REBOUND_1, easing: ease.out }),
      withTiming(MEET, { duration: STRIKE, easing: ease.in }),
      withTiming(MEET * 0.62, { duration: REBOUND_2, easing: ease.out }),
      withTiming(MEET, { duration: STICK, easing: ease.in }),
    );

    // Squash on every impact, slight stretch while travelling.
    // Cumulative timings line up with the y sequence above.
    squash.value = withSequence(
      withTiming(0.92, { duration: STRIKE - 50 }), // stretch on approach
      withTiming(1.4, { duration: 50 }), //   impact 1
      withTiming(0.94, { duration: REBOUND_1 - 80 }),
      withTiming(1.0, { duration: 80 }),
      withTiming(0.95, { duration: STRIKE - 60 }),
      withTiming(1.35, { duration: 60 }), //  impact 2
      withTiming(0.97, { duration: REBOUND_2 - 60 }),
      withTiming(1.0, { duration: 60 }),
      withTiming(1.35, { duration: STICK }), // impact 3 — stick
    );

    const collisionTotal = STRIKE + REBOUND_1 + STRIKE + REBOUND_2 + STICK;

    // Merge: hide the two dots, show the burst dot in their place.
    dotOpacity.value = withDelay(
      collisionTotal + 40,
      withTiming(0, { duration: 30 }),
    );
    burstScale.value = withDelay(
      collisionTotal + 40,
      withSequence(
        withTiming(1, { duration: 1 }),
        withTiming(0.8, { duration: BURST_ANTICIPATION }), // inhale
        withTiming(
          (DIAG * 1.1) / BURST_BASE,
          { duration: BURST_EXPAND, easing: Easing.bezier(0.55, 0, 0.35, 1) },
          (done) => {
            if (done) runOnJS(startReveal)();
          },
        ),
      ),
    );
  }, [y, squash, dotOpacity, burstScale, startReveal]);

  const topDot = useAnimatedStyle(() => ({
    opacity: dotOpacity.value,
    transform: [
      { translateY: y.value },
      { scaleX: squash.value },
      { scaleY: 1 / squash.value },
    ],
  }));

  const bottomDot = useAnimatedStyle(() => ({
    opacity: dotOpacity.value,
    transform: [
      { translateY: -y.value },
      { scaleX: squash.value },
      { scaleY: 1 / squash.value },
    ],
  }));

  const burst = useAnimatedStyle(() => ({
    transform: [{ scale: burstScale.value }],
  }));

  // Circular reveal: a gold "ring" whose border is thicker than the
  // screen diagonal. Growing the ring's inner hole punches an opening
  // through to the app content underneath — no mask library needed.
  const ring = useAnimatedStyle(() => {
    const d = hole.value;
    return {
      width: d + DIAG * 2,
      height: d + DIAG * 2,
      borderRadius: (d + DIAG * 2) / 2,
      borderWidth: DIAG,
    };
  });

  return (
    <View style={StyleSheet.absoluteFill} pointerEvents="none">
      {phase === 'collide' ? (
        <View style={[StyleSheet.absoluteFill, styles.field]}>
          <View style={styles.colon}>
            <Animated.View style={[styles.dot, topDot]} />
            <Animated.View style={[styles.dot, bottomDot]} />
          </View>
          <Animated.View style={[styles.burst, burst]} />
        </View>
      ) : (
        <View style={[StyleSheet.absoluteFill, styles.center]}>
          <Animated.View style={[styles.ring, ring]} />
        </View>
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  field: {
    backgroundColor: GREEN,
    alignItems: 'center',
    justifyContent: 'center',
  },
  center: {
    alignItems: 'center',
    justifyContent: 'center',
    overflow: 'hidden',
  },
  colon: {
    alignItems: 'center',
    gap: GAP,
  },
  dot: {
    width: DOT,
    height: DOT,
    borderRadius: DOT / 2,
    backgroundColor: GOLD,
  },
  burst: {
    position: 'absolute',
    width: BURST_BASE,
    height: BURST_BASE,
    borderRadius: BURST_BASE / 2,
    backgroundColor: GOLD,
  },
  ring: {
    borderColor: GOLD,
    backgroundColor: 'transparent',
  },
});
