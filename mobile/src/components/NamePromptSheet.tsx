import { useEffect, useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { Button, Input } from "./ui";
import { updateProfile } from "../lib/api";
import { needsNamePrompt } from "../lib/onboarding";
import { useAuthState } from "../app/_layout";

// "What should we call you?" — asked ONCE of an account that has no display
// name, and skippable.
//
// Until build 12 every profile was seeded with the email's local part, so a
// supporter was announced as "taoaura.lavoro is on the way". The seed is gone
// (the server never writes it; migration 20260922120000 blanked the old
// ones), which leaves existing accounts nameless until asked. New accounts
// are asked by complete-profile, where the name is required; this is for
// everyone who finished onboarding before that.
//
// Skipping is remembered per device and account (AsyncStorage), so nobody is
// nagged — Edit profile is always there. Mounted by the tabs layout, so it
// meets the person on whichever tab they land.
const skipKey = (email: string) => `hora_name_prompt_skipped:${email.toLowerCase()}`;

export function NamePromptSheet() {
  const { profile, refresh } = useAuthState();
  const email = profile?.email ?? "";
  const wanted = needsNamePrompt(profile);

  // null until the skip flag has been read: "unknown" must not flash the
  // sheet at somebody who already skipped it.
  const [skipped, setSkipped] = useState<boolean | null>(null);
  const [name, setName] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  useEffect(() => {
    if (!wanted || !email) return;
    let alive = true;
    AsyncStorage.getItem(skipKey(email))
      .then((v) => alive && setSkipped(v === "1"))
      .catch(() => alive && setSkipped(false));
    return () => {
      alive = false;
    };
  }, [wanted, email]);

  const visible = wanted && !done && skipped === false;

  async function handleSave() {
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Type a name, or skip for now.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await updateProfile({ name: trimmed });
      setDone(true);
      // The cached profile now carries the name, so the gate stays quiet.
      refresh().catch(() => {});
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't save your name. Try again.");
    } finally {
      setSaving(false);
    }
  }

  function handleSkip() {
    if (saving) return;
    setDone(true);
    if (email) AsyncStorage.setItem(skipKey(email), "1").catch(() => {});
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleSkip}>
      {/* A full-window RN Modal: KeyboardAvoidingView measures from the window
          here, so `padding` is right (the modal-SHEET case that needed
          automaticallyAdjustKeyboardInsets is post-task, not this). */}
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <View className="flex-1 justify-end">
          {/* The backdrop is its own layer, not a Pressable wrapping the sheet:
              a Pressable is `accessible` by default, and wrapping the sheet in
              one collapses the heading, the field and both buttons into a
              single "button" for VoiceOver (same reason BetaNoticeSheet does
              this). */}
          <Pressable
            className="absolute inset-0 bg-ink/40"
            onPress={handleSkip}
            accessibilityLabel="Skip for now"
          />
          <View className="rounded-t-card bg-surface p-6 pb-8">
            <Text className="text-title font-semibold text-ink">What should we call you?</Text>
            <Text className="mb-4 mt-2 text-body text-muted">
              This is the name supporters and requesters see on your tasks, in chat and in notifications.
            </Text>
            <Input
              label="Name"
              value={name}
              onChangeText={(v) => {
                setName(v);
                if (error) setError(null);
              }}
              placeholder="Jane Doe"
              autoFocus
              autoCapitalize="words"
              returnKeyType="done"
              onSubmitEditing={handleSave}
              error={error ?? undefined}
            />
            <View className="mt-6 gap-2">
              <Button label="Save" onPress={handleSave} loading={saving} />
              <Button label="Skip for now" variant="text" onPress={handleSkip} disabled={saving} />
            </View>
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}
