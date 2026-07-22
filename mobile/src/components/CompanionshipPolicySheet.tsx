import { useMemo } from "react";
import { Modal, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { Button } from "./ui";
import { parseCompanionshipPolicy } from "../lib/companionship-policy";
import { space } from "../theme/tokens";

export interface CompanionshipPolicySheetProps {
  visible: boolean;
  /** Dismissed without acknowledging (backdrop, swipe down, hardware back). */
  onDismiss: () => void;
  /** "I understand" — the only path that unlocks posting. */
  onAcknowledge: () => void;
}

// Sheet may take at most this share of the screen; the scrollable policy body
// gets the rest after the header and the pinned footer have taken theirs.
const SHEET_MAX_HEIGHT_RATIO = 0.85;
const BODY_MAX_HEIGHT_RATIO = 0.7;

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
  const insets = useSafeAreaInsets();
  const lines = useMemo(() => parseCompanionshipPolicy(), []);

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onDismiss}>
      <View className="flex-1 justify-end">
        {/* Backdrop is its own layer rather than a Pressable wrapping the
            sheet: a ScrollView nested inside a pressable parent loses the
            responder negotiation on drag, which is what made the policy body
            unscrollable (and its last section unreachable). */}
        <Pressable className="absolute inset-0 bg-ink/40" onPress={onDismiss} />

        <View
          className="rounded-t-card bg-surface"
          style={{ maxHeight: height * SHEET_MAX_HEIGHT_RATIO }}
        >
          <View className="px-6 pb-4 pt-6">
            <Text className="mb-1 text-title font-semibold text-ink">Companionship Policy</Text>
            <Text className="text-caption text-muted">
              Read this before posting — it defines what a companionship task can and cannot be.
            </Text>
          </View>

          <ScrollView
            // flexShrink matters as much as maxHeight here: header + body +
            // footer can exceed the sheet's own cap, and RN children don't
            // shrink by default — without it the footer gets pushed off the
            // bottom of the clamped sheet on shorter screens.
            style={{ maxHeight: height * BODY_MAX_HEIGHT_RATIO, flexShrink: 1 }}
            showsVerticalScrollIndicator
            contentContainerStyle={{ paddingHorizontal: space[6], paddingBottom: space[6] }}
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

          {/* Pinned footer — always visible, never scrolls away. Its top
              hairline doubles as the "content continues above" edge cue. */}
          <View
            className="border-t border-line px-6 pt-4"
            style={{ paddingBottom: Math.max(insets.bottom, space[8]) }}
          >
            <Button label="I understand" onPress={onAcknowledge} />
          </View>
        </View>
      </View>
    </Modal>
  );
}
