import { useState } from "react";
import {
  Image,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  Text,
  View,
} from "react-native";
import * as ImagePicker from "expo-image-picker";
import { Button, Input, PressableScale } from "./ui";
import { formatCost } from "../lib/task-utils";

export interface CompleteTaskPayload {
  photoUri: string;
  photoMimeType?: string | null;
  photoFileName?: string | null;
  note: string;
  /**
   * Shopping settlement (Stripe Phase 2b). Both are undefined on a task with
   * no approved budget — there is nothing to account for — and both are sent
   * together on one that has. `receiptCents` of 0 is a real answer meaning
   * nothing was bought, and needs no photo.
   */
  receiptCents?: number;
  receiptPhoto?: PickedPhoto | null;
}

export interface CompleteTaskSheetProps {
  visible: boolean;
  /**
   * The currently approved shopping ceiling. Above zero turns on the receipt
   * step: the server REFUSES a completion on such a task that says nothing
   * about the receipt, so this is not decoration.
   */
  approvedBudgetCents?: number;
  /** BillingConfig.OverageToleranceCents, from the server. */
  toleranceCents?: number;
  onClose: () => void;
  onSubmit: (payload: CompleteTaskPayload) => Promise<void>;
}

interface PickedPhoto {
  uri: string;
  mimeType?: string | null;
  fileName?: string | null;
}

// Dollars in, cents out, at the boundary where a human typed it. Everything
// downstream is integer cents (S-05).
function parseDollarsToCents(input: string): number | null {
  const cleaned = input.trim().replace(/[^0-9.]/g, "");
  if (cleaned === "") return null;
  const value = Number(cleaned);
  if (!Number.isFinite(value) || value < 0) return null;
  return Math.round(value * 100);
}

export function CompleteTaskSheet({
  visible,
  approvedBudgetCents = 0,
  toleranceCents = 0,
  onClose,
  onSubmit,
}: CompleteTaskSheetProps) {
  const [photo, setPhoto] = useState<PickedPhoto | null>(null);
  const [note, setNote] = useState("");
  const [receiptAmount, setReceiptAmount] = useState("");
  const [receiptPhoto, setReceiptPhoto] = useState<PickedPhoto | null>(null);
  const [nothingBought, setNothingBought] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const needsReceipt = approvedBudgetCents > 0;
  const maxReceiptCents = approvedBudgetCents + toleranceCents;

  function reset() {
    setPhoto(null);
    setNote("");
    setReceiptAmount("");
    setReceiptPhoto(null);
    setNothingBought(false);
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  // One pair of pickers for both photos. The receipt is captured exactly the
  // way the completion photo is and uploaded through the same endpoint and
  // bucket — a second storage path for the same kind of file, taken on the
  // same phone, in the same flow, would be two things to keep working.
  async function pickFromCamera(set: (p: PickedPhoto) => void) {
    const perm = await ImagePicker.requestCameraPermissionsAsync();
    if (!perm.granted) {
      setError("Camera access is off. Enable it in Settings to take a photo.");
      return;
    }
    const result = await ImagePicker.launchCameraAsync({ quality: 0.7 });
    if (!result.canceled && result.assets[0]) {
      const asset = result.assets[0];
      set({
        uri: asset.uri,
        mimeType: asset.mimeType,
        fileName: asset.fileName,
      });
      setError(null);
    }
  }

  async function pickFromLibrary(set: (p: PickedPhoto) => void) {
    const perm = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (!perm.granted) {
      setError("Photo library access is off. Enable it in Settings to choose a photo.");
      return;
    }
    const result = await ImagePicker.launchImageLibraryAsync({
      quality: 0.7,
      mediaTypes: ["images"],
    });
    if (!result.canceled && result.assets[0]) {
      const asset = result.assets[0];
      set({
        uri: asset.uri,
        mimeType: asset.mimeType,
        fileName: asset.fileName,
      });
      setError(null);
    }
  }

  async function handleSubmit() {
    if (!photo) {
      setError("Add a photo to complete this task.");
      return;
    }

    // The receipt half, validated here only so the supporter is not sent on a
    // round trip to be told something obvious. The server checks all of it
    // again and is the authority — above all on the budget ceiling, which it
    // may have raised since this screen loaded.
    let receiptCents: number | undefined;
    if (needsReceipt) {
      if (nothingBought) {
        receiptCents = 0;
      } else {
        const parsed = parseDollarsToCents(receiptAmount);
        if (parsed === null) {
          setError("Enter what the receipt came to, or say nothing was bought.");
          return;
        }
        receiptCents = parsed;
      }
      if (receiptCents > 0 && !receiptPhoto) {
        setError("Add a photo of the receipt.");
        return;
      }
      if (receiptCents > maxReceiptCents) {
        setError(
          `That's over the approved budget. Ask for a budget increase first, or correct the amount — the most you can claim is ${formatCost(maxReceiptCents)}.`,
        );
        return;
      }
    }

    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        photoUri: photo.uri,
        photoMimeType: photo.mimeType,
        photoFileName: photo.fileName,
        note: note.trim(),
        receiptCents,
        receiptPhoto: receiptCents ? receiptPhoto : null,
      });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't complete this task. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      {/* Lifts the sheet above the keyboard so the note field — and the primary
          button sitting below it — stay visible while typing. */}
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
          <Pressable
            className="rounded-t-card bg-surface p-6 pb-8"
            onPress={(e) => e.stopPropagation()}
          >
            {/* The receipt step makes this sheet taller than one screen on a
                small phone, and a sheet whose primary button is below the fold
                with no way to reach it is not a sheet. Bounded so it still
                reads as a sheet rather than a screen. */}
            <ScrollView className="max-h-[560px]" showsVerticalScrollIndicator={false}>
              <Text className="mb-1 text-title font-semibold text-ink">Complete this task</Text>
              <Text className="mb-4 text-caption text-muted">
                Add a photo so the requester can see it's done.
              </Text>

              {photo ? (
                <View className="mb-4 gap-2">
                  <Image
                    source={{ uri: photo.uri }}
                    className="h-[180px] w-full rounded-sm"
                    resizeMode="cover"
                  />
                  <PressableScale onPress={() => setPhoto(null)} hitSlop={8} className="self-start">
                    <Text className="text-caption font-semibold text-brand">Change photo</Text>
                  </PressableScale>
                </View>
              ) : (
                <View className="mb-4 gap-2">
                  <View className="flex-row gap-2">
                    <Button
                      label="Take photo"
                      variant="secondary"
                      onPress={() => pickFromCamera(setPhoto)}
                      className="flex-1"
                    />
                    <Button
                      label="Choose photo"
                      variant="secondary"
                      onPress={() => pickFromLibrary(setPhoto)}
                      className="flex-1"
                    />
                  </View>
                  <Text className="text-caption text-muted">
                    A photo is required to complete this task.
                  </Text>
                </View>
              )}

              <Input
                value={note}
                onChangeText={setNote}
                placeholder="Add a note (optional)"
                multiline
                numberOfLines={3}
                textAlignVertical="top"
                className="max-h-[90px]"
              />

              {/* Shopping settlement. Only on a task with an approved budget —
                  everywhere else this section does not exist, because there is
                  nothing to account for. */}
              {needsReceipt ? (
                <View className="mt-6 border-t border-line pt-6">
                  <Text className="mb-1 text-title font-semibold text-ink">
                    What did it come to?
                  </Text>
                  <Text className="mb-4 text-caption text-muted">
                    Approved budget {formatCost(approvedBudgetCents)}. You're reimbursed the receipt
                    total, up to {formatCost(maxReceiptCents)}.
                  </Text>

                  {nothingBought ? (
                    <PressableScale
                      onPress={() => setNothingBought(false)}
                      className="min-h-11 justify-center"
                    >
                      <Text className="text-body text-ink">Nothing was bought.</Text>
                      <Text className="text-caption font-semibold text-brand">
                        Enter a receipt instead
                      </Text>
                    </PressableScale>
                  ) : (
                    <>
                      <Input
                        label="Receipt total"
                        value={receiptAmount}
                        onChangeText={setReceiptAmount}
                        placeholder="0.00"
                        keyboardType="decimal-pad"
                        inputMode="decimal"
                      />
                      <PressableScale
                        onPress={() => setNothingBought(true)}
                        hitSlop={8}
                        className="mt-2 min-h-11 justify-center"
                      >
                        <Text className="text-caption font-semibold text-brand">
                          I didn't buy anything
                        </Text>
                      </PressableScale>

                      {receiptPhoto ? (
                        <View className="mt-4 gap-2">
                          <Image
                            source={{ uri: receiptPhoto.uri }}
                            className="h-[180px] w-full rounded-sm"
                            resizeMode="cover"
                          />
                          <PressableScale
                            onPress={() => setReceiptPhoto(null)}
                            hitSlop={8}
                            className="self-start"
                          >
                            <Text className="text-caption font-semibold text-brand">
                              Change receipt photo
                            </Text>
                          </PressableScale>
                        </View>
                      ) : (
                        <View className="mt-4 flex-row gap-2">
                          <Button
                            label="Photo of receipt"
                            variant="secondary"
                            onPress={() => pickFromCamera(setReceiptPhoto)}
                            className="flex-1"
                          />
                          <Button
                            label="Choose photo"
                            variant="secondary"
                            onPress={() => pickFromLibrary(setReceiptPhoto)}
                            className="flex-1"
                          />
                        </View>
                      )}
                    </>
                  )}
                </View>
              ) : null}

              {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
            </ScrollView>

            <View className="mt-6 gap-2">
              {submitting ? (
                <Text className="text-center text-caption text-muted">Uploading photo…</Text>
              ) : null}
              {/* Validating on press rather than disabling (DESIGN.md §5): with
                  a receipt to fill in there are now several reasons this could
                  be incomplete, and a dead button names none of them. */}
              <Button label="Complete task" onPress={handleSubmit} loading={submitting} />
              <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
