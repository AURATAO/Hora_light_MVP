import { useEffect, useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Button, Input, Pill } from "./ui";
import {
  cancelChargeLine,
  cancelGraceCountdown,
  cancelReleaseLine,
  holdReleasedMessage,
} from "../lib/payment-copy";
import { formatCost } from "../lib/task-utils";
import type { CancelTaskResult } from "../lib/api";
import type { TaskCancellation, TaskPayment } from "../lib/types";

// The reason presets come from the SERVER now (task.cancellation.reasons),
// not from a hardcoded list here. They used to be mobile's own, with web
// running a free-text box beside them — which is precisely why a cancellation
// reason could never be relayed to the supporter: there was no way to tell
// "plans changed" from something an ops admin typed about them.
//
// This fallback covers the one case where the sheet opens without them: the
// task fetch failed, and a cancel must never be blocked by not knowing what it
// costs. The values match server/cancel_reasons.go.
const FALLBACK_REASONS = [
  { value: "plans_changed", label: "Plans changed" },
  { value: "no_longer_needed", label: "No longer needed" },
  { value: "found_another_way", label: "Found another way" },
  { value: "posted_by_mistake", label: "Posted by mistake" },
  { value: "other", label: "Other" },
];

export interface CancelTaskSheetProps {
  visible: boolean;
  /**
   * What cancelling right now would cost and release, priced by the server.
   * Null on a task whose detail fetch failed, and the sheet then says nothing
   * about money rather than guessing at it.
   */
  cancellation?: TaskCancellation | null;
  /** The hold standing against the task, for the release line. */
  payment?: TaskPayment | null;
  onClose: () => void;
  /**
   * Resolves with the cancel result so this sheet can report what happened to
   * the money. The cancel path has always released the hold correctly and has
   * never said so, which left requesters watching a reservation sit on their
   * statement with no idea it had already been reversed.
   */
  onConfirm: (reason: string, reasonCode: string) => Promise<CancelTaskResult | void>;
}

export function CancelTaskSheet({
  visible,
  cancellation,
  payment,
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
  const [result, setResult] = useState<CancelTaskResult | null>(null);
  const [done, setDone] = useState(false);
  // Ticks only while a grace countdown is on screen.
  const [now, setNow] = useState(() => Date.now());

  const graceEndsAt = cancellation?.grace_ends_at ?? null;
  const withinGrace = Boolean(graceEndsAt) && Date.parse(graceEndsAt as string) > now;
  const countdown = cancelGraceCountdown(graceEndsAt, now);

  useEffect(() => {
    if (!visible || done || !graceEndsAt) return undefined;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [visible, done, graceEndsAt]);

  const reasons = cancellation?.reasons?.length ? cancellation.reasons : FALLBACK_REASONS;
  const needsNote = selected === "other";

  // Recomputed against the live countdown rather than read off the fetch: the
  // sheet can sit open across the boundary, and a requester who reads "free"
  // and taps twenty seconds later must not be surprised by a charge.
  const chargeLine = cancelChargeLine(cancellation, { withinGrace });
  const releaseLine = cancelReleaseLine(cancellation, payment, { withinGrace });

  function reset() {
    setSelected(null);
    setNote("");
    setSubmitting(false);
    setError(null);
    setReleasedMessage(null);
    setResult(null);
    setDone(false);
    setNow(Date.now());
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleConfirm() {
    if (!selected) return;
    const chosen = reasons.find((r) => r.value === selected);
    // The free text is still sent: it is what the ops feed and the audit row
    // record. Only the CODE is ever relayed to the supporter — see
    // server/cancel_reasons.go.
    const reason = needsNote ? note.trim() || "Other" : chosen?.label ?? selected;
    setSubmitting(true);
    setError(null);
    try {
      const res = await onConfirm(reason, selected);
      setReleasedMessage(holdReleasedMessage(res ?? null));
      setResult((res as CancelTaskResult) ?? null);
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
        <View className="flex-1 justify-end">
          {/* The backdrop is its own layer, not a Pressable wrapping the sheet
              (the NamePromptSheet pattern). A Pressable is `accessible` by
              default, so wrapping the sheet in one collapsed the heading, the
              countdown, every reason pill, the note field and both buttons
              into a single VoiceOver element — a requester using a screen
              reader could not pick a reason, and so could not cancel. As a
              sibling it is one focusable "Close" control and every control
              inside the sheet is its own. */}
          <Pressable
            className="absolute inset-0 bg-ink/40"
            onPress={handleClose}
            accessibilityRole="button"
            accessibilityLabel="Close"
          />
          <View className="rounded-t-card bg-surface p-6 pb-8">
            {done ? (
              <>
                <Text className="mb-1 text-title font-semibold text-ink" accessibilityRole="header">
                  Task cancelled
                </Text>
                {/* What was charged, when anything was. Under the current
                    policy a cancelled task usually HAS a bill, and a
                    confirmation that mentions only the release would be
                    telling half the story. */}
                {result && (result.bill_cents ?? 0) > 0 ? (
                  <Text className="mb-1 text-caption text-ink">
                    Charged {formatCost(result.bill_cents ?? 0)} — paid to your supporter.
                  </Text>
                ) : null}
                {/* What happened to the rest, in the server's own numbers.
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
                <Text className="mb-1 text-title font-semibold text-ink" accessibilityRole="header">
                  Cancel this task?
                </Text>

                {/* THE LIVE COUNTDOWN. Inside the grace window this is the
                    whole message: the cancel is free right now and will not be
                    in a moment, and a requester deciding in that moment is
                    owed the seconds rather than a policy sentence. */}
                {withinGrace && countdown ? (
                  <View className="mb-4 rounded-card border border-line bg-bg p-3">
                    <Text className="text-caption text-ink">
                      Free cancellation for{" "}
                      <Text className="font-semibold">{countdown}</Text> more — after that, the{" "}
                      {/* The server's number, never a literal: the base fee
                          differs by category ($25 for companionship) and a
                          hardcoded $12 would quietly understate half of
                          them. */}
                      {cancellation?.base_fee_cents ? `${formatCost(cancellation.base_fee_cents)} ` : ""}
                      base fee goes to your supporter.
                    </Text>
                  </View>
                ) : null}

                {/* The numbers, not the rules. Null on a task nobody has
                    accepted, where cancelling has never cost anything. */}
                {chargeLine ? (
                  <Text className="mb-2 text-caption text-ink">{chargeLine}</Text>
                ) : (
                  <Text className="mb-2 text-caption text-muted">
                    Nobody has accepted this task yet, so cancelling costs nothing.
                  </Text>
                )}
                {releaseLine ? (
                  <Text className="mb-4 text-caption text-muted">{releaseLine}</Text>
                ) : (
                  <View className="mb-2" />
                )}

                <Text className="mb-2 text-caption text-muted">
                  Let the supporter know why — this helps us improve matching.
                </Text>
                <View className="flex-row flex-wrap gap-2">
                  {reasons.map((reason) => (
                    <Pill
                      key={reason.value}
                      label={reason.label}
                      selected={selected === reason.value}
                      onPress={() => setSelected(reason.value)}
                    />
                  ))}
                </View>
                {needsNote ? (
                  <Input
                    className="mt-3 max-h-[90px]"
                    value={note}
                    onChangeText={setNote}
                    placeholder="What happened? (only the HO:RA team sees this)"
                    multiline
                    numberOfLines={3}
                    textAlignVertical="top"
                  />
                ) : null}
                {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
                {/* The gap belongs to the action area, not to whatever sits
                    above it, so the pills-only state gets the same breathing
                    room as the state with the Other note field. 6 = space[6],
                    matching CompleteTaskSheet's footer, the sheet this one is
                    shaped like. */}
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
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}
