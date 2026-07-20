import { useCallback, useEffect, useMemo, useState } from "react";
import { Image, RefreshControl, ScrollView, Text, View, useWindowDimensions } from "react-native";
import Animated, {
  ReduceMotion,
  useAnimatedStyle,
  useSharedValue,
  withTiming,
} from "react-native-reanimated";
import { useFocusEffect, useRouter } from "expo-router";
import * as SecureStore from "expo-secure-store";
import {
  ArrowRight,
  Bell,
  CircleAlert,
  ClipboardList,
  ImageIcon,
  UserRound,
} from "lucide-react-native";
import { Badge, Card, EmptyState, Logo, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getMe, getNotifications, getPostedTasks } from "../../lib/api";
import { getCategoryMeta } from "../../lib/categories";
import { HOME_CARDS, HOME_CARD_IMAGES, buildHeroLines } from "../../lib/home-content";
import { deriveTaskStatus, formatRelativeTime } from "../../lib/task-utils";
import type { Task, TaskCategory } from "../../lib/types";
import { color, size, space } from "../../theme/tokens";

// "For you" quick-access categories — a fast lane into post-task with the
// category pre-selected. The full set lives behind "What do you need?".
const HOME_CATEGORIES: TaskCategory[] = [
  "grocery",
  "delivery",
  "laundry",
  "queue",
  "companionship",
  "quick_errand",
];

const STATUS_LABEL: Record<ReturnType<typeof deriveTaskStatus>, string> = {
  open: "Open",
  assigned: "Assigned",
  completed: "Completed",
  cancelled: "Cancelled",
};

// Each card peeks the next one; the horizontal list snaps card-by-card.
const CARD_WIDTH_RATIO = 0.75;

// Hero placeholder rotation cadence and transition duration.
const HERO_ROTATE_MS = 6000;
const HERO_FADE_MS = 300;
const HERO_SLIDE = 8;

// Cross-fades + slides the hero placeholder line every ~6s. The interval only
// runs while the screen is focused (useFocusEffect), so no timer ticks in the
// background. Reduced-motion is honored via ReduceMotion.System.
function RotatingText({ lines }: { lines: string[] }) {
  const [index, setIndex] = useState(0);
  const opacity = useSharedValue(1);
  const translateY = useSharedValue(0);
  const safeIndex = index % lines.length;

  const animatedStyle = useAnimatedStyle(() => ({
    opacity: opacity.value,
    transform: [{ translateY: translateY.value }],
  }));

  // Slide/fade the new line in whenever the shown line changes (and on mount).
  useEffect(() => {
    opacity.value = 0;
    translateY.value = HERO_SLIDE;
    opacity.value = withTiming(1, { duration: HERO_FADE_MS, reduceMotion: ReduceMotion.System });
    translateY.value = withTiming(0, { duration: HERO_FADE_MS, reduceMotion: ReduceMotion.System });
  }, [safeIndex, opacity, translateY]);

  const advance = useCallback(() => setIndex((prev) => prev + 1), []);

  useFocusEffect(
    useCallback(() => {
      if (lines.length <= 1) return; // nothing to rotate through
      let swapTimer: ReturnType<typeof setTimeout> | undefined;
      const id = setInterval(() => {
        // Fade/slide the current line out, then swap once it's hidden. The swap
        // is a plain JS timeout so the in-animation (keyed on index) takes over.
        translateY.value = withTiming(-HERO_SLIDE, {
          duration: HERO_FADE_MS,
          reduceMotion: ReduceMotion.System,
        });
        opacity.value = withTiming(0, {
          duration: HERO_FADE_MS,
          reduceMotion: ReduceMotion.System,
        });
        swapTimer = setTimeout(advance, HERO_FADE_MS);
      }, HERO_ROTATE_MS);
      return () => {
        clearInterval(id);
        if (swapTimer) clearTimeout(swapTimer);
      };
    }, [lines.length, opacity, translateY, advance])
  );

  return (
    <View className="mr-2 flex-1">
      <Animated.View style={animatedStyle}>
        <Text className="text-title font-semibold text-white" numberOfLines={1}>
          {lines[safeIndex]}
        </Text>
      </Animated.View>
    </View>
  );
}

function CategoryCircle({
  category,
  onPress,
}: {
  category: TaskCategory;
  onPress: () => void;
}) {
  const meta = getCategoryMeta(category);
  const Icon = meta.icon;

  return (
    <PressableScale onPress={onPress} className="w-[64px] items-center gap-2">
      <View
        className="h-[64px] w-[64px] items-center justify-center rounded-pill bg-brand"
        style={{ aspectRatio: 1 }}
      >
        <Icon color={color.gold} size={24} strokeWidth={size.iconStroke} />
      </View>
      <Text className="text-center text-caption text-muted" numberOfLines={2}>
        {meta.label}
      </Text>
    </PressableScale>
  );
}

function NewsCard({
  card,
  width,
  onPress,
}: {
  card: (typeof HOME_CARDS)[number];
  width: number;
  onPress?: () => void;
}) {
  const image = HOME_CARD_IMAGES[card.slug];

  const body = (
    <View className="overflow-hidden rounded-card border border-line bg-surface" style={{ width }}>
      {image ? (
        <Image source={image} resizeMode="cover" style={{ width: "100%", height: 140 }} />
      ) : (
        <View className="h-[140px] items-center justify-center bg-brand-tint">
          <ImageIcon color={color.brand} size={24} strokeWidth={size.iconStroke} />
        </View>
      )}
      <View className="p-4">
        <Text className="text-body font-semibold text-ink" numberOfLines={1}>
          {card.title}
        </Text>
        <Text className="mt-1 text-caption text-muted" numberOfLines={1}>
          {card.caption}
        </Text>
      </View>
    </View>
  );

  if (onPress) {
    return <PressableScale onPress={onPress}>{body}</PressableScale>;
  }
  return body;
}

function TaskRow({ task, onPress }: { task: Task; onPress: () => void }) {
  const status = deriveTaskStatus(task);
  const meta = getCategoryMeta(task.category);
  const Icon = meta.icon;

  return (
    <Card className="mb-3" onPress={onPress}>
      <View className="flex-row items-center justify-between">
        <View className="mr-2 flex-1 flex-row items-center gap-2">
          <Icon color={color.muted} size={18} strokeWidth={size.iconStroke} />
          <Text className="flex-1 text-body font-semibold text-ink" numberOfLines={1}>
            {task.title}
          </Text>
        </View>
        {status === "assigned" ? (
          <UserRound color={color.muted} size={16} strokeWidth={size.iconStroke} />
        ) : null}
      </View>
      <View className="mt-2 flex-row items-center justify-between">
        <Text className="text-caption text-muted">
          {meta.label} · {formatRelativeTime(task.created_at)}
        </Text>
        <Badge label={STATUS_LABEL[status]} variant="success" />
      </View>
    </Card>
  );
}

export default function Home() {
  const router = useRouter();
  const { width: windowWidth } = useWindowDimensions();
  const [firstName, setFirstName] = useState<string | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [hasUnreadNotifications, setHasUnreadNotifications] = useState(false);

  const cardWidth = Math.round(windowWidth * CARD_WIDTH_RATIO);
  const heroLines = useMemo(() => buildHeroLines(firstName), [firstName]);

  // Resolve the first name for the hero greeting: fresh from the server, falling
  // back to the SecureStore cache offline. Stays null when unknown so the
  // "Hi, {name}" line is skipped entirely rather than greeting a blank.
  useEffect(() => {
    let mounted = true;
    (async () => {
      try {
        const me = await getMe();
        if (!mounted) return;
        if (!me.auth) {
          router.replace("/(auth)/login");
          return;
        }
        const first = me.name.trim().split(/\s+/)[0] || null;
        setFirstName(first);
        if (first) SecureStore.setItemAsync("hora_user_name", first).catch(() => {});
      } catch (e) {
        if (e instanceof ApiError && e.isAuthError) {
          router.replace("/(auth)/login");
          return;
        }
        const cached = await SecureStore.getItemAsync("hora_user_name").catch(() => null);
        if (mounted && cached) setFirstName(cached);
      }
    })();
    return () => {
      mounted = false;
    };
  }, [router]);

  const loadTasks = useCallback(async () => {
    try {
      const posted = await getPostedTasks();
      // getPostedTasks() already guarantees an array, but never let a non-array
      // reach state regardless — a screen crash should never be one API change away.
      setTasks(Array.isArray(posted) ? posted : []);
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setError(e instanceof Error ? e.message : "Couldn't load your tasks");
    } finally {
      setLoading(false);
    }
  }, [router]);

  const loadUnreadNotifications = useCallback(async () => {
    try {
      const unread = await getNotifications({ unread: true, limit: 1 });
      setHasUnreadNotifications(unread.length > 0);
    } catch {
      // Non-critical: leave the badge as it was rather than surface an error.
    }
  }, []);

  useFocusEffect(
    useCallback(() => {
      loadTasks();
      loadUnreadNotifications();
    }, [loadTasks, loadUnreadNotifications])
  );

  async function onRefresh() {
    setRefreshing(true);
    await loadTasks();
    setRefreshing(false);
  }

  function goToPostTask(category?: TaskCategory) {
    if (category) {
      router.push({ pathname: "/post-task", params: { category } });
    } else {
      router.push("/post-task");
    }
  }

  return (
    <Screen
      insetForTabBar
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
      }
    >
      <View className="mb-6 mt-4 h-11 items-center justify-center">
        <Logo height={36} />
        <PressableScale
          onPress={() => router.push("/notifications")}
          className="absolute right-0 top-0 h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <View>
            <Bell color={color.ink} size={22} strokeWidth={size.iconStroke} />
            {hasUnreadNotifications ? (
              <View className="absolute -right-0.5 -top-0.5 h-2 w-2 rounded-pill bg-brand" />
            ) : null}
          </View>
        </PressableScale>
      </View>

      <PressableScale
        onPress={() => goToPostTask()}
        className="mb-8 flex-row items-center justify-between rounded-card bg-ink p-6"
      >
        <RotatingText lines={heroLines} />
        <ArrowRight color={color.white} size={20} strokeWidth={size.iconStroke} />
      </PressableScale>

      <Text className="mb-3 text-title font-semibold text-ink">For you</Text>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        className="-mx-6 mb-8"
        contentContainerStyle={{
          paddingHorizontal: space[6],
          gap: space[4],
          alignItems: "flex-start",
        }}
      >
        {HOME_CATEGORIES.map((value) => (
          <CategoryCircle key={value} category={value} onPress={() => goToPostTask(value)} />
        ))}
      </ScrollView>

      <Text className="mb-3 text-title font-semibold text-ink">Get to know HO:RA</Text>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        decelerationRate="fast"
        snapToInterval={cardWidth + space[3]}
        snapToAlignment="start"
        className="-mx-6 mb-8"
        contentContainerStyle={{ paddingHorizontal: space[6], gap: space[3] }}
      >
        {HOME_CARDS.map((card) => (
          <NewsCard
            key={card.slug}
            card={card}
            width={cardWidth}
            onPress={card.action ? () => router.push(card.action!) : undefined}
          />
        ))}
      </ScrollView>

      <Text className="mb-3 text-title font-semibold text-ink">Your tasks</Text>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
          <Skeleton className="h-[84px]" />
        </View>
      ) : error && tasks.length === 0 ? (
        <EmptyState
          icon={CircleAlert}
          title="Couldn't load your tasks"
          caption={error}
          actionLabel="Retry"
          onAction={loadTasks}
        />
      ) : tasks.length === 0 ? (
        <EmptyState
          icon={ClipboardList}
          title="No active tasks"
          caption="Post your first task and get matched in minutes"
          actionLabel="Post a task"
          onAction={() => goToPostTask()}
        />
      ) : (
        <View>
          {tasks.map((task) => (
            <TaskRow key={task.id} task={task} onPress={() => router.push(`/task/${task.id}`)} />
          ))}
        </View>
      )}
    </Screen>
  );
}
