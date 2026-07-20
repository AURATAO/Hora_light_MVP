import { useState } from "react";
import { Alert, Text, View } from "react-native";
import { useRouter } from "expo-router";
import * as ImagePicker from "expo-image-picker";
import { Avatar, Button, Input, PressableScale, Screen } from "../../components/ui";
import { ApiError, updateProfile, uploadAvatar } from "../../lib/api";
import { useAuthState } from "../_layout";

// Screen 2 of onboarding. Required: name, phone, and a profile photo (web
// requires phone + photo; mobile also requires a name). Seeded from the
// cached profile so a returning-but-incomplete user sees what they already
// have. Saving PATCHes name + phone (the avatar is persisted by its own
// upload endpoint), then enters the tabs.
export default function CompleteProfile() {
  const router = useRouter();
  const { profile, refresh } = useAuthState();

  const [name, setName] = useState(profile?.name ?? "");
  const [phone, setPhone] = useState(profile?.phone ?? "");
  const [avatarUrl, setAvatarUrl] = useState<string | null>(profile?.avatar_url ?? null);
  const [uploadingAvatar, setUploadingAvatar] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Presence-only, matching web (no phone format/regex on client or server).
  const canContinue = !!name.trim() && !!phone.trim() && !!avatarUrl;

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  async function pickAndUploadAvatar(fromCamera: boolean) {
    const perm = fromCamera
      ? await ImagePicker.requestCameraPermissionsAsync()
      : await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (!perm.granted) {
      Alert.alert(
        fromCamera ? "Camera access is off" : "Photo library access is off",
        "Enable it in Settings to add your photo."
      );
      return;
    }

    const result = fromCamera
      ? await ImagePicker.launchCameraAsync({ quality: 0.7 })
      : await ImagePicker.launchImageLibraryAsync({ quality: 0.7, mediaTypes: ["images"] });
    if (result.canceled || !result.assets[0]) return;

    const asset = result.assets[0];
    setUploadingAvatar(true);
    try {
      // POST /profile/avatar writes avatar_url to the profiles row itself, so
      // completing the form only needs to PATCH name + phone (matches web).
      const { url } = await uploadAvatar({
        uri: asset.uri,
        mimeType: asset.mimeType,
        fileName: asset.fileName,
      });
      setAvatarUrl(url);
    } catch (e) {
      if (handleAuthError(e)) return;
      Alert.alert("Couldn't add photo", e instanceof Error ? e.message : "Try again.");
    } finally {
      setUploadingAvatar(false);
    }
  }

  function handleAvatarPress() {
    Alert.alert("Add photo", undefined, [
      { text: "Take photo", onPress: () => pickAndUploadAvatar(true) },
      { text: "Choose from library", onPress: () => pickAndUploadAvatar(false) },
      { text: "Cancel", style: "cancel" },
    ]);
  }

  async function handleContinue() {
    if (!canContinue) {
      setError("Add your name, phone number, and a profile photo to continue.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await updateProfile({ name: name.trim(), phone: phone.trim() });
      await refresh();
      router.replace("/(tabs)/home");
    } catch (e) {
      if (handleAuthError(e)) return;
      setError(e instanceof Error ? e.message : "Couldn't save your profile. Try again.");
      setSubmitting(false);
    }
  }

  return (
    <Screen>
      <Text className="mb-2 mt-4 text-display text-ink">Complete your profile</Text>
      <Text className="mb-8 text-body text-muted">
        Add a few details so people know who they're working with.
      </Text>

      <View className="mb-8 items-center gap-3">
        <PressableScale onPress={handleAvatarPress} disabled={uploadingAvatar}>
          <Avatar uri={avatarUrl} name={name} size={88} />
        </PressableScale>
        <PressableScale onPress={handleAvatarPress} disabled={uploadingAvatar} hitSlop={8}>
          <Text className="text-caption font-semibold text-brand">
            {uploadingAvatar ? "Uploading…" : avatarUrl ? "Change photo" : "Add a photo"}
          </Text>
        </PressableScale>
      </View>

      <View className="gap-3">
        <Input label="Name" value={name} onChangeText={setName} placeholder="Jane Doe" />
        <Input
          label="Phone"
          value={phone}
          onChangeText={setPhone}
          placeholder="(555) 555-5555"
          keyboardType="phone-pad"
        />
      </View>

      {error ? <Text className="mt-4 text-caption text-danger">{error}</Text> : null}

      <View className="mb-8 mt-8">
        <Button label="Continue" onPress={handleContinue} loading={submitting} disabled={!canContinue} />
      </View>
    </Screen>
  );
}
