import type { ReactElement, ReactNode } from "react";
import { View, Text, ScrollView, type RefreshControlProps } from "react-native";
import { SafeAreaView, useSafeAreaInsets } from "react-native-safe-area-context";
import { layout, space } from "../../theme/tokens";

export interface ScreenProps {
  children?: ReactNode;
  headline?: string;
  scroll?: boolean;
  className?: string;
  refreshControl?: ReactElement<RefreshControlProps>;
  // Tab screens sit under a floating pill tab bar (see (tabs)/_layout.tsx), which
  // is absolutely positioned and reserves no layout space. Set this so the last
  // items clear the bar instead of hiding behind it. Non-tab screens (pushed
  // routes) have no bar and leave it off.
  insetForTabBar?: boolean;
}

// Every screen composes from this: page bg, safe area, horizontal padding 24 (DESIGN.md §5).
export function Screen({
  children,
  headline,
  scroll = true,
  className,
  refreshControl,
  insetForTabBar = false,
}: ScreenProps) {
  const insets = useSafeAreaInsets();
  // Bar height + gap above the safe-area inset + one grid step of breathing room.
  const tabBarPad = insetForTabBar
    ? layout.tabBarHeight + layout.tabBarBottomGap + space[4] + insets.bottom
    : 0;

  const content = (
    <>
      {headline ? <Text className="mb-6 mt-4 text-display text-ink">{headline}</Text> : null}
      {children}
    </>
  );

  return (
    <SafeAreaView className="flex-1 bg-page" edges={["top", "left", "right"]}>
      {scroll ? (
        <ScrollView
          className={`flex-1 px-6 ${className ?? ""}`}
          contentContainerStyle={{ flexGrow: 1, paddingBottom: tabBarPad }}
          refreshControl={refreshControl}
        >
          {content}
        </ScrollView>
      ) : (
        <View className={`flex-1 px-6 ${className ?? ""}`} style={{ paddingBottom: tabBarPad }}>
          {content}
        </View>
      )}
    </SafeAreaView>
  );
}
