import { useEffect } from "react";
import { Text } from "react-native";
import Animated, { FadeInDown } from "react-native-reanimated";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import NetInfo, { useNetInfo } from "@react-native-community/netinfo";
import { WifiOff } from "lucide-react-native";
import { color, size } from "../theme/tokens";

// While we believe we're offline, force a fresh probe this often so the bar
// clears on its own. Short enough to feel immediate, long enough not to churn.
const RECHECK_MS = 2000;

// App-wide connectivity indicator, mounted once at the root (see _layout.tsx).
// It is a toast-style overlay pinned to the top safe area: it reserves no
// layout space and sets pointerEvents="none", so it can NEVER block a screen or
// swallow a tap. Recovery from a failed fetch stays each screen's own
// ErrorState + retry — this bar only tells the user *why* things stalled.
//
// netinfo is treated conservatively: the bar shows only when connectivity is
// *known* to be gone (isConnected === false, or the reachability probe has
// explicitly failed). A null/unknown state — normal for the first tick after
// launch, and while a probe is in flight — counts as online, so there's no
// false "offline" flash on boot or during a re-probe.
export function OfflineBanner() {
  const insets = useSafeAreaInsets();
  const { isConnected, isInternetReachable } = useNetInfo();
  const offline = isConnected === false || isInternetReachable === false;

  // `isInternetReachable` is netinfo's own HTTP probe, and it is only re-run
  // when the native layer reports a connectivity change. That event is not
  // reliable — on the iOS simulator (Mac WiFi toggled off/on) `isConnected`
  // flips back to true but the change event is routinely missed, leaving the
  // probe's stale `false` behind and pinning this bar on forever.
  //
  // So while we believe we're offline, drive recovery ourselves: NetInfo
  // .refresh() force-refreshes the singleton `useNetInfo` reads from (the
  // isolated useNetInfoInstance hook is explicitly NOT affected by it), which
  // re-reads native state and re-runs the probe. The bar therefore clears
  // within RECHECK_MS of connectivity returning even if no event ever fires.
  // Runs only while offline, so there's no polling in the steady state.
  useEffect(() => {
    if (!offline) return;
    const id = setInterval(() => {
      NetInfo.refresh().catch(() => {});
    }, RECHECK_MS);
    return () => clearInterval(id);
  }, [offline]);

  if (!offline) return null;

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
