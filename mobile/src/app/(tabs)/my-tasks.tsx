import { Text } from "react-native";
import { Screen } from "../../components/ui";

export default function MyTasks() {
  return (
    <Screen headline="My tasks">
      <Text className="text-body text-muted">
        Tasks you've posted or accepted will show up here.
      </Text>
    </Screen>
  );
}
