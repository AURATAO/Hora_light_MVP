import { useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Button, Input, Pill } from "./ui";
import type { ExtensionFallback } from "../lib/types";
import { formatCost } from "../lib/task-utils";

// The supporter is standing in a shop with a price that does not fit the
// budget. Everything about this sheet is shaped by that: it is three fields
// long, the amount is the first thing they touch, and the fallback is chosen
// HERE rather than five minutes from now, because in five minutes they will be
// deciding under time pressure with no answer to work from.
//
// Silence is a denial (server/extensions.go). That is why the fallback is
// mandatory and why the sheet says so plainly before they send.

export interface BudgetIncreaseSubmit {
  requestedCents: number;
  reason: string;
  fallback: ExtensionFallback;
  fallbackNote: string;
}

export interface BudgetIncreaseSheetProps {
  visible: boolean;
  /** The currently approved ceiling, so the ask is anchored to something real. */
  approvedBudgetCents: number;
  /** BillingConfig.ApprovalTimeoutMinutes, from the server — never hardcoded. */
  timeoutMinutes: number;
  onClose: () => void;
  onSubmit: (payload: BudgetIncreaseSubmit) => Promise<void>;
}

const FALLBACK_LABELS: { value: ExtensionFallback; label: string }[] = [
  { value: "buy_alternative", label: "Buy an alternative" },
  { value: "skip_item", label: "Skip this item" },
];

// Dollars in, cents out. The only place in this file that touches money as a
// float, at the boundary where a human typed it — everything downstream is
// integer cents (S-05).
function parseDollarsToCents(input: string): number | null {
  const cleaned = input.trim().replace(/[^0-9.]/g, "");
  if (cleaned === "") return null;
  const value = Number(cleaned);
  if (!Number.isFinite(value) || value <= 0) return null;
  return Math.round(value * 100);
}

export function BudgetIncreaseSheet({
  visible,
  approvedBudgetCents,
  timeoutMinutes,
  onClose,
  onSubmit,
}: BudgetIncreaseSheetProps) {
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [fallback, setFallback] = useState<ExtensionFallback | null>(null);
  const [fallbackNote, setFallbackNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setAmount("");
    setReason("");
    setFallback(null);
    setFallbackNote("");
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleSubmit() {
    const cents = parseDollarsToCents(amount);
    if (cents === null) {
      setError("Enter how much more you need.");
      return;
    }
    if (!fallback) {
      setError("Choose what to do if there's no answer.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        requestedCents: cents,
        reason: reason.trim(),
        fallback,
        fallbackNote: fallbackNote.trim(),
      });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't send your request. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  const cents = parseDollarsToCents(amount);

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
          <Pressable
            className="rounded-t-card bg-surface p-6 pb-8"
            onPress={(e) => e.stopPropagation()}
          >
            <Text className="mb-1 text-title font-semibold text-ink">Ask for more budget</Text>
            <Text className="mb-4 text-caption text-muted">
              Approved so far: {formatCost(approvedBudgetCents)}. Ask for what you need on top of
              that — they can approve it in one tap.
            </Text>

            <View className="mb-4">
              <Input
                label="How much more?"
                value={amount}
                onChangeText={setAmount}
                placeholder="0.00"
                keyboardType="decimal-pad"
                inputMode="decimal"
              />
              {cents !== null ? (
                <Text className="mt-1 text-caption text-muted">
                  New budget would be {formatCost(approvedBudgetCents + cents)}.
                </Text>
              ) : null}
            </View>

            <View className="mb-4">
              <Input
                label="Why? (optional)"
                value={reason}
                onChangeText={setReason}
                placeholder="Only the larger size was in stock"
                grow
              />
            </View>

            <Text className="mb-2 text-caption font-semibold text-muted">
              If there's no answer in {timeoutMinutes} minutes
            </Text>
            <View className="mb-2 flex-row flex-wrap gap-2">
              {FALLBACK_LABELS.map((option) => (
                <Pill
                  key={option.value}
                  label={option.label}
                  selected={fallback === option.value}
                  onPress={() => setFallback(option.value)}
                />
              ))}
            </View>
            <Text className="mb-4 text-caption text-muted">
              No answer counts as a no, so this is what you'll do.
            </Text>

            {fallback === "buy_alternative" ? (
              <View className="mb-4">
                <Input
                  label="Which alternative?"
                  value={fallbackNote}
                  onChangeText={setFallbackNote}
                  placeholder="The 500g jar instead"
                  grow
                />
              </View>
            ) : null}

            {error ? <Text className="mb-3 text-caption text-danger">{error}</Text> : null}

            <View className="gap-2">
              <Button label="Send request" onPress={handleSubmit} loading={submitting} />
              <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
