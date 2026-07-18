import { useCallback, useState } from "react";
import { Text, View, type LayoutChangeEvent } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import { Chatbox, Session, getConversationBuilder, type ConversationBuilder, type User } from "@talkjs/expo";
import { ChevronLeft, CircleAlert } from "lucide-react-native";
import { Avatar } from "../../../components/ui/Avatar";
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
  taskTitle: string;
  counterpartName: string;
  counterpartAvatar: string | null;
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
  // TalkJS's Chatbox needs to know how tall our own header is to position its
  // message field above the keyboard correctly (talkjs.com/docs — the SDK's
  // built-in KeyboardAvoidingView offset assumes the Chatbox starts at the
  // physical top of the screen; ours starts below this header instead).
  // Measured via onLayout rather than a guessed constant so it stays correct
  // across devices/safe-area sizes.
  const [headerHeight, setHeaderHeight] = useState<number | undefined>(undefined);

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

      setSetup({
        me,
        conversationBuilder: builder,
        signature,
        taskTitle: task.title || "Task",
        counterpartName: otherProfile?.name ?? (otherEmail ? deriveName(otherEmail) : "Chat"),
        counterpartAvatar: otherProfile?.avatar_url ?? null,
      });
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

  function onHeaderLayout(e: LayoutChangeEvent) {
    setHeaderHeight(e.nativeEvent.layout.height);
  }

  return (
    <View className="flex-1 bg-page">
      <SafeAreaView edges={["top"]} className="border-b border-line bg-surface" onLayout={onHeaderLayout}>
        <View className="flex-row items-center gap-3 px-4 py-3">
          <PressableScale
            onPress={() => router.back()}
            className="h-11 w-11 items-center justify-center rounded-pill"
            hitSlop={8}
          >
            <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
          </PressableScale>
          {setup ? (
            <>
              <Avatar uri={setup.counterpartAvatar} name={setup.counterpartName} size={32} />
              <View className="flex-1">
                <Text className="text-body font-semibold text-ink" numberOfLines={1}>
                  {setup.counterpartName}
                </Text>
                <Text className="text-caption text-muted" numberOfLines={1}>
                  {setup.taskTitle}
                </Text>
              </View>
            </>
          ) : (
            <Text className="text-body font-semibold text-ink">Chat</Text>
          )}
        </View>
      </SafeAreaView>

      <SafeAreaView edges={["bottom", "left", "right"]} className="flex-1">
        {loading ? (
          <View className="gap-3 px-6 pt-4">
            <Skeleton className="h-16" />
            <Skeleton className="h-16 w-2/3" />
            <Skeleton className="h-16 w-1/2" />
          </View>
        ) : error || !TALKJS_APP_ID ? (
          <View className="px-6 pt-4">
            <EmptyState
              icon={CircleAlert}
              title="Couldn't load this chat"
              caption={error ?? "Missing TalkJS app configuration."}
              actionLabel={error ? "Retry" : undefined}
              onAction={error ? load : undefined}
            />
          </View>
        ) : sessionError ? (
          <View className="px-6 pt-4">
            <EmptyState
              icon={CircleAlert}
              title="Chat couldn't connect"
              caption={sessionError}
              actionLabel="Retry"
              onAction={retrySession}
            />
          </View>
        ) : setup ? (
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
              keyboardVerticalOffset={headerHeight}
            />
          </Session>
        ) : null}
      </SafeAreaView>
    </View>
  );
}
