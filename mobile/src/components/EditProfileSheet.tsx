import { useEffect, useState } from "react";
import { KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import { Button, Input } from "./ui";
import { updateProfile } from "../lib/api";
import type { Profile } from "../lib/types";

export interface EditProfileSheetProps {
  visible: boolean;
  profile: Profile;
  onClose: () => void;
  onSaved: (profile: Profile) => void;
}

export function EditProfileSheet({ visible, profile, onClose, onSaved }: EditProfileSheetProps) {
  const [name, setName] = useState(profile.name ?? "");
  const [phone, setPhone] = useState(profile.phone ?? "");
  const [city, setCity] = useState(profile.city ?? "");
  const [bio, setBio] = useState(profile.bio ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Re-seed from the latest profile every time the sheet opens, in case it
  // changed (e.g. a fresh avatar upload) since the last edit.
  useEffect(() => {
    if (!visible) return;
    setName(profile.name ?? "");
    setPhone(profile.phone ?? "");
    setCity(profile.city ?? "");
    setBio(profile.bio ?? "");
    setError(null);
  }, [visible, profile]);

  function handleClose() {
    if (submitting) return;
    onClose();
  }

  async function handleSubmit() {
    // A name is required to save, same as on complete-profile: it is what
    // everyone else sees, and the email prefix is no longer a stand-in.
    if (!name.trim()) {
      setError("Enter the name you'd like to be called.");
      return;
    }
    if (!phone.trim()) {
      setError("Enter a phone number.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const updated = await updateProfile({
        name: name.trim(),
        phone: phone.trim(),
        city: city.trim(),
        bio: bio.trim(),
      });
      onSaved(updated);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't save your profile. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      {/* Lifts the sheet above the keyboard so the note field — and the primary
          button sitting below it — stay visible while typing. */}
      {/* A full-window RN Modal: KeyboardAvoidingView measures from the window
          here, so `padding` lifts the sheet above the keyboard and the bio
          field and the primary button below it stay visible while typing. */}
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === "ios" ? "padding" : undefined}>
        <View className="flex-1 justify-end">
          {/* The backdrop is its own layer, not a Pressable wrapping the sheet
              (the NamePromptSheet pattern). A Pressable is `accessible` by
              default, so wrapping the sheet in one collapsed the heading, all
              four fields and both buttons into a single VoiceOver element that
              read as one button — nothing inside it could be focused or
              edited with a screen reader on. As a sibling it is one focusable
              "Close" control and the sheet's own controls are each their own. */}
          <Pressable
            className="absolute inset-0 bg-ink/40"
            onPress={handleClose}
            accessibilityRole="button"
            accessibilityLabel="Close"
          />
          <View className="rounded-t-card bg-surface p-6 pb-8">
            <Text className="mb-4 text-title font-semibold text-ink" accessibilityRole="header">
              Edit profile
            </Text>
            <View className="gap-3">
              <Input label="Name" value={name} onChangeText={setName} placeholder="Jane Doe" />
              <Input
                label="Phone"
                value={phone}
                onChangeText={setPhone}
                placeholder="(555) 555-5555"
                keyboardType="phone-pad"
              />
              <Input label="City" value={city} onChangeText={setCity} placeholder="New York, NY" />
              <Input
                label="Bio"
                value={bio}
                onChangeText={setBio}
                placeholder="A little about you"
                multiline
                numberOfLines={3}
                textAlignVertical="top"
                className="max-h-[90px]"
              />
            </View>
            {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
            {/* 6, not 5: tailwind.config.js has no `5` in its spacing scale, so
                mt-5 generated nothing and the buttons sat flush against the form.
                Same fix as CancelTaskSheet. */}
            <View className="mt-6 gap-2">
              <Button label="Save changes" onPress={handleSubmit} loading={submitting} disabled={!phone.trim()} />
              <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </View>
        </View>
      </KeyboardAvoidingView>
    </Modal>
  );
}
