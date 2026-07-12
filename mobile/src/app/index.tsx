import { View, ActivityIndicator } from "react-native";
import { Redirect } from "expo-router";
import { useAuthState } from "./_layout";
import { color } from "../theme/tokens";

export default function Index() {
  const { loading, authenticated } = useAuthState();

  if (loading) {
    // Full-screen boot is the one exception to "never a centered spinner" (DESIGN.md §4).
    return (
      <View className="flex-1 items-center justify-center bg-page">
        <ActivityIndicator color={color.ink} />
      </View>
    );
  }

  return <Redirect href={authenticated ? "/(tabs)/home" : "/(auth)/login"} />;
}
