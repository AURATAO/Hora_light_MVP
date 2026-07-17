import { useCallback, useState } from "react";
import { RefreshControl, ScrollView, Text, View } from "react-native";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import { ChevronLeft, MapPin, Phone, UserRound } from "lucide-react-native";
import { Avatar, Card, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getPublicProfile } from "../../lib/api";
import type { PublicProfile } from "../../lib/types";
import { color, size } from "../../theme/tokens";

export default function PublicProfileScreen() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const id = Array.isArray(params.id) ? params.id[0] : (params.id ?? "");

  const [profile, setProfile] = useState<PublicProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    try {
      const p = await getPublicProfile(id);
      setProfile(p);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setError(e instanceof Error ? e.message : "Couldn't load this profile");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

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

  return (
    <Screen scroll={false}>
      <View className="mb-6 mt-4 flex-row items-center">
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="ml-1 text-title font-semibold text-ink">Profile</Text>
      </View>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-[100px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : error && !profile ? (
        <EmptyState
          icon={UserRound}
          title="Couldn't load this profile"
          caption={error}
          actionLabel="Retry"
          onAction={load}
        />
      ) : profile ? (
        <ScrollView
          showsVerticalScrollIndicator={false}
          refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />}
        >
          <View className="mb-6 flex-row items-start gap-4">
            <Avatar uri={profile.avatar_url} name={profile.name} size={72} />
            <View className="flex-1 pt-1">
              <Text className="text-title font-semibold text-ink">{profile.name || "—"}</Text>
              {profile.city ? (
                <View className="mt-1 flex-row items-center gap-1.5">
                  <MapPin color={color.muted} size={14} strokeWidth={size.iconStroke} />
                  <Text className="text-caption text-muted">{profile.city}</Text>
                </View>
              ) : null}
              {profile.phone ? (
                <View className="mt-1 flex-row items-center gap-1.5">
                  <Phone color={color.muted} size={14} strokeWidth={size.iconStroke} />
                  <Text className="text-caption text-muted">{profile.phone}</Text>
                </View>
              ) : null}
            </View>
          </View>

          {profile.bio ? <Text className="mb-6 text-body text-ink">{profile.bio}</Text> : null}

          <View className="mb-8 flex-row gap-3">
            <Card className="flex-1">
              <Text className="text-title font-semibold text-ink">{profile.asg_completed}</Text>
              <Text className="mt-1 text-caption text-muted">Tasks completed</Text>
            </Card>
            <Card className="flex-1">
              <Text className="text-title font-semibold text-ink">{profile.asg_in_progress}</Text>
              <Text className="mt-1 text-caption text-muted">In progress</Text>
            </Card>
          </View>
        </ScrollView>
      ) : null}
    </Screen>
  );
}
