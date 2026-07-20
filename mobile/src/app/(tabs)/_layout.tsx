import { Tabs } from "expo-router";
import { StyleSheet } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { Briefcase, House, List, User } from "lucide-react-native";
import { color, layout, radius, size, space, type as typeScale } from "../../theme/tokens";

const TAB_ICON_SIZE = 22;

export default function TabsLayout() {
  const insets = useSafeAreaInsets();

  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: color.ink,
        tabBarInactiveTintColor: color.inactive,
        // Floating pill: absolutely positioned, inset from the screen edges and
        // lifted above the safe area. Depth is a hairline all around (DESIGN.md
        // §1 favors a hairline over shadows) plus a whisper of shadow — the one
        // shadow §1 allows the tab bar — so the pill reads as lifted, not painted on.
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
          shadowOpacity: 0.06,
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
      <Tabs.Screen
        name="home"
        options={{
          title: "Home",
          tabBarIcon: ({ color: tintColor }) => (
            <House color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
          ),
        }}
      />
      <Tabs.Screen
        name="work"
        options={{
          title: "Work",
          tabBarIcon: ({ color: tintColor }) => (
            <Briefcase color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
          ),
        }}
      />
      <Tabs.Screen
        name="my-tasks"
        options={{
          title: "Tasks",
          tabBarIcon: ({ color: tintColor }) => (
            <List color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
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
  );
}
