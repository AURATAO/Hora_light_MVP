import { useState } from "react";
import { Image, KeyboardAvoidingView, Modal, Platform, Pressable, Text, View } from "react-native";
import * as ImagePicker from "expo-image-picker";
import { Button, Input, PressableScale } from "./ui";

export interface CompleteTaskPayload {
  photoUri: string;
  photoMimeType?: string | null;
  photoFileName?: string | null;
  note: string;
}

export interface CompleteTaskSheetProps {
  visible: boolean;
  onClose: () => void;
  onSubmit: (payload: CompleteTaskPayload) => Promise<void>;
}

interface PickedPhoto {
  uri: string;
  mimeType?: string | null;
  fileName?: string | null;
}

export function CompleteTaskSheet({ visible, onClose, onSubmit }: CompleteTaskSheetProps) {
  const [photo, setPhoto] = useState<PickedPhoto | null>(null);
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setPhoto(null);
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
      const asset = result.assets[0];
      setPhoto({ uri: asset.uri, mimeType: asset.mimeType, fileName: asset.fileName });
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
      const asset = result.assets[0];
      setPhoto({ uri: asset.uri, mimeType: asset.mimeType, fileName: asset.fileName });
      setError(null);
    }
  }

  async function handleSubmit() {
    if (!photo) {
      setError("Add a photo to complete this task.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit({
        photoUri: photo.uri,
        photoMimeType: photo.mimeType,
        photoFileName: photo.fileName,
        note: note.trim(),
      });
      reset();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Couldn't complete this task. Try again.");
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
            <Text className="mb-1 text-title font-semibold text-ink">Complete this task</Text>
            <Text className="mb-4 text-caption text-muted">
              Add a photo so the requester can see it's done.
            </Text>

            {photo ? (
              <View className="mb-4 gap-2">
                <Image source={{ uri: photo.uri }} className="h-[180px] w-full rounded-sm" resizeMode="cover" />
                <PressableScale onPress={() => setPhoto(null)} hitSlop={8} className="self-start">
                  <Text className="text-caption font-semibold text-brand">Change photo</Text>
                </PressableScale>
              </View>
            ) : (
              <View className="mb-4 gap-2">
                <View className="flex-row gap-2">
                  <Button label="Take photo" variant="secondary" onPress={pickFromCamera} className="flex-1" />
                  <Button label="Choose photo" variant="secondary" onPress={pickFromLibrary} className="flex-1" />
                </View>
                <Text className="text-caption text-muted">A photo is required to complete this task.</Text>
              </View>
            )}

            <Input
              value={note}
              onChangeText={setNote}
              placeholder="Add a note (optional)"
              multiline
              numberOfLines={3}
              textAlignVertical="top"
              className="max-h-[90px]"
            />

            {error ? <Text className="mt-3 text-caption text-danger">{error}</Text> : null}

            <View className="mt-6 gap-2">
              {submitting ? (
                <Text className="text-center text-caption text-muted">Uploading photo…</Text>
              ) : null}
              <Button label="Complete task" onPress={handleSubmit} loading={submitting} disabled={!photo} />
              <Button label="Cancel" variant="text" onPress={handleClose} disabled={submitting} />
            </View>
          </Pressable>
        </Pressable>
      </KeyboardAvoidingView>
    </Modal>
  );
}
