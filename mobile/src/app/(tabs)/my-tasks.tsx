import { useState } from "react";
import { View } from "react-native";
import { useRouter } from "expo-router";
import { ClipboardList, Hourglass } from "lucide-react-native";
import { EmptyState, Pill, Screen } from "../../components/ui";

type Segment = "posted" | "working";

export default function MyTasks() {
  const router = useRouter();
  const [segment, setSegment] = useState<Segment>("posted");

  return (
    <Screen headline="My tasks">
      <View className="mb-6 flex-row gap-2">
        <Pill label="Posted" selected={segment === "posted"} onPress={() => setSegment("posted")} />
        <Pill
          label="Working"
          selected={segment === "working"}
          onPress={() => setSegment("working")}
        />
      </View>

      {segment === "posted" ? (
        <EmptyState
          icon={ClipboardList}
          title="No posted tasks yet"
          caption="Tasks you post will show up here."
          actionLabel="Post a task"
          onAction={() => router.push("/post-task")}
        />
      ) : (
        <EmptyState
          icon={Hourglass}
          title="No active work yet"
          caption="Tasks you accept will show up here."
        />
      )}
    </Screen>
  );
}
