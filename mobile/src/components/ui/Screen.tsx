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
import { cn } from "../../theme/cn";

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

  // KEYBOARD AVOIDANCE, BY PLATFORM.
  //
  // On iOS the ScrollView handles the keyboard ITSELF via
  // automaticallyAdjustKeyboardInsets: UIKit grows the content inset by the
  // keyboard's height, so scrolling to the bottom puts the last control above
  // the keyboard, not under it. KeyboardAvoidingView's `padding` was doing
  // this job before, and it is what the build 11 device run caught leaving
  // the Post Task button two-thirds hidden: KAV measures the keyboard's
  // overlap against its own frame, and inside a `presentation: "modal"`
  // sheet that frame is offset from the window by the sheet's top inset —
  // so the overlap it computed was short by exactly that much, which on a
  // 52pt button is most of the button.
  //
  // Android keeps KeyboardAvoidingView: automaticallyAdjustKeyboardInsets is
  // iOS-only, and adjustResize plus `padding` behaves there.
  const iosScrollInsets = avoidKeyboard && Platform.OS === "ios";
  const wrapInKAV = avoidKeyboard && Platform.OS !== "ios";

  const body = scroll ? (
    <ScrollView
      className={cn("flex-1 px-6", className)}
      contentContainerStyle={{
        flexGrow: 1,
        // One grid step of clearance under the last control when the keyboard
        // is up, so a focused field's button is not flush against the inset.
        paddingBottom: tabBarPad + (iosScrollInsets ? space[6] : 0),
        justifyContent: center ? "center" : undefined,
      }}
      // Without this, the first tap while the keyboard is open only dismisses it
      // and the button underneath needs a second tap.
      keyboardShouldPersistTaps="handled"
      automaticallyAdjustKeyboardInsets={iosScrollInsets}
      keyboardDismissMode={iosScrollInsets ? "interactive" : "none"}
      refreshControl={refreshControl}
    >
      {content}
    </ScrollView>
  ) : (
    <View className={cn("flex-1 px-6", className)} style={{ paddingBottom: tabBarPad }}>
      {content}
    </View>
  );

  return (
    <SafeAreaView className="flex-1 bg-page" edges={["top", "left", "right"]}>
      {wrapInKAV ? (
        // Android only — see the note above `body`. No keyboardVerticalOffset:
        // this sits inside a SafeAreaView that omits the bottom edge, so it
        // already reaches the physical bottom of the screen.
        <KeyboardAvoidingView style={{ flex: 1 }} behavior="padding">
          {body}
        </KeyboardAvoidingView>
      ) : (
        body
      )}
    </SafeAreaView>
  );
}
