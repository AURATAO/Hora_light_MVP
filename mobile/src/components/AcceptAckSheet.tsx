import { Modal, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { Button } from "./ui";
import { ackParagraphs, type AcceptRequirements } from "../lib/accept-ack";

// What a supporter confirms before accepting a task (PLAN 13c).
//
// Every word on this sheet is the server's: the heading, each item's title
// and body, and the button. Nothing is decided here: which items a task
// asks for and how often is server/accept_acks.json, so the wording can
// change after a legal review without an app release.
//
// One solid button (DESIGN.md §5): accepting the notes IS accepting the
// task, so there is no second confirmation behind it.

export interface AcceptAckSheetProps {
  requirements: AcceptRequirements | null;
  /** True while the accept request is in flight. */
  busy: boolean;
  error?: string | null;
  onClose: () => void;
  onAccept: () => void;
}

export function AcceptAckSheet({ requirements, busy, error, onClose, onAccept }: AcceptAckSheetProps) {
  const { height } = useWindowDimensions();
  const visible = Boolean(requirements && requirements.items.length > 0);

  function handleClose() {
    if (busy) return;
    onClose();
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <View className="flex-1 justify-end">
        {/* A sibling of the sheet, never its parent (test/sheets-a11y): a
            wrapping Pressable would collapse the notes and the button into
            one VoiceOver element. */}
        <Pressable
          className="absolute inset-0 bg-ink/40"
          onPress={handleClose}
          accessibilityRole="button"
          accessibilityLabel="Close"
        />
        <View className="rounded-t-card bg-surface p-6 pb-8" style={{ maxHeight: height * 0.86 }}>
          <Text className="mb-4 text-title font-semibold text-ink" accessibilityRole="header">
            {requirements?.heading}
          </Text>

          {/* The notes scroll; the button does not. Long text must never
              push the only way forward off the screen. */}
          <ScrollView className="mb-4" showsVerticalScrollIndicator>
            {(requirements?.items ?? []).map((item, i) => (
              <View
                key={`${item.key}@${item.version}`}
                className={i > 0 ? "mt-4 border-t border-line pt-4" : ""}
              >
                {item.title ? (
                  <Text className="mb-2 text-body font-semibold text-ink">{item.title}</Text>
                ) : null}
                {ackParagraphs(item.body).map((p, j) => (
                  <Text key={j} className={`text-body text-ink ${j > 0 ? "mt-2" : ""}`}>
                    {p}
                  </Text>
                ))}
              </View>
            ))}
          </ScrollView>

          {error ? <Text className="mb-2 text-caption text-danger">{error}</Text> : null}
          <View className="gap-2">
            <Button label={requirements?.button ?? ""} onPress={onAccept} loading={busy} />
            <Button label="Not now" variant="text" onPress={handleClose} disabled={busy} />
          </View>
        </View>
      </View>
    </Modal>
  );
}
