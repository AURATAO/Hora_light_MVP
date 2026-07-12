import { Text } from "react-native";
import { Screen } from "../../components/ui";

export default function Home() {
  return (
    <Screen headline="Home">
      <Text className="text-body text-muted">Tasks near you will show up here.</Text>
    </Screen>
  );
}
