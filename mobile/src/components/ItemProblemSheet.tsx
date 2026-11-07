import { useState } from "react";
import { Image, KeyboardAvoidingView, Modal, Platform, Pressable, ScrollView, Text, View } from "react-native";
import * as ImagePicker from "expo-image-picker";
import { Button, Input } from "./ui";
import { ChoiceRow } from "./BudgetIncreaseSheet";
import { formatCost } from "../lib/task-utils";
import type { FilePart } from "../lib/api";

// "Problem with an item" on a shopping task (server/item.go). The supporter is
// in a shop and something is out of stock. Three cases, and the sheet is the
// fork between them:
//
//   alternative  found one that fits the budget → the requester is asked
//                "1 for <alternative>, 2 to skip it"
//   none         nothing similar → the requester is told it's skipped
//   over         the alternative costs more than the budget → that is a
//                budget request, so the sheet hands over to it

type Outcome = "alternative" | "none" | "over";

export interface ItemProblemSubmit {
  itemName: string;
  storeName: string;
  outcome: "alternative" | "none";
  alternativeName?: string;
  alternativeCents?: number;
  photo?: FilePart | null;
}

const OUTCOMES: { value: Outcome; label: string }[] = [
  { value: "alternative", label: "Found an alternative within the budget" },
  { value: "none", label: "Nothing similar: skip it" },
  { value: "over", label: "The alternative costs more than the budget" },
];

// Dollars in, cents out, at the boundary where a human typed it (S-05).
// Zero is allowed: a free sample swap is still a swap.
function parseDollars(input: string): number | null {
  const cleaned = input.trim().replace(/[^0-9.]/g, "");
  if (cleaned === "") return null;
  const value = Number(cleaned);
  if (!Number.isFinite(value) || value < 0) return null;
  return Math.round(value * 100);
}

export function ItemProblemSheet({
  visible,
  approvedBudgetCents,
  timeoutMinutes,
  onClose,
  onSubmit,
  onNeedMoreBudget,
}: {
  visible: boolean;
  approvedBudgetCents: number;
  timeoutMinutes: number;
  onClose: () => void;
  onSubmit: (payload: ItemProblemSubmit) => Promise<void>;
  /** Case 3: close this sheet and open the budget request. */
  onNeedMoreBudget: () => void;
}) {
  const [item, setItem] = useState("");
  const [store, setStore] = useState("");
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [alt, setAlt] = useState("");
  const [price, setPrice] = useState("");
  const [photo, setPhoto] = useState<FilePart | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setItem("");
    setStore("");
    setOutcome(null);
    setAlt("");
    setPrice("");
    setPhoto(null);
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function takePhoto() {
    const perm = await ImagePicker.requestCameraPermissionsAsync();
    if (!perm.granted) {
      setError("Camera access is off. Enable it in Settings to take a photo.");
      return;
    }
    const result = await ImagePicker.launchCameraAsync({ quality: 0.7 });
    if (!result.canceled && result.assets[0]) {
      const a = result.assets[0];
      setPhoto({ uri: a.uri, mimeType: a.mimeType, fileName: a.fileName });
      setError(null);
    }
  }

  async function handleSubmit() {
    if (!item.trim()) {
      setError("Say which item is out of stock.");
      return;
    }
    if (!outcome) {
      setError("Choose what you found.");
      return;
    }
    if (outcome === "over") {
      reset();
      onNeedMoreBudget();
      return;
    }
    let cents: number | undefined;
    if (outcome === "alternative") {
      if (!alt.trim()) {
        setError("Name the alternative you found.");
        return;
      }
      const parsed = parseDollars(price);
      if (parsed === null) {
        setError("Enter the alternative's price.");
        return;
      }
      if (parsed > approvedBudgetCents) {
        setError(`That's more than the ${formatCost(approvedBudgetCents)} budget. Choose "costs more than the budget" to ask for more.`);
        return;
      }
      cents = parsed;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        itemName: item.trim(),
        storeName: store.trim(),
        outcome,
        alternativeName: outcome === "alternative" ? alt.trim() : undefined,
        alternativeCents: cents,
        photo: outcome === "alternative" ? photo : null,
      });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't send it. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  const submitLabel = outcome === "over" ? "Ask for more budget" : outcome === "none" ? "Tell them" : "Ask them";

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
          <Pressable className="rounded-t-card bg-surface p-6 pb-8" onPress={(e) => e.stopPropagation()}>
            <ScrollView className="max-h-[520px]" showsVerticalScrollIndicator={false}>
              <Text className="mb-1 text-title font-semibold text-ink">Problem with an item</Text>
              <Text className="mb-6 text-caption text-muted">
                Budget: {formatCost(approvedBudgetCents)}. You never spend more than that without their yes.
              </Text>

              <View className="mb-4 gap-3">
                <Input label="What's out of stock?" value={item} onChangeText={setItem} placeholder="Oat milk" maxLength={80} />
                <Input label="Store (optional)" value={store} onChangeText={setStore} placeholder="Trader Joe's" maxLength={60} />
              </View>

              <View className="mb-4 gap-2">
                {OUTCOMES.map((o) => (
                  <ChoiceRow key={o.value} label={o.label} selected={outcome === o.value} onPress={() => setOutcome(o.value)} />
                ))}
              </View>

              {outcome === "alternative" ? (
                <View className="mb-2 gap-3">
                  <Input label="The alternative" value={alt} onChangeText={setAlt} placeholder="Almond milk" maxLength={80} />
                  <Input
                    label="Its price"
                    value={price}
                    onChangeText={setPrice}
                    placeholder="0.00"
                    keyboardType="decimal-pad"
                    inputMode="decimal"
                  />
                  {photo ? (
                    <View className="flex-row items-center gap-3">
                      <Image source={{ uri: photo.uri }} className="h-[64px] w-[64px] rounded-sm" />
                      <Button label="Remove photo" variant="text" onPress={() => setPhoto(null)} />
                    </View>
                  ) : (
                    <Button label="Add a photo (optional)" variant="secondary" onPress={takePhoto} />
                  )}
                  <Text className="text-caption text-muted">
                    They reply 1 for the alternative or 2 to skip it. No answer in {timeoutMinutes || 5} minutes: it&apos;s
                    your call, using what they asked for.
                  </Text>
                </View>
              ) : null}

              {outcome === "none" ? (
                <Text className="mb-2 text-caption text-muted">They&apos;ll be told you&apos;re skipping it. Nothing to answer.</Text>
              ) : null}

              {error ? <Text className="mt-4 text-caption text-danger">{error}</Text> : null}
            </ScrollView>

            <View className="mt-6 gap-2">
              <Button label={submitLabel} onPress={handleSubmit} loading={submitting} />
              <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
