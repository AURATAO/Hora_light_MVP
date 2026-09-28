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
import { UserSafetyMenu } from "../../../components/UserSafetyMenu";
import { CHAT_BLOCKED_LINE } from "../../../lib/safety";
import { ApiError, getMe, getProfile, getPublicProfile, getTalkjsSignature, getTask } from "../../../lib/api";
import { color, size } from "../../../theme/tokens";

const TALKJS_APP_ID = process.env.EXPO_PUBLIC_TALKJS_APP_ID;

interface ChatSetup {
  me: User;
  conversationBuilder: ConversationBuilder;
  signature: string;
  taskTitle: string;
  counterpartName: string;
  counterpartAvatar: string | null;
  /** The other party's users.id, for Report / Block. Null only when there is
   *  no other party yet. On the sandbox's self-accepted task this is the
   *  viewer's own id and the menu still renders (lib/safety.ts
   *  safetyCounterpart); the server refuses the self-report itself. */
  counterpartId: string | null;
  taskId: string;
  /** A block between the two (either way): the thread is read-only. */
  blocked: boolean;
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
      // Both are the server's resolution (names.go); the email is a TalkJS id
      // and is never shown as a name.
      const otherName =
        otherProfile?.name?.trim() ||
        (isAssigneeSelf ? task.requester_name : task.assignee_name)?.trim() ||
        "Your HO:RA contact";

      const me: User = {
        id: profile.email ?? auth.email,
        name: auth.name,
        email: profile.email ?? auth.email,
        photoUrl: profile.avatar_url ?? undefined,
      };

      // Blocked: both seats read-only. The server has already set this
      // through TalkJS's REST API (server/safety.go); passing it here too
      // keeps a conversation opened for the first time AFTER the block from
      // being created read-write. Unblocked, access is left unset, which
      // TalkJS treats as "leave whatever the server set" — so opening the
      // chat can never undo a block.
      const blocked = task.chat_blocked === true;
      const access = blocked ? ({ access: "Read" } as const) : undefined;
      const builder = getConversationBuilder(`task_${task.id}`);
      builder.setParticipant(me, access);
      if (otherEmail) {
        builder.setParticipant(
          {
            id: otherEmail,
            name: otherName,
            email: otherEmail,
            photoUrl: otherProfile?.avatar_url ?? undefined,
          },
          access
        );
      }
      builder.setAttributes({ subject: task.title || "Task", custom: { taskId: task.id } });

      setSetup({
        me,
        conversationBuilder: builder,
        signature,
        taskTitle: task.title || "Task",
        counterpartName: otherEmail ? otherName : "Chat",
        counterpartAvatar: otherProfile?.avatar_url ?? null,
        counterpartId: otherId || null,
        taskId: task.id,
        blocked,
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
              {setup.counterpartId ? (
                <UserSafetyMenu
                  userId={setup.counterpartId}
                  userName={setup.counterpartName}
                  taskId={setup.taskId}
                  blocked={setup.blocked}
                  onBlocked={load}
                />
              ) : null}
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
              key={setup.blocked ? "read-only" : "read-write"}
              conversationBuilder={setup.conversationBuilder}
              showChatHeader={false}
              messageField={setup.blocked ? { visible: false } : { placeholder: "Type here…" }}
              keyboardVerticalOffset={headerHeight}
            />
            {setup.blocked ? (
              <View className="border-t border-line bg-surface px-6 py-4">
                <Text className="text-caption text-muted">{CHAT_BLOCKED_LINE}</Text>
              </View>
            ) : null}
          </Session>
        ) : null}
      </SafeAreaView>
    </View>
  );
}
