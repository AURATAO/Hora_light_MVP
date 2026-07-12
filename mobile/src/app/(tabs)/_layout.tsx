import { Tabs } from "expo-router";
import { StyleSheet } from "react-native";
import { House, ListChecks, User } from "lucide-react-native";
import { color, size, type as typeScale } from "../../theme/tokens";

const TAB_ICON_SIZE = 22;

export default function TabsLayout() {
  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: color.ink,
        tabBarInactiveTintColor: color.inactive,
        tabBarStyle: {
          backgroundColor: color.surface,
          borderTopWidth: StyleSheet.hairlineWidth,
          borderTopColor: color.line,
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
        name="my-tasks"
        options={{
          title: "My tasks",
          tabBarIcon: ({ color: tintColor }) => (
            <ListChecks color={tintColor} size={TAB_ICON_SIZE} strokeWidth={size.iconStroke} />
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
