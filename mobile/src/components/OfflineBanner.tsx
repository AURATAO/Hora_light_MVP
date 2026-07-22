import { useEffect, useState } from "react";
import { Text } from "react-native";
import Animated, { FadeInDown } from "react-native-reanimated";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useNetInfo } from "@react-native-community/netinfo";
import { WifiOff } from "lucide-react-native";
import { API_BASE_URL } from "../lib/api";
import { color, size } from "../theme/tokens";

// How often to re-probe the backend while netinfo claims we're offline.
const RECHECK_MS = 2000;
// A probe that hasn't answered in this long counts as unreachable. Kept below
// RECHECK_MS * 2 so probes can't pile up.
const PROBE_TIMEOUT_MS = 3000;

// App-wide connectivity indicator, mounted once at the root (see _layout.tsx).
// It is a toast-style overlay pinned to the top safe area: it reserves no
// layout space and sets pointerEvents="none", so it can NEVER block a screen or
// swallow a tap. Recovery from a failed fetch stays each screen's own
// ErrorState + retry — this bar only tells the user *why* things stalled.
//
// Two signals, with different jobs:
//
//   netinfo is the trigger to SHOW. It reacts instantly to a real disconnect.
//   It is treated conservatively — only a *known*-gone state (isConnected
//   === false, or its reachability probe explicitly failed) counts; a
//   null/unknown state means online, so there's no false flash on boot.
//
//   A HEAD probe of our own backend is the authority to CLEAR, because netinfo
//   alone cannot be trusted to recover. On iOS it derives reachability from
//   the native path status, and when that says disconnected it sets
//   isInternetReachable=false *and cancels its own retry timer*
//   (internetReachability.js `_setExpectsConnection`) — so it has no
//   self-recovery path and simply waits for a native "connected" event. On the
//   iOS simulator that event frequently never arrives after the host Mac's
//   WiFi returns: the native path status stays stuck while the data path works
//   perfectly. NetInfo.refresh() cannot help — it re-reads the same stuck
//   native value and re-asserts offline.
//
// So while netinfo claims offline we verify it ourselves every RECHECK_MS.
// Any HTTP response — 4xx and 5xx included — proves the round trip succeeded
// and therefore that we are online; only a network-level throw or a timeout
// counts as offline. (This is why an auth-gated endpoint is fine: a 401 is
// still proof of reachability.) The probe runs *only* while netinfo claims
// offline, so there is no steady-state cost.
export function OfflineBanner() {
  const insets = useSafeAreaInsets();
  const { isConnected, isInternetReachable } = useNetInfo();
  const netInfoOffline = isConnected === false || isInternetReachable === false;

  // null = not probed yet. Shown immediately on entering the offline state so
  // a genuine disconnect surfaces at once, then corrected by the first probe.
  const [reachable, setReachable] = useState<boolean | null>(null);

  useEffect(() => {
    if (!netInfoOffline) {
      setReachable(null);
      return;
    }

    let cancelled = false;

    const probe = async () => {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
      try {
        // `no-store` matters: a cached response would resolve without a round
        // trip and falsely prove reachability.
        await fetch(API_BASE_URL, {
          method: "HEAD",
          cache: "no-store",
          signal: controller.signal,
        });
        if (!cancelled) setReachable(true);
      } catch {
        if (!cancelled) setReachable(false);
      } finally {
        clearTimeout(timeout);
      }
    };

    probe();
    const id = setInterval(probe, RECHECK_MS);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [netInfoOffline]);

  if (!netInfoOffline || reachable === true) return null;

  return (
    <Animated.View
      entering={FadeInDown}
      pointerEvents="none"
      style={{ position: "absolute", top: insets.top, left: 0, right: 0, zIndex: 50 }}
      className="flex-row items-center justify-center gap-2 bg-ink px-6 py-2"
    >
      <WifiOff color={color.white} size={14} strokeWidth={size.iconStroke} />
      <Text className="text-caption font-semibold text-white">No internet connection</Text>
    </Animated.View>
  );
}
