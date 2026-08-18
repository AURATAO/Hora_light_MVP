import { useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, ScrollView, Text, View, useWindowDimensions } from "react-native";
import { Star } from "lucide-react-native";
import { Button, Input, Pill, PressableScale } from "./ui";
import { TRACTION_3_CONFIG } from "../lib/beta-notice";
import type { EaseRating, RaterRole, WouldUseAgain } from "../lib/types";
import { color, size, space } from "../theme/tokens";

// Labels live here and only here — the payload carries slugs, so re-wording a
// question never touches stored data (server/main.go validEaseRatings et al.).
const EASE_OPTIONS: { value: EaseRating; label: string }[] = [
  { value: "very_easy", label: "Very easy" },
  { value: "easy", label: "Easy" },
  { value: "neutral", label: "Neither easy nor difficult" },
  { value: "difficult", label: "Difficult" },
  { value: "very_difficult", label: "Very difficult" },
];

const USE_AGAIN_OPTIONS: { value: WouldUseAgain; label: string }[] = [
  { value: "yes", label: "Yes" },
  { value: "maybe_task", label: "Maybe, depending on the task" },
  { value: "maybe_cost", label: "Maybe, depending on the final cost" },
  { value: "no", label: "No" },
];

const rate = TRACTION_3_CONFIG.perMinuteRate;

// Both roles answer the same two multiple-choice questions with the same slugs;
// only the wording differs, because "the flow" means something different from
// each side of a task.
const QUESTIONS: Record<RaterRole, { ease: string; useAgain: string; open: string }> = {
  requester: {
    ease: "How easy was it to create, follow and complete this mission through HO:RA?",
    useAgain: `HO:RA — ${rate} per minute. Would you use it for a local errand?`,
    open: "What is the one thing we should improve before the public launch?",
  },
  supporter: {
    ease: "How clear and easy was the complete mission flow, from acceptance to clock-out?",
    useAgain: `At ${rate}/min, would you take on tasks like this again as a supporter?`,
    open:
      "What is the one operational or app-related improvement that would help you " +
      "complete future missions more efficiently?",
  },
};

export interface TractionReviewSubmitPayload {
  /** Requester only — the supporter form has no star question. */
  stars?: number;
  ease_rating: EaseRating;
  would_use_again: WouldUseAgain;
  open_feedback?: string;
}

export interface TractionReviewSheetProps {
  visible: boolean;
  /**
   * Which side of the task is answering. Presentation only: the server derives
   * the stored rater_role from the caller's relation to the task and ignores
   * anything the client claims.
   */
  role: RaterRole;
  onClose: () => void;
  onSubmit: (payload: TractionReviewSubmitPayload) => Promise<void>;
}

const SHEET_MAX_HEIGHT_RATIO = 0.85;
const BODY_MAX_HEIGHT_RATIO = 0.7;

/**
 * The Traction 3 post-task questionnaire, replacing ReviewSheet for the length
 * of the round (see isTractionWindowActive). Skipping writes nothing at all —
 * no placeholder row — so the questionnaire can still be answered later.
 */
export function TractionReviewSheet({ visible, role, onClose, onSubmit }: TractionReviewSheetProps) {
  const { height } = useWindowDimensions();
  const copy = QUESTIONS[role];
  const isRequester = role === "requester";

  const [ease, setEase] = useState<EaseRating | null>(null);
  const [stars, setStars] = useState(0);
  const [useAgain, setUseAgain] = useState<WouldUseAgain | null>(null);
  const [openFeedback, setOpenFeedback] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setEase(null);
    setStars(0);
    setUseAgain(null);
    setOpenFeedback("");
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  // The open question is optional; everything else is required, which is what
  // keeps the CTA disabled rather than validating on press.
  const complete = !!ease && !!useAgain && (!isRequester || stars > 0);

  async function handleSubmit() {
    if (!complete) return;
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        stars: isRequester ? stars : undefined,
        ease_rating: ease,
        would_use_again: useAgain,
        open_feedback: openFeedback.trim() || undefined,
      });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't submit your answers. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      {/* Lifts the sheet above the keyboard so the open-text field stays
          visible while typing. */}
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <View className="flex-1 justify-end">
          {/* Backdrop as its own layer, not a Pressable wrapping the sheet: a
              ScrollView nested inside a pressable parent loses the responder
              negotiation on drag (see CompanionshipPolicySheet). */}
          <Pressable className="absolute inset-0 bg-ink/40" onPress={handleClose} />

          <View className="rounded-t-card bg-surface" style={{ maxHeight: height * SHEET_MAX_HEIGHT_RATIO }}>
            <View className="px-6 pb-4 pt-6">
              <Text className="text-title font-semibold text-ink">How did it go?</Text>
            </View>

            <ScrollView
              style={{ maxHeight: height * BODY_MAX_HEIGHT_RATIO, flexShrink: 1 }}
              keyboardShouldPersistTaps="handled"
              showsVerticalScrollIndicator
              contentContainerStyle={{ paddingHorizontal: space[6], paddingBottom: space[6] }}
            >
              <Text className="mb-2 text-caption text-muted">{copy.ease}</Text>
              <View className="mb-4 flex-row flex-wrap gap-2">
                {EASE_OPTIONS.map((opt) => (
                  <Pill
                    key={opt.value}
                    label={opt.label}
                    selected={ease === opt.value}
                    onPress={() => setEase((v) => (v === opt.value ? null : opt.value))}
                  />
                ))}
              </View>

              {isRequester ? (
                <>
                  <Text className="mb-2 text-caption text-muted">Rate your supporter</Text>
                  <View className="mb-4 flex-row gap-2">
                    {[1, 2, 3, 4, 5].map((s) => (
                      <PressableScale key={s} onPress={() => setStars(s)} hitSlop={4}>
                        <Star
                          color={color.ink}
                          fill={s <= stars ? color.ink : "none"}
                          size={28}
                          strokeWidth={size.iconStroke}
                        />
                      </PressableScale>
                    ))}
                  </View>
                </>
              ) : null}

              <Text className="mb-2 text-caption text-muted">{copy.useAgain}</Text>
              <View className="mb-4 flex-row flex-wrap gap-2">
                {USE_AGAIN_OPTIONS.map((opt) => (
                  <Pill
                    key={opt.value}
                    label={opt.label}
                    selected={useAgain === opt.value}
                    onPress={() => setUseAgain((v) => (v === opt.value ? null : opt.value))}
                  />
                ))}
              </View>

              <Input
                label={copy.open}
                value={openFeedback}
                onChangeText={setOpenFeedback}
                placeholder="Optional"
                multiline
                numberOfLines={3}
                textAlignVertical="top"
                className="max-h-[90px]"
              />

              {error ? <Text className="mt-2 text-caption text-danger">{error}</Text> : null}
            </ScrollView>

            <View className="border-t border-line px-6 pb-8 pt-4">
              <Button label="Submit" onPress={handleSubmit} loading={submitting} disabled={!complete} />
              <Button
                label="Skip for now"
                variant="text"
                onPress={handleClose}
                disabled={submitting}
                className="mt-2"
              />
            </View>
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}
