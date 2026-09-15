import { useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Button, Input, Pill } from "./ui";
import { holdReleasedMessage } from "../lib/payment-copy";
import type { CancelTaskResult } from "../lib/api";

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
  /**
   * "Your reserved $76.75 will be released immediately." — named BEFORE they
   * commit, not after. Null on every task with no hold, and the sheet then
   * says nothing about money.
   */
  willReleaseMessage?: string | null;
  onClose: () => void;
  /**
   * Resolves with the cancel result so this sheet can report what happened to
   * the money. The cancel path has always released the hold correctly and has
   * never said so, which left requesters watching a reservation sit on their
   * statement with no idea it had already been reversed.
   */
  onConfirm: (reason: string) => Promise<CancelTaskResult | void>;
}

export function CancelTaskSheet({
  visible,
  willReleaseMessage,
  onClose,
  onConfirm,
}: CancelTaskSheetProps) {
  const [selected, setSelected] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Set once the cancel succeeds. The sheet stays open on this state rather
  // than closing, because the release confirmation is the last thing the
  // requester needs and a sheet that vanishes takes it with it.
  const [releasedMessage, setReleasedMessage] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  function reset() {
    setSelected(null);
    setNote("");
    setSubmitting(false);
    setError(null);
    setReleasedMessage(null);
    setDone(false);
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
      const result = await onConfirm(reason);
      setReleasedMessage(holdReleasedMessage(result ?? null));
      setDone(true);
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
            {done ? (
              <>
                <Text className="mb-1 text-title font-semibold text-ink">Task cancelled</Text>
                {/* What happened to the money, in the server's own numbers.
                    Absent on a task that never had a hold — the sheet then
                    says nothing about money rather than "$0.00 released". */}
                {releasedMessage ? (
                  <Text className="mb-4 text-caption text-muted">{releasedMessage}</Text>
                ) : (
                  <Text className="mb-4 text-caption text-muted">
                    Your supporter has been told.
                  </Text>
                )}
                <View className="mt-2 gap-2">
                  <Button label="Done" onPress={handleClose} />
                </View>
              </>
            ) : (
              <>
                <Text className="mb-1 text-title font-semibold text-ink">Cancel this task?</Text>
                <Text className="mb-4 text-caption text-muted">
                  Let the supporter know why — this helps us improve matching.
                </Text>
                {/* Named before they commit. The cancel is correct either way;
                    this is the difference between trusting it and not. */}
                {willReleaseMessage ? (
                  <Text className="mb-4 text-caption text-ink">{willReleaseMessage}</Text>
                ) : null}
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
                {/* The gap belongs to the action area, not to whatever sits
                    above it, so the pills-only state gets the same breathing
                    room as the state with the Other note field. mt-5 was doing
                    nothing at all here: tailwind.config.js replaces the spacing
                    scale with the token set (0/1/2/3/4/6/8/12), so `5`
                    generates no class and the buttons sat flush against the
                    content. 6 = space[6], matching CompleteTaskSheet's footer,
                    the sheet this one is shaped like. */}
                <View className="mt-6 gap-2">
                  <Button
                    label="Cancel task"
                    onPress={handleConfirm}
                    loading={submitting}
                    disabled={!selected}
                    className="bg-danger"
                  />
                  <Button
                    label="Never mind"
                    variant="text"
                    onPress={handleClose}
                    disabled={submitting}
                  />
                </View>
              </>
            )}
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
