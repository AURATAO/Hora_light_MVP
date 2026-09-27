import { useEffect, useState } from "react";
import { Modal, Pressable, Text, View } from "react-native";
import { Button } from "./ui";
import { ApiError, deleteMyAccount, getAccountDeletionPreview, type DeletionBlocker } from "../lib/api";

// Delete account (App Store Guideline 5.1.1(v)). Two states: the server's
// list of what stands in the way (open tasks, a balance, a payout on its way
// to the bank), or the confirmation itself — which says exactly what is
// removed and what the platform has to keep. Same sheet shape as the others:
// the backdrop is a SIBLING Pressable (sheets-a11y.test.mjs).

export interface DeleteAccountSheetProps {
  visible: boolean;
  onClose: () => void;
  /** Called after the server confirms the deletion; the caller signs out. */
  onDeleted: () => Promise<void> | void;
}

/** What goes and what stays — the same sentences the server acts on
 *  (server/account_deletion.go). Exported so the copy can be tested. */
export const DELETION_REMOVES = [
  "Your name, phone number, city, photo and bio",
  "Your email and sign-in — you won't be able to sign in again",
  "Saved cards, your location history and notifications",
];
export const DELETION_KEEPS =
  "Records of completed tasks and their payments are kept for tax and dispute purposes, with your name replaced by “Deleted user”. Reviews you wrote and safety reports stay too.";

export function DeleteAccountSheet({ visible, onClose, onDeleted }: DeleteAccountSheetProps) {
  const [checking, setChecking] = useState(true);
  const [blockers, setBlockers] = useState<DeletionBlocker[]>([]);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!visible) return;
    let live = true;
    setChecking(true);
    setError(null);
    getAccountDeletionPreview()
      .then((r) => {
        if (live) setBlockers(r.blockers ?? []);
      })
      .catch((e) => {
        if (live) setError(e instanceof Error ? e.message : "Couldn't check your account. Try again.");
      })
      .finally(() => {
        if (live) setChecking(false);
      });
    return () => {
      live = false;
    };
  }, [visible]);

  function handleClose() {
    if (deleting) return;
    onClose();
  }

  async function handleDelete() {
    setDeleting(true);
    setError(null);
    try {
      await deleteMyAccount();
      await onDeleted();
    } catch (e) {
      if (e instanceof ApiError && Array.isArray((e.body as { blockers?: unknown })?.blockers)) {
        setBlockers((e.body as { blockers: DeletionBlocker[] }).blockers);
      } else {
        setError(e instanceof Error ? e.message : "Couldn't delete your account. Try again.");
      }
      setDeleting(false);
    }
  }

  const blocked = !checking && blockers.length > 0;

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <View className="flex-1 justify-end">
        <Pressable
          className="absolute inset-0 bg-ink/40"
          onPress={handleClose}
          accessibilityRole="button"
          accessibilityLabel="Close"
        />
        <View className="rounded-t-card bg-surface p-6 pb-8">
          <Text className="mb-2 text-title font-semibold text-ink" accessibilityRole="header">
            {blocked ? "Not yet — a few things first" : "Delete your account?"}
          </Text>

          {checking ? (
            <Text className="mb-4 text-caption text-muted">Checking your account…</Text>
          ) : blocked ? (
            <View className="mb-4 gap-2">
              {blockers.map((b) => (
                <Text key={b.code} className="text-body text-ink">
                  {"•"} {b.message}
                </Text>
              ))}
              <Text className="mt-1 text-caption text-muted">
                Once these are cleared, come back here and the deletion goes through.
              </Text>
            </View>
          ) : (
            <View className="mb-4 gap-2">
              <Text className="text-caption text-muted">This removes, permanently:</Text>
              {DELETION_REMOVES.map((line) => (
                <Text key={line} className="text-body text-ink">
                  {"•"} {line}
                </Text>
              ))}
              <Text className="mt-1 text-caption text-muted">{DELETION_KEEPS}</Text>
            </View>
          )}

          {error ? <Text className="mb-3 text-caption text-danger">{error}</Text> : null}

          <View className="gap-2">
            {!checking && !blocked ? (
              <Button
                label="Delete my account"
                onPress={handleDelete}
                loading={deleting}
                className="bg-danger"
              />
            ) : null}
            <Button
              label={blocked ? "OK" : "Keep my account"}
              variant="text"
              onPress={handleClose}
              disabled={deleting}
            />
          </View>
        </View>
      </View>
    </Modal>
  );
}
