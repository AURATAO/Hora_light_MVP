import { useState } from "react";
import { Image, Modal, Pressable, Text, View } from "react-native";
import * as ImagePicker from "expo-image-picker";
import { Button, Input } from "./ui";

export interface CompleteTaskPayload {
  photoUri: string;
  note: string;
}

export interface CompleteTaskSheetProps {
  visible: boolean;
  onClose: () => void;
  onSubmit: (payload: CompleteTaskPayload) => Promise<void>;
}

export function CompleteTaskSheet({ visible, onClose, onSubmit }: CompleteTaskSheetProps) {
  const [photoUri, setPhotoUri] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setPhotoUri(null);
    setNote("");
    setSubmitting(false);
    setError(null);
  }

  function handleClose() {
    if (submitting) return;
    reset();
    onClose();
  }

  async function pickFromCamera() {
    const perm = await ImagePicker.requestCameraPermissionsAsync();
    if (!perm.granted) {
      setError("Camera access is off. Enable it in Settings to take a photo.");
      return;
    }
    const result = await ImagePicker.launchCameraAsync({ quality: 0.7 });
    if (!result.canceled && result.assets[0]) {
      setPhotoUri(result.assets[0].uri);
      setError(null);
    }
  }

  async function pickFromLibrary() {
    const perm = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (!perm.granted) {
      setError("Photo library access is off. Enable it in Settings to choose a photo.");
      return;
    }
    const result = await ImagePicker.launchImageLibraryAsync({
      quality: 0.7,
      mediaTypes: ["images"],
    });
    if (!result.canceled && result.assets[0]) {
      setPhotoUri(result.assets[0].uri);
      setError(null);
    }
  }

  async function handleSubmit() {
    if (!photoUri) {
      setError("Add a photo to complete this task.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({ photoUri, note: note.trim() });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't complete this task. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={handleClose}>
      <Pressable className="flex-1 justify-end bg-ink/40" onPress={handleClose}>
        <Pressable className="rounded-t-card bg-surface p-6 pb-8" onPress={(e) => e.stopPropagation()}>
          <Text className="mb-1 text-title font-semibold text-ink">Complete this task</Text>
          <Text className="mb-4 text-caption text-muted">
            Add a photo so the requester can see it's done.
          </Text>

          {photoUri ? (
            <Image source={{ uri: photoUri }} className="mb-4 h-40 w-full rounded-sm" resizeMode="cover" />
          ) : null}

          <View className="flex-row gap-2">
            <Button label="Take photo" variant="secondary" onPress={pickFromCamera} className="flex-1" />
            <Button label="Choose photo" variant="secondary" onPress={pickFromLibrary} className="flex-1" />
          </View>

          <Input
            className="mt-4"
            value={note}
            onChangeText={setNote}
            placeholder="Add a note (optional)"
            multiline
            numberOfLines={3}
            textAlignVertical="top"
          />

          {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}

          <View className="mt-5 gap-2">
            <Button label="Complete task" onPress={handleSubmit} loading={submitting} disabled={!photoUri} />
            <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
          </View>
        </Pressable>
      </Pressable>
    </Modal>
  );
}
