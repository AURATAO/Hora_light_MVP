import { Tabs } from "expo-router";
import { StyleSheet } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { House, List, User, Wallet } from "lucide-react-native";
import { useSupporterStatus } from "../../lib/use-supporter-status";
import { NamePromptSheet } from "../../components/NamePromptSheet";
import { color, layout, radius, size, space, type as typeScale } from "../../theme/tokens";

const TAB_ICON_SIZE = 22;

export default function TabsLayout() {
  const insets = useSafeAreaInsets();
  // My jobs and Earnings are for approved supporters. `null` (not yet known)
  // hides them, and they appear once the profile lands: showing a tab and
  // taking it away is worse than showing it a beat late.
  const { isApproved } = useSupporterStatus();

  return (
    <>
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: color.ink,
        tabBarInactiveTintColor: color.inactive,
        // Floating pill: absolutely positioned, inset from the screen edges and
        // lifted above the safe area. Depth is a hairline all around for edge
        // definition plus a soft elevation shadow — the one shadow DESIGN.md §1
        // allows the tab bar — so the pill reads as a floating island against
        // `page`, not painted on. Kept low-opacity: elevation, not a drop shadow.
        //
        // The shadow renders because nothing clips it: the bar is a sibling of
        // the screen container inside the navigator, and no ancestor sets
        // `overflow: hidden`. `backgroundColor` is required for the iOS shadow
        // (transparent views cast none) and `elevation` is Android's equivalent.
        //
        // The inset MUST be set via the logical `start`/`end` props: the tab
        // bar's built-in style (react-navigation BottomTabBar `styles.bottom`)
        // already sets `start: 0, end: 0`, and in Yoga logical insets win over
        // physical `left`/`right`. Setting only left/right leaves the bar full
        // width; `left`/`right` are kept for web parity where logical props map
        // through differently.
        tabBarStyle: {
          position: "absolute",
          start: layout.tabBarInset,
          end: layout.tabBarInset,
          left: layout.tabBarInset,
          right: layout.tabBarInset,
          bottom: insets.bottom + layout.tabBarBottomGap,
          height: layout.tabBarHeight,
          paddingTop: space[2],
          paddingBottom: space[2],
          borderRadius: radius.pill,
          backgroundColor: color.surface,
          borderWidth: StyleSheet.hairlineWidth,
          borderTopWidth: StyleSheet.hairlineWidth,
          borderColor: color.line,
          shadowColor: color.ink,
          shadowOffset: { width: 0, height: 4 },
          shadowOpacity: 0.1,
          shadowRadius: 12,
          elevation: 8,
        },
        tabBarLabelStyle: {
          fontSize: typeScale.micro.fontSize,
          lineHeight: typeScale.micro.lineHeight,
          fontWeight: typeScale.micro.fontWeight,
        },
      }}
    >
      {/* SUPPORTER-ONLY APP (PLAN §13b-3, 2026-11-06). Requesters book over
          iMessage, WhatsApp or the web; this app is where supporters find,
          do and get paid for tasks. Tasks is first: an approved supporter
          lands on open work, anyone else on how to become a supporter
          (work.tsx). My jobs and Earnings mean nothing before approval, so
          they stay hidden until then — `href: null` hides the button AND
          blocks navigation to the route. */}
      <Tabs.Screen
        name="work"
        options={{
          title: "Tasks",
          tabBarIcon: ({ color: tintColor }) => (
            <House color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
          ),
        }}
      />
      <Tabs.Screen
        name="my-tasks"
        options={{
          title: "My jobs",
          href: isApproved ? undefined : null,
          tabBarIcon: ({ color: tintColor }) => (
            <List color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
          ),
        }}
      />
      <Tabs.Screen
        name="earnings"
        options={{
          title: "Earnings",
          href: isApproved ? undefined : null,
          tabBarIcon: ({ color: tintColor }) => (
            <Wallet color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
          ),
        }}
      />
      <Tabs.Screen
        name="profile"
        options={{
          title: "Profile",
          tabBarIcon: ({ color: tintColor }) => (
            <User color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
          ),
        }}
      />
    </Tabs>
    {/* "What should we call you?" — once, for an account that finished
        onboarding before a name was required. Lives here rather than on a
        screen so it meets them wherever they land. */}
    <NamePromptSheet />
    </>
  );
}
