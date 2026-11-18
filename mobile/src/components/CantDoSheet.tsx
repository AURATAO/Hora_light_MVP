import { useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { Button, Input } from "./ui";
import { ChoiceRow } from "./BudgetIncreaseSheet";
import { buildCantDoPayload, type CantDoCopy, type CantDoPayload } from "../lib/cant-do";

// "I can't do this task" (lib/cant-do.ts, server/cant_do.go).
//
// Two questions and an optional line, then one button. Every word is the
// server's. Sending it changes nothing on the task: it tells HO:RA, and the
// sheet says so before and after, so nobody presses it expecting to be
// released and walks away.

export interface CantDoResult {
  title: string;
  body: string;
}

export function CantDoSheet({
  visible,
  copy,
  onClose,
  onSubmit,
}: {
  visible: boolean;
  copy: CantDoCopy | null;
  onClose: () => void;
  /** Resolves with the server's confirmation; throws with a message to show. */
  onSubmit: (payload: CantDoPayload) => Promise<CantDoResult>;
}) {
  const { height } = useWindowDimensions();
  const [reason, setReason] = useState<string | null>(null);
  const [bought, setBought] = useState<boolean | null>(null);
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<CantDoResult | null>(null);

  function reset() {
    setReason(null);
    setBought(null);
    setNote("");
    setSubmitting(false);
    setError(null);
    setDone(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleSubmit() {
    const built = buildCantDoPayload(reason, bought, note);
    if ("error" in built) {
      setError(built.error);
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      setDone(await onSubmit(built.payload));
    } catch (e) {
      setError(e instanceof Error ? e.message : "That didn't reach us. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible && copy !== null} transparent animationType="slide" onRequestClose={handleClose}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <View className="flex-1 justify-end">
          {/* A sibling of the sheet, never its parent (test/sheets-a11y): a
              wrapping Pressable would collapse every choice into one
              VoiceOver element. */}
          <Pressable
            className="absolute inset-0 bg-ink/40"
            onPress={handleClose}
            accessibilityRole="button"
            accessibilityLabel="Close"
          />
          <View className="rounded-t-card bg-surface p-6 pb-8" style={{ maxHeight: height * 0.88 }}>
            {done ? (
              <>
                <Text className="mb-2 text-title font-semibold text-ink" accessibilityRole="header">
                  {done.title}
                </Text>
                <Text className="mb-6 text-body text-ink">{done.body}</Text>
                <Button label="Close" onPress={handleClose} />
              </>
            ) : (
              <>
                <Text className="mb-1 text-title font-semibold text-ink" accessibilityRole="header">
                  {copy?.heading}
                </Text>
                {copy?.intro ? <Text className="mb-4 text-caption text-muted">{copy.intro}</Text> : <View className="mb-4" />}

                {/* The questions scroll; the buttons do not. */}
                <ScrollView className="mb-4" showsVerticalScrollIndicator keyboardShouldPersistTaps="handled">
                  <View className="mb-6 gap-2" accessibilityRole="radiogroup">
                    {(copy?.reasons ?? []).map((r) => (
                      <ChoiceRow
                        key={r.code}
                        label={r.label}
                        selected={reason === r.code}
                        onPress={() => {
                          setReason(r.code);
                          setError(null);
                        }}
                      />
                    ))}
                  </View>

                  <Text className="mb-2 text-body font-semibold text-ink">{copy?.boughtQuestion}</Text>
                  <View className="mb-6 gap-2" accessibilityRole="radiogroup">
                    <ChoiceRow
                      label={copy?.boughtYes ?? ""}
                      selected={bought === true}
                      onPress={() => {
                        setBought(true);
                        setError(null);
                      }}
                    />
                    <ChoiceRow
                      label={copy?.boughtNo ?? ""}
                      selected={bought === false}
                      onPress={() => {
                        setBought(false);
                        setError(null);
                      }}
                    />
                  </View>

                  <Input
                    value={note}
                    onChangeText={setNote}
                    placeholder={copy?.notePlaceholder || undefined}
                    maxLength={500}
                    multiline
                  />
                </ScrollView>

                {error ? <Text className="mb-2 text-caption text-danger">{error}</Text> : null}
                <View className="gap-2">
                  <Button label={copy?.submit ?? ""} onPress={handleSubmit} loading={submitting} />
                  <Button label="Not now" variant="text" onPress={handleClose} disabled={submitting} />
                </View>
              </>
            )}
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}
