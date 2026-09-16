import { useState } from "react";
import {
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  Text,
  View,
} from "react-native";
import { Button, Input, PressableScale } from "./ui";
import type { ExtensionFallback } from "../lib/types";
import { formatCost } from "../lib/task-utils";

// The supporter is standing in a shop with a price that does not fit the
// budget. Everything about this sheet is shaped by that: it is short, the
// amount is the first thing they touch, the reason is a tap rather than typing,
// and the fallback is chosen HERE rather than five minutes from now, because in
// five minutes they will be deciding under time pressure with no answer.
//
// Silence is a denial (server/extensions.go). That is why the fallback is
// mandatory and why the sheet says so plainly before they send.

/** The escape hatch. Not a stored slug — a mode the form enters, which then
 *  stores "other: <what they typed>". */
const OTHER = "other";

// Long enough for a sentence, short enough to stay one line on a phone. The
// server bounds it too; this is the keyboard-level version of the same rule.
const OTHER_MAX_LENGTH = 80;

export interface BudgetIncreaseSubmit {
  requestedCents: number;
  /** The preset slug, or "other: <text>". */
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
  /** The ordered presets from GET /tasks/:id/extensions. Empty while that is
   *  still loading, and the sheet then shows only "Other" rather than an
   *  invented list. */
  reasons: { value: string; label: string }[];
  onClose: () => void;
  onSubmit: (payload: BudgetIncreaseSubmit) => Promise<void>;
}

const FALLBACK_OPTIONS: { value: ExtensionFallback; label: string }[] = [
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

/**
 * One full-width selectable row.
 *
 * Composed from PressableScale rather than added as a Button variant
 * (DESIGN.md §7): these are a single-select list, not actions, and the labels
 * are long enough that a Pill row would wrap badly. Selected state uses the
 * same brandTint + brand pairing §5 specifies for a selected Pill, so it reads
 * as the same idea at a different width.
 */
function ChoiceRow({
  label,
  selected,
  onPress,
}: {
  label: string;
  selected: boolean;
  onPress: () => void;
}) {
  return (
    <PressableScale
      onPress={onPress}
      accessibilityRole="radio"
      accessibilityState={{ selected }}
      // min-h-[44px], not min-h-11: tailwind.config.js REPLACES the spacing
      // scale with the token set (0/1/2/3/4/6/8/12), so `11` generates no class
      // at all — the same trap CancelTaskSheet documented for `mt-5`. DESIGN.md
      // §3's 44×44 minimum has to be spelled out to actually apply.
      className={`min-h-[44px] flex-row items-center gap-3 rounded-sm border px-3 py-3 ${
        selected ? "border-brand bg-brand-tint" : "border-line bg-surface"
      }`}
    >
      <View
        className={`h-4 w-4 items-center justify-center rounded-pill border ${
          selected ? "border-brand" : "border-line"
        }`}
      >
        {selected ? <View className="h-2 w-2 rounded-pill bg-brand" /> : null}
      </View>
      <Text className={`flex-1 text-caption ${selected ? "text-brand" : "text-ink"}`}>{label}</Text>
    </PressableScale>
  );
}

export function BudgetIncreaseSheet({
  visible,
  approvedBudgetCents,
  timeoutMinutes,
  reasons,
  onClose,
  onSubmit,
}: BudgetIncreaseSheetProps) {
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState<string | null>(null);
  const [otherText, setOtherText] = useState("");
  const [fallback, setFallback] = useState<ExtensionFallback | null>(null);
  const [fallbackNote, setFallbackNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setAmount("");
    setReason(null);
    setOtherText("");
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
    if (!reason) {
      setError("Pick a reason.");
      return;
    }
    if (reason === OTHER && !otherText.trim()) {
      setError("Say briefly what happened.");
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
        // The stored shape: a preset slug, or "other: <what they typed>". The
        // server maps it back to a sentence for the requester's approval card.
        reason: reason === OTHER ? `other: ${otherText.trim()}` : reason,
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
            {/* The preset list makes this taller than one screen on a small
                phone, and a sheet whose primary button is below the fold is not
                a sheet. Bounded so it still reads as one. */}
            <ScrollView className="max-h-[520px]" showsVerticalScrollIndicator={false}>
              <Text className="mb-1 text-title font-semibold text-ink">Ask for more budget</Text>
              <Text className="mb-6 text-caption text-muted">
                Approved so far: {formatCost(approvedBudgetCents)}. Ask for what you need on top of
                that — they can approve it in one tap.
              </Text>

              <View className="mb-6">
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

              {/* A closed set, not a text box. Free text on a phone with a
                  five-minute timer running is a field people leave empty, and
                  an empty reason makes the requester's one-tap approval a
                  guess. */}
              <Text className="mb-2 text-caption font-semibold text-muted">Why?</Text>
              <View className="mb-6 gap-2">
                {[...reasons, { value: OTHER, label: "Other" }].map((option) => (
                  <ChoiceRow
                    key={option.value}
                    label={option.label}
                    selected={reason === option.value}
                    onPress={() => setReason(option.value)}
                  />
                ))}
                {reason === OTHER ? (
                  <Input
                    value={otherText}
                    onChangeText={setOtherText}
                    placeholder="What happened?"
                    maxLength={OTHER_MAX_LENGTH}
                  />
                ) : null}
              </View>

              {/* FALLBACK, not an action — grouped under its own label so it
                  reads as one choice with two options rather than as more
                  buttons competing with the submit below. */}
              <Text className="mb-2 text-caption font-semibold text-muted">
                If there's no answer in {timeoutMinutes} minutes
              </Text>
              <View className="gap-2">
                {FALLBACK_OPTIONS.map((option) => (
                  <ChoiceRow
                    key={option.value}
                    label={option.label}
                    selected={fallback === option.value}
                    onPress={() => setFallback(option.value)}
                  />
                ))}
                {fallback === "buy_alternative" ? (
                  <Input
                    value={fallbackNote}
                    onChangeText={setFallbackNote}
                    placeholder="Which alternative?"
                  />
                ) : null}
              </View>
              <Text className="mt-2 text-caption text-muted">
                No answer counts as a no, so this is what you'll do.
              </Text>

              {error ? <Text className="mt-4 text-caption text-danger">{error}</Text> : null}
            </ScrollView>

            {/* THE submit, and visibly the only one on the sheet. */}
            <View className="mt-6 gap-2">
              <Button label="Send request" onPress={handleSubmit} loading={submitting} />
              <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
