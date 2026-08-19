import type { ReactNode } from "react";
import { View, Text, type ViewProps } from "react-native";
import { PressableScale } from "./PressableScale";
import { cn } from "../../theme/cn";

export interface CardProps extends ViewProps {
  children?: ReactNode;
  onPress?: () => void;
}

// Surface bg, hairline border, radius card, padding 16, no shadow (DESIGN.md §5).
export function Card({ children, className, onPress, ...props }: CardProps) {
  const cardClassName = cn("rounded-card border border-line bg-surface p-4", className);

  if (onPress) {
    return (
      <PressableScale onPress={onPress} className={cardClassName}>
        {children}
      </PressableScale>
    );
  }

  return (
    <View className={cardClassName} {...props}>
      {children}
    </View>
  );
}

export interface TaskCardProps {
  title: string;
  price: string;
  metadata: string;
  badges?: ReactNode;
  onPress?: () => void;
  className?: string;
}

export function TaskCard({ title, price, metadata, badges, onPress, className }: TaskCardProps) {
  return (
    <Card className={className} onPress={onPress}>
      <View className="flex-row items-center justify-between">
        <Text className="flex-1 pr-2 text-body font-semibold text-ink" numberOfLines={1}>
          {title}
        </Text>
        <Text className="text-title font-semibold text-ink">{price}</Text>
      </View>
      <Text className="mt-1 text-caption text-muted">{metadata}</Text>
      {badges ? <View className="mt-2 flex-row gap-2">{badges}</View> : null}
    </Card>
  );
}
