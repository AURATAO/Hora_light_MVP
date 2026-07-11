import { Tabs } from "expo-router";
import { Ionicons } from "@expo/vector-icons";

// Mirrors `brand` in ../../../../design-tokens/colors.js (S-30). Metro can't
// resolve a runtime import across the package boundary the way the Node-side
// tailwind.config.js require can; className-based colors elsewhere in the app
// still flow through that shared file via NativeWind.
const BRAND_COLOR = "#3A5A2D";

export default function TabsLayout() {
  return (
    <Tabs screenOptions={{ tabBarActiveTintColor: BRAND_COLOR }}>
      <Tabs.Screen
        name="home"
        options={{
          title: "Home",
          tabBarIcon: ({ color, size }) => <Ionicons name="home" color={color} size={size} />,
        }}
      />
      <Tabs.Screen
        name="my-tasks"
        options={{
          title: "My Tasks",
          tabBarIcon: ({ color, size }) => <Ionicons name="list" color={color} size={size} />,
        }}
      />
      <Tabs.Screen
        name="profile"
        options={{
          title: "Profile",
          tabBarIcon: ({ color, size }) => <Ionicons name="person" color={color} size={size} />,
        }}
      />
    </Tabs>
  );
}
