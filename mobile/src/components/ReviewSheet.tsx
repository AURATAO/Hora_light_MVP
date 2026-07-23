import { useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Star } from "lucide-react-native";
import { Button, Input, Pill, PressableScale } from "./ui";
import type { ValueRating } from "../lib/types";
import { color, size } from "../theme/tokens";

const VALUE_OPTIONS: { value: ValueRating; label: string }[] = [
  { value: "not_worth", label: "Not worth it" },
  { value: "fair", label: "Fair price" },
  { value: "great", label: "Great value" },
];

export interface ReviewSubmitPayload {
  stars: number;
  value_rating?: ValueRating;
  would_rehire?: boolean;
  comment?: string;
}

export interface ReviewSheetProps {
  visible: boolean;
  onClose: () => void;
  onSubmit: (payload: ReviewSubmitPayload) => Promise<void>;
}

export function ReviewSheet({ visible, onClose, onSubmit }: ReviewSheetProps) {
  const [stars, setStars] = useState(0);
  const [valueRating, setValueRating] = useState<ValueRating | null>(null);
  const [wouldRehire, setWouldRehire] = useState<boolean | null>(null);
  const [comment, setComment] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setStars(0);
    setValueRating(null);
    setWouldRehire(null);
    setComment("");
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleSubmit() {
    if (stars === 0) {
      setError("Please select a star rating.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        stars,
        value_rating: valueRating ?? undefined,
        would_rehire: wouldRehire ?? undefined,
        comment: comment.trim() || undefined,
      });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't submit your review. Try again.");
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
          <Pressable className="rounded-t-card bg-surface p-6 pb-8" onPress={(e) => e.stopPropagation()}>
            <Text className="mb-4 text-title font-semibold text-ink">How did it go?</Text>

            <Text className="mb-2 text-caption text-muted">Overall experience</Text>
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

            <Text className="mb-2 text-caption text-muted">Value for money</Text>
            <View className="mb-4 flex-row flex-wrap gap-2">
              {VALUE_OPTIONS.map((opt) => (
                <Pill
                  key={opt.value}
                  label={opt.label}
                  selected={valueRating === opt.value}
                  onPress={() => setValueRating((v) => (v === opt.value ? null : opt.value))}
                />
              ))}
            </View>

            <Text className="mb-2 text-caption text-muted">Would you hire again?</Text>
            <View className="mb-4 flex-row gap-2">
              <Pill
                label="Yes, definitely"
                selected={wouldRehire === true}
                onPress={() => setWouldRehire((v) => (v === true ? null : true))}
              />
              <Pill
                label="Not really"
                selected={wouldRehire === false}
                onPress={() => setWouldRehire((v) => (v === false ? null : false))}
              />
            </View>

            <Input
              label="Comments (optional)"
              value={comment}
              onChangeText={setComment}
              placeholder="Great job, very punctual…"
              multiline
              numberOfLines={3}
              textAlignVertical="top"
            />

            {error ? <Text className="mt-2 text-caption text-danger">{error}</Text> : null}

            <View className="mt-4 gap-2">
              <Button label="Submit review" onPress={handleSubmit} loading={submitting} disabled={stars === 0} />
              <Button label="Skip for now" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
