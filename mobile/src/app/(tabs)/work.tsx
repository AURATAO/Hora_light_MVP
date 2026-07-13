import { Briefcase } from "lucide-react-native";
import { EmptyState, Screen } from "../../components/ui";

export default function Work() {
  return (
    <Screen headline="Available tasks">
      <EmptyState
        icon={Briefcase}
        title="No tasks available"
        caption="Supporter features coming soon"
      />
    </Screen>
  );
}
