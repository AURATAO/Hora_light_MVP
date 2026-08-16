import { useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Button, Input, Pill } from "./ui";

// Web's cancel flow (app/src/components/CancelTaskButton.jsx) is free-text
// only — no presets to mirror. These are mobile's own; kept as clean,
// groupable strings so cancel_reason stays analyzable across clients even
// though web never sends them itself.
const PRESET_REASONS = [
  "Changed my mind",
  "Posted by mistake",
  "Found another way",
  "Took too long to get accepted",
  "Other",
];

export interface CancelTaskSheetProps {
  visible: boolean;
  onClose: () => void;
  onConfirm: (reason: string) => Promise<void>;
}

export function CancelTaskSheet({ visible, onClose, onConfirm }: CancelTaskSheetProps) {
  const [selected, setSelected] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setSelected(null);
    setNote("");
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleConfirm() {
    if (!selected) return;
    const reason = selected === "Other" ? note.trim() || "Other" : selected;
    setSubmitting(true);
    setError(null);
    try {
      await onConfirm(reason);
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't cancel your task. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      {/* Lifts the sheet above the keyboard so the note field — and the primary
          button sitting below it — stay visible while typing. */}
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
          <Pressable
            className="rounded-t-card bg-surface p-6 pb-8"
            onPress={(e) => e.stopPropagation()}
          >
            <Text className="mb-1 text-title font-semibold text-ink">Cancel this task?</Text>
            <Text className="mb-4 text-caption text-muted">
              Let the supporter know why — this helps us improve matching.
            </Text>
            <View className="flex-row flex-wrap gap-2">
              {PRESET_REASONS.map((reason) => (
                <Pill
                  key={reason}
                  label={reason}
                  selected={selected === reason}
                  onPress={() => setSelected(reason)}
                />
              ))}
            </View>
            {selected === "Other" ? (
              <Input
                className="mt-3 max-h-[90px]"
                value={note}
                onChangeText={setNote}
                placeholder="Add a note (optional)"
                multiline
                numberOfLines={3}
                textAlignVertical="top"
              />
            ) : null}
            {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
            <View className="mt-5 gap-2">
              <Button
                label="Cancel task"
                onPress={handleConfirm}
                loading={submitting}
                disabled={!selected}
                className="bg-danger"
              />
              <Button label="Never mind" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
