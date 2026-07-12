import { Text } from "react-native";
import { useRouter } from "expo-router";
import { Screen, PressableScale } from "../../components/ui";

export default function Profile() {
  const router = useRouter();

  return (
    <Screen>
      {/* TODO: remove before TestFlight — dev-only route to the component showcase */}
      <PressableScale onLongPress={() => router.push("/dev-components")}>
        <Text className="mb-6 mt-4 text-display text-ink">Profile</Text>
      </PressableScale>
      <Text className="text-body text-muted">Your profile will show up here.</Text>
    </Screen>
  );
}
