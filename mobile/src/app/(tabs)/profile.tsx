import { Text } from "react-native";
import { useRouter } from "expo-router";
import { User } from "lucide-react-native";
import { EmptyState, PressableScale, Screen } from "../../components/ui";

export default function Profile() {
  const router = useRouter();

  return (
    <Screen>
      {/* TODO: remove before TestFlight — dev-only route to the component showcase */}
      <PressableScale onLongPress={() => router.push("/dev-components")}>
        <Text className="mb-6 mt-4 text-display text-ink">Profile</Text>
      </PressableScale>
      <EmptyState icon={User} title="Your profile" caption="Profile details will show up here." />
    </Screen>
  );
}
