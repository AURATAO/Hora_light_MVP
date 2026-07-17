import { useEffect, useState } from "react";
import { Modal, Pressable, Text, View } from "react-native";
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
      <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
        <Pressable className="rounded-t-card bg-surface p-6 pb-8" onPress={(e) => e.stopPropagation()}>
          <Text className="mb-4 text-title font-semibold text-ink">Edit profile</Text>
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
            />
          </View>
          {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}
          <View className="mt-5 gap-2">
            <Button label="Save changes" onPress={handleSubmit} loading={submitting} disabled={!phone.trim()} />
            <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
          </View>
        </Pressable>
      </Pressable>
    </Modal>
  );
}
