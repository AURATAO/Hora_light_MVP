import { Text } from "react-native";
import Animated, { FadeInDown } from "react-native-reanimated";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useNetInfo } from "@react-native-community/netinfo";
import { WifiOff } from "lucide-react-native";
import { color, size } from "../theme/tokens";

// App-wide connectivity indicator, mounted once at the root (see _layout.tsx).
// It is a toast-style overlay pinned to the top safe area: it reserves no
// layout space and sets pointerEvents="none", so it can NEVER block a screen or
// swallow a tap. Recovery from a failed fetch stays each screen's own
// ErrorState + retry — this bar only tells the user *why* things stalled.
//
// netinfo is treated conservatively: the bar shows only when connectivity is
// *known* to be gone (isConnected === false, or the reachability probe has
// explicitly failed). A null/unknown state — normal for the first tick after
// launch — counts as online, so there's no false "offline" flash on boot.
export function OfflineBanner() {
  const insets = useSafeAreaInsets();
  const { isConnected, isInternetReachable } = useNetInfo();
  const offline = isConnected === false || isInternetReachable === false;

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
