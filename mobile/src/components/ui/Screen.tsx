import type { ReactElement, ReactNode } from "react";
import {
  View,
  Text,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
  type RefreshControlProps,
} from "react-native";
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
  // Screens whose inputs sit in the lower half: lifts the content so the focused
  // field (and its primary button) stay above the software keyboard. Opt-in
  // rather than always-on, because the lift is wasted work — and on short
  // screens a visible jump — where nothing is typed into.
  avoidKeyboard?: boolean;
  // Vertically centers the content when it is shorter than the viewport. Use
  // this instead of `justify-center` in `className`: with `scroll` on, the
  // alignment has to live on the scroll content container, not the ScrollView.
  center?: boolean;
}

// Every screen composes from this: page bg, safe area, horizontal padding 24 (DESIGN.md §5).
export function Screen({
  children,
  headline,
  scroll = true,
  className,
  refreshControl,
  insetForTabBar = false,
  avoidKeyboard = false,
  center = false,
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

  const body = scroll ? (
    <ScrollView
      className={`flex-1 px-6 ${className ?? ""}`}
      contentContainerStyle={{
        flexGrow: 1,
        paddingBottom: tabBarPad,
        justifyContent: center ? "center" : undefined,
      }}
      // Without this, the first tap while the keyboard is open only dismisses it
      // and the button underneath needs a second tap.
      keyboardShouldPersistTaps="handled"
      refreshControl={refreshControl}
    >
      {content}
    </ScrollView>
  ) : (
    <View className={`flex-1 px-6 ${className ?? ""}`} style={{ paddingBottom: tabBarPad }}>
      {content}
    </View>
  );

  return (
    <SafeAreaView className="flex-1 bg-page" edges={["top", "left", "right"]}>
      {avoidKeyboard ? (
        // No keyboardVerticalOffset: this sits inside a SafeAreaView that omits
        // the bottom edge, so it already reaches the physical bottom of the
        // screen. Android is left to the platform's own adjustResize — adding
        // padding there would double-count the keyboard height.
        <KeyboardAvoidingView
          style={{ flex: 1 }}
          behavior={Platform.OS === "ios" ? "padding" : undefined}
        >
          {body}
        </KeyboardAvoidingView>
      ) : (
        body
      )}
    </SafeAreaView>
  );
}
