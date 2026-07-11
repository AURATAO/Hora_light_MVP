import { View, ActivityIndicator } from "react-native";
import { Redirect } from "expo-router";
import { useAuthState } from "./_layout";

export default function Index() {
  const { loading, authenticated } = useAuthState();

  if (loading) {
    return (
      <View className="flex-1 items-center justify-center bg-neutralbg">
        <ActivityIndicator />
      </View>
    );
  }

  return <Redirect href={authenticated ? "/(tabs)/home" : "/(auth)/login"} />;
}
