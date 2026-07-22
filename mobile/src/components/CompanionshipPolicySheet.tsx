import { useMemo } from "react";
import { Modal, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { Button } from "./ui";
import { parseCompanionshipPolicy } from "../lib/companionship-policy";

export interface CompanionshipPolicySheetProps {
  visible: boolean;
  /** Dismissed without acknowledging (backdrop, swipe down, hardware back). */
  onDismiss: () => void;
  /** "I understand" — the only path that unlocks posting. */
  onAcknowledge: () => void;
}

// Policy gate for companionship tasks. Acknowledgement is deliberately the
// single explicit button: every dismissal path (backdrop tap, Android back,
// swipe) leaves the task un-postable, matching web's checkbox-then-confirm
// modal.
export function CompanionshipPolicySheet({
  visible,
  onDismiss,
  onAcknowledge,
}: CompanionshipPolicySheetProps) {
  const { height } = useWindowDimensions();
  const lines = useMemo(() => parseCompanionshipPolicy(), []);

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onDismiss}>
      <Pressable className="flex-1 justify-end bg-ink/40" onPress={onDismiss}>
        <Pressable className="rounded-t-card bg-surface p-6 pb-8" onPress={(e) => e.stopPropagation()}>
          <Text className="mb-1 text-title font-semibold text-ink">Companionship Policy</Text>
          <Text className="mb-4 text-caption text-muted">
            Read this before posting — it defines what a companionship task can and cannot be.
          </Text>

          <ScrollView
            style={{ maxHeight: height * 0.5 }}
            showsVerticalScrollIndicator={false}
            contentContainerStyle={{ paddingBottom: 4 }}
          >
            {lines.map((line, i) =>
              line.kind === "heading" ? (
                <Text
                  key={i}
                  className={`text-body font-semibold text-ink ${i === 0 ? "" : "mt-4"}`}
                >
                  {line.text}
                </Text>
              ) : (
                <View key={i} className="mt-2 flex-row gap-2">
                  <Text className="text-body text-muted">•</Text>
                  <Text className="flex-1 text-body text-ink">{line.text}</Text>
                </View>
              )
            )}
          </ScrollView>

          <Button label="I understand" onPress={onAcknowledge} className="mt-6" />
        </Pressable>
      </Pressable>
    </Modal>
  );
}
