import { useCallback, useState } from "react";
import { Text, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import { Chatbox, Session, getConversationBuilder, type ConversationBuilder, type User } from "@talkjs/expo";
import { ChevronLeft, CircleAlert } from "lucide-react-native";
import { EmptyState } from "../../../components/ui/EmptyState";
import { PressableScale } from "../../../components/ui/PressableScale";
import { Skeleton } from "../../../components/ui/Skeleton";
import { ApiError, getMe, getProfile, getPublicProfile, getTalkjsSignature, getTask } from "../../../lib/api";
import { color, size } from "../../../theme/tokens";

const TALKJS_APP_ID = process.env.EXPO_PUBLIC_TALKJS_APP_ID;

// Matches web's TaskChatBox.jsx deriveName — a display-name fallback for the
// counterpart when their public profile has no name set.
function deriveName(email: string): string {
  const at = email.indexOf("@");
  return (at > 0 ? email.slice(0, at) : email).replace(/\./g, " ");
}

interface ChatSetup {
  me: User;
  conversationBuilder: ConversationBuilder;
  signature: string;
}

export default function TaskChat() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const id = Array.isArray(params.id) ? params.id[0] : (params.id ?? "");

  const [setup, setSetup] = useState<ChatSetup | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [sessionAttempt, setSessionAttempt] = useState(0);
  const [sessionError, setSessionError] = useState<string | null>(null);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const load = useCallback(async () => {
    try {
      const [auth, profile, task, signature] = await Promise.all([
        getMe(),
        getProfile(),
        getTask(id),
        getTalkjsSignature(),
      ]);
      if (!auth.auth) {
        router.replace("/(auth)/login");
        return;
      }

      const isAssigneeSelf = task.assigned_to_id === auth.id;
      const otherId = isAssigneeSelf ? task.requester_id : task.assigned_to_id;
      const otherEmail = isAssigneeSelf ? task.requester : task.assigned_to;

      const otherProfile = otherId ? await getPublicProfile(otherId).catch(() => null) : null;

      const me: User = {
        id: profile.email ?? auth.email,
        name: auth.name,
        email: profile.email ?? auth.email,
        photoUrl: profile.avatar_url ?? undefined,
      };

      const builder = getConversationBuilder(`task_${task.id}`);
      builder.setParticipant(me);
      if (otherEmail) {
        builder.setParticipant({
          id: otherEmail,
          name: otherProfile?.name ?? deriveName(otherEmail),
          email: otherEmail,
          photoUrl: otherProfile?.avatar_url ?? undefined,
        });
      }
      builder.setAttributes({ subject: task.title || "Task", custom: { taskId: task.id } });

      setSetup({ me, conversationBuilder: builder, signature });
      setError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setError(e instanceof Error ? e.message : "Couldn't load this chat");
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

  function retrySession() {
    setSessionError(null);
    setSessionAttempt((n) => n + 1);
  }

  return (
    <SafeAreaView className="flex-1 bg-page" edges={["top", "left", "right"]}>
      <View className="mb-2 mt-4 flex-row items-center px-6">
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="ml-1 text-title font-semibold text-ink">Chat</Text>
      </View>

      {loading ? (
        <View className="gap-3 px-6">
          <Skeleton className="h-16" />
          <Skeleton className="h-16 w-2/3" />
          <Skeleton className="h-16 w-1/2" />
        </View>
      ) : error || !TALKJS_APP_ID ? (
        <View className="px-6">
          <EmptyState
            icon={CircleAlert}
            title="Couldn't load this chat"
            caption={error ?? "Missing TalkJS app configuration."}
            actionLabel={error ? "Retry" : undefined}
            onAction={error ? load : undefined}
          />
        </View>
      ) : sessionError ? (
        <View className="px-6">
          <EmptyState
            icon={CircleAlert}
            title="Chat couldn't connect"
            caption={sessionError}
            actionLabel="Retry"
            onAction={retrySession}
          />
        </View>
      ) : setup ? (
        <View className="flex-1">
          <Session
            key={sessionAttempt}
            appId={TALKJS_APP_ID}
            me={setup.me}
            signature={setup.signature}
            onError={() => setSessionError("Check your connection and try again.")}
          >
            <Chatbox
              conversationBuilder={setup.conversationBuilder}
              showChatHeader={false}
              messageField={{ placeholder: "Type here…" }}
            />
          </Session>
        </View>
      ) : null}
    </SafeAreaView>
  );
}
