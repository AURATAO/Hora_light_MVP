import { useEffect, useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Button, Input, Pill } from "./ui";
import { getReportReasons, reportUser, type ReportReason } from "../lib/api";
import { FALLBACK_REPORT_REASONS, REPORT_SENT_BODY, safetyTargetName } from "../lib/safety";

// Report a user (App Store Guideline 1.2). Shaped like CancelTaskSheet: server
// presets as pills, an optional note that only the HO:RA team reads, and a
// confirmation that stays on screen instead of vanishing with the sheet. The
// backdrop is a SIBLING Pressable, never a wrapper — see sheets-a11y.test.mjs.

export interface ReportUserSheetProps {
  visible: boolean;
  userId: string;
  userName?: string | null;
  taskId: string;
  onClose: () => void;
}

const DETAILS_MAX = 1000;

export function ReportUserSheet({ visible, userId, userName, taskId, onClose }: ReportUserSheetProps) {
  const [reasons, setReasons] = useState<ReportReason[]>(FALLBACK_REPORT_REASONS);
  const [selected, setSelected] = useState<string | null>(null);
  const [details, setDetails] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  useEffect(() => {
    if (!visible) return;
    let live = true;
    getReportReasons()
      .then((r) => {
        if (live && r.reasons?.length) setReasons(r.reasons);
      })
      .catch(() => {
        // The fallback list stays; a report must never be blocked on this.
      });
    return () => {
      live = false;
    };
  }, [visible]);

  function reset() {
    setSelected(null);
    setDetails("");
    setSubmitting(false);
    setError(null);
    setDone(false);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function handleSubmit() {
    if (!selected) return;
    setSubmitting(true);
    setError(null);
    try {
      await reportUser(userId, taskId, selected, details.trim() || undefined);
      setDone(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't send your report. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  const name = safetyTargetName(userName);

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <View className="flex-1 justify-end">
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
                  Report sent
                </Text>
                <Text className="mb-4 text-caption text-muted">{REPORT_SENT_BODY}</Text>
                <Button label="Done" onPress={handleClose} />
              </>
            ) : (
              <>
                <Text className="mb-1 text-title font-semibold text-ink" accessibilityRole="header">
                  Report {name}
                </Text>
                <Text className="mb-3 text-caption text-muted">
                  What happened? {name === "this person" ? "They" : name} won't be told who reported them.
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
                <Input
                  className="mt-3 max-h-[90px]"
                  value={details}
                  onChangeText={(t) => setDetails(t.slice(0, DETAILS_MAX))}
                  placeholder="Anything else we should know? (optional, only the HO:RA team sees this)"
                  multiline
                  numberOfLines={3}
                  textAlignVertical="top"
                />
                {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
                <View className="mt-6 gap-2">
                  <Button
                    label="Send report"
                    onPress={handleSubmit}
                    loading={submitting}
                    disabled={!selected}
                    className="bg-danger"
                  />
                  <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
                </View>
              </>
            )}
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}
