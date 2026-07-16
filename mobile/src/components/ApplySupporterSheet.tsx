import { useState } from "react";
import { Modal, Pressable, Text, View } from "react-native";
import { Button, Input } from "./ui";
import { applySupporter } from "../lib/api";

export interface ApplySupporterSheetProps {
  visible: boolean;
  onClose: () => void;
  onApplied: () => void;
}

export function ApplySupporterSheet({ visible, onClose, onApplied }: ApplySupporterSheetProps) {
  const [firstName, setFirstName] = useState("");
  const [lastName, setLastName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setFirstName("");
    setLastName("");
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleSubmit() {
    if (!firstName.trim() || !lastName.trim()) {
      setError("Enter your first and last name.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await applySupporter({ first_name: firstName.trim(), last_name: lastName.trim() });
      reset();
      onApplied();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't submit your application. Try again.");
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
        <Pressable className="rounded-t-card bg-surface p-6 pb-8" onPress={(e) => e.stopPropagation()}>
          <Text className="mb-1 text-title font-semibold text-ink">Become a supporter</Text>
          <Text className="mb-4 text-caption text-muted">
            Tell us your name to get started — we'll review your application.
          </Text>
          <View className="gap-3">
            <Input label="First name" value={firstName} onChangeText={setFirstName} placeholder="Jane" />
            <Input label="Last name" value={lastName} onChangeText={setLastName} placeholder="Doe" />
          </View>
          {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
          <View className="mt-5 gap-2">
            <Button
              label="Submit application"
              onPress={handleSubmit}
              loading={submitting}
              disabled={!firstName.trim() || !lastName.trim()}
            />
            <Button label="Never mind" variant="text" onPress={handleClose} disabled={submitting} />
          </View>
        </Pressable>
      </Pressable>
    </Modal>
  );
}
