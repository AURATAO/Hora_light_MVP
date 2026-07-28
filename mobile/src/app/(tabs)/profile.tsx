import { useCallback, useState } from "react";
import { Alert, Linking, RefreshControl, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import * as ImagePicker from "expo-image-picker";
import * as SecureStore from "expo-secure-store";
import * as WebBrowser from "expo-web-browser";
import Constants from "expo-constants";
import {
  ChevronRight,
  FileText,
  MessageCircle,
  ShieldCheck,
  User,
  type LucideIcon,
} from "lucide-react-native";
import { EditProfileSheet } from "../../components/EditProfileSheet";
import { SupporterStatusRow } from "../../components/SupporterStatusBanner";
import { Avatar, Card, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getProfile, logout, uploadAvatar } from "../../lib/api";
import { cancelAllOvertimeReminders } from "../../lib/overtime-reminders";
import { unregisterCurrentPushToken } from "../../lib/push";
import { HORA_WHATSAPP_NUMBER, LEGAL_URLS } from "../../lib/constants";
import { supabase } from "../../lib/supabase";
import type { Profile } from "../../lib/types";
import { color, size } from "../../theme/tokens";

function formatMemberSince(createdAt: string): string {
  return new Date(createdAt).toLocaleDateString([], { month: "long", year: "numeric" });
}

function DetailRow({ label, value }: { label: string; value: string | null }) {
  return (
    <View>
      <Text className="text-caption text-muted">{label}</Text>
      <Text className="mt-0.5 text-body text-ink">{value || "—"}</Text>
    </View>
  );
}

function ListRow({ icon: Icon, label, onPress }: { icon: LucideIcon; label: string; onPress: () => void }) {
  return (
    <PressableScale onPress={onPress} className="flex-row items-center justify-between py-3">
      <View className="flex-row items-center gap-3">
        <Icon color={color.muted} size={18} strokeWidth={size.iconStroke} />
        <Text className="text-body text-ink">{label}</Text>
      </View>
      <ChevronRight color={color.muted} size={18} strokeWidth={size.iconStroke} />
    </PressableScale>
  );
}

export default function Profile() {
  const router = useRouter();

  const [profile, setProfile] = useState<Profile | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [uploadingAvatar, setUploadingAvatar] = useState(false);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const load = useCallback(async () => {
    try {
      const p = await getProfile();
      setProfile(p);
      setError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setError(e instanceof Error ? e.message : "Couldn't load your profile");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load])
  );

  async function onRefresh() {
    setRefreshing(true);
    await load();
    setRefreshing(false);
  }

  async function pickAndUploadAvatar(fromCamera: boolean) {
    const perm = fromCamera
      ? await ImagePicker.requestCameraPermissionsAsync()
      : await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (!perm.granted) {
      Alert.alert(
        fromCamera ? "Camera access is off" : "Photo library access is off",
        "Enable it in Settings to update your photo."
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
      // server/main.go's POST /profile/avatar writes avatar_url to the
      // profiles row itself — no follow-up PATCH /profile call needed.
      const { url } = await uploadAvatar({ uri: asset.uri, mimeType: asset.mimeType, fileName: asset.fileName });
      setProfile((prev) => (prev ? { ...prev, avatar_url: url } : prev));
    } catch (e) {
      if (handleAuthError(e)) return;
      Alert.alert("Couldn't update photo", e instanceof Error ? e.message : "Try again.");
    } finally {
      setUploadingAvatar(false);
    }
  }

  function handleAvatarPress() {
    Alert.alert("Update photo", undefined, [
      { text: "Take photo", onPress: () => pickAndUploadAvatar(true) },
      { text: "Choose from library", onPress: () => pickAndUploadAvatar(false) },
      { text: "Cancel", style: "cancel" },
    ]);
  }

  async function openWhatsApp() {
    if (!/[1-9]/.test(HORA_WHATSAPP_NUMBER)) {
      Alert.alert(
        "WhatsApp number not configured",
        "Set HORA_WHATSAPP_NUMBER in src/lib/constants.ts before this ships."
      );
      return;
    }
    // Deliberately leaves the app — this opens the WhatsApp app itself, not
    // an in-app browser sheet. Only the failure path (WhatsApp not
    // installed, etc.) needs handling.
    try {
      await Linking.openURL(`https://wa.me/${HORA_WHATSAPP_NUMBER}`);
    } catch {
      Alert.alert("Couldn't open WhatsApp", "Make sure WhatsApp is installed and try again.");
    }
  }

  async function openLegalPage(url: string) {
    try {
      await WebBrowser.openBrowserAsync(url);
    } catch {
      Alert.alert("Page temporarily unavailable", "Try again later.");
    }
  }

  function handleLogout() {
    Alert.alert("Log out?", "You'll need to sign in again to use HO:RA.", [
      { text: "Cancel", style: "cancel" },
      {
        text: "Log out",
        style: "destructive",
        onPress: async () => {
          // Drop this device's push token while the session cookie is still
          // valid — /push/unregister is auth-gated, so it must run before
          // logout() clears the session. Best-effort; never blocks logout.
          await unregisterCurrentPushToken();
          await Promise.all([
            supabase.auth.signOut().catch(() => {}),
            logout().catch(() => {}),
            cancelAllOvertimeReminders().catch(() => {}),
          ]);
          await Promise.all([
            SecureStore.deleteItemAsync("hora_user_id").catch(() => {}),
            SecureStore.deleteItemAsync("hora_user_email").catch(() => {}),
            SecureStore.deleteItemAsync("hora_user_name").catch(() => {}),
          ]);
          router.replace("/(auth)/login");
        },
      },
    ]);
  }

  if (loading) {
    return (
      <Screen insetForTabBar headline="Profile">
        <View className="gap-3">
          <Skeleton className="h-[100px]" />
          <Skeleton className="h-[140px]" />
          <Skeleton className="h-[140px]" />
        </View>
      </Screen>
    );
  }

  if (error && !profile) {
    return (
      <Screen insetForTabBar headline="Profile">
        <EmptyState
          icon={User}
          title="Couldn't load your profile"
          caption={error}
          actionLabel="Retry"
          onAction={load}
        />
      </Screen>
    );
  }

  if (!profile) return null;

  return (
    <Screen insetForTabBar refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}>
      <Text className="mb-6 mt-4 text-display text-ink">Profile</Text>

      <View className="mb-6 items-center gap-3">
        <PressableScale onPress={handleAvatarPress} disabled={uploadingAvatar}>
          <Avatar uri={profile.avatar_url} name={profile.name} size={88} />
        </PressableScale>
        <View className="items-center">
          <Text className="text-title font-semibold text-ink">{profile.name || "Add your name"}</Text>
          <Text className="text-caption text-muted">{profile.email}</Text>
          <Text className="mt-1 text-caption text-muted">Member since {formatMemberSince(profile.created_at)}</Text>
        </View>
      </View>

      <View className="mb-6">
        {profile.supporter_status !== "none" ? (
          // approved/applied/rejected: same states as the Work tab banner, at row scale.
          <SupporterStatusRow status={profile.supporter_status} />
        ) : (
          <PressableScale
            onPress={() => router.push("/(tabs)/work")}
            className="flex-row items-center justify-between rounded-card border border-line bg-surface p-4"
          >
            <View className="flex-1 pr-2">
              <Text className="text-body font-semibold text-ink">Want to earn?</Text>
              <Text className="mt-0.5 text-caption text-muted">Become a supporter and help people nearby.</Text>
            </View>
            <ChevronRight color={color.muted} size={18} strokeWidth={size.iconStroke} />
          </PressableScale>
        )}
      </View>

      <View className="mb-6">
        <View className="mb-2 flex-row items-center justify-between">
          <Text className="text-title font-semibold text-ink">Your details</Text>
          <PressableScale onPress={() => setEditOpen(true)} hitSlop={8}>
            <Text className="text-caption font-semibold text-brand">Edit</Text>
          </PressableScale>
        </View>
        <Card className="gap-3">
          <DetailRow label="Phone" value={profile.phone} />
          <DetailRow label="City" value={profile.city} />
          <DetailRow label="Bio" value={profile.bio} />
        </Card>
      </View>

      <View className="mb-6">
        <Text className="mb-2 text-title font-semibold text-ink">Contact & about</Text>
        <Card>
          <ListRow icon={MessageCircle} label="Contact HO:RA on WhatsApp" onPress={openWhatsApp} />
          <View className="h-px bg-line" />
          <ListRow icon={FileText} label="Terms of Use" onPress={() => openLegalPage(LEGAL_URLS.terms)} />
          <View className="h-px bg-line" />
          <ListRow icon={ShieldCheck} label="Privacy Policy" onPress={() => openLegalPage(LEGAL_URLS.privacy)} />
        </Card>
        <Text className="mt-3 text-center text-caption text-muted">
          Version {Constants.expoConfig?.version ?? "—"}
        </Text>
      </View>

      <PressableScale onPress={handleLogout} className="mb-8 items-center py-3">
        <Text className="text-body font-semibold text-danger">Log out</Text>
      </PressableScale>

      <EditProfileSheet
        visible={editOpen}
        profile={profile}
        onClose={() => setEditOpen(false)}
        onSaved={(updated) => {
          setProfile(updated);
          setEditOpen(false);
        }}
      />
    </Screen>
  );
}
