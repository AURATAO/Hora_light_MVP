import type { ReactNode } from "react";
import { View, Text, ScrollView } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";

export interface ScreenProps {
  children?: ReactNode;
  headline?: string;
  scroll?: boolean;
  className?: string;
}

// Every screen composes from this: page bg, safe area, horizontal padding 24 (DESIGN.md §5).
export function Screen({ children, headline, scroll = true, className }: ScreenProps) {
  const content = (
    <>
      {headline ? <Text className="mb-6 mt-4 text-display text-ink">{headline}</Text> : null}
      {children}
    </>
  );

  return (
    <SafeAreaView className="flex-1 bg-page" edges={["top", "left", "right"]}>
      {scroll ? (
        <ScrollView
          className={`flex-1 px-6 ${className ?? ""}`}
          contentContainerStyle={{ flexGrow: 1 }}
        >
          {content}
        </ScrollView>
      ) : (
        <View className={`flex-1 px-6 ${className ?? ""}`}>{content}</View>
      )}
    </SafeAreaView>
  );
}
