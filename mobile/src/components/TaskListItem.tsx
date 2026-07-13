import { Text, View } from "react-native";
import { UserRound } from "lucide-react-native";
import { Badge, Card } from "./ui";
import { getCategoryMeta } from "../lib/categories";
import { deriveTaskStatus, formatRelativeTime, statusLabel } from "../lib/task-utils";
import type { Task } from "../lib/types";
import { color, size } from "../theme/tokens";

export interface TaskListItemProps {
  task: Task;
  onPress: () => void;
  className?: string;
}

// Shared task-row presentation for My Tasks and (as a header) Task Detail.
// Home's own private row (mobile/src/app/(tabs)/home.tsx) predates this and
// is left as-is — not touched here to avoid regressing an already-shipped screen.
export function TaskListItem({ task, onPress, className }: TaskListItemProps) {
  const status = deriveTaskStatus(task);
  const meta = getCategoryMeta(task.category);
  const Icon = meta.icon;

  return (
    <Card className={className} onPress={onPress}>
      <View className="flex-row items-center justify-between">
        <View className="mr-2 flex-1 flex-row items-center gap-2">
          <Icon color={color.muted} size={18} strokeWidth={size.iconStroke} />
          <Text className="flex-1 text-body font-semibold text-ink" numberOfLines={1}>
            {task.title}
          </Text>
        </View>
        {status === "assigned" ? (
          <UserRound color={color.muted} size={16} strokeWidth={size.iconStroke} />
        ) : null}
      </View>
      <View className="mt-2 flex-row items-center justify-between">
        <Text className="text-caption text-muted">
          {meta.label} · {formatRelativeTime(task.created_at)}
        </Text>
        <Badge label={statusLabel(status)} variant="success" />
      </View>
    </Card>
  );
}
