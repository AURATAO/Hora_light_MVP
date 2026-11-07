import { useCallback, useRef, useState } from "react";
import { Linking, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import { BellRing, Briefcase, MapPin } from "lucide-react-native";
import { Button, Card, PressableScale } from "./ui";
import { getDispatchSettings, setAvailability, type DispatchSettings } from "../lib/api";
import { ensurePushForOffers } from "../lib/push";
import { color, size } from "../theme/tokens";

// "I'M AVAILABLE" (D-24). While it is on, new tasks are pushed to this phone:
// the supporter's own boroughs first, everyone else three minutes later. It
// switches itself off after four hours (the server sets available_until), so a
// supporter who forgets is not paged at 3 a.m.
//
// Rendered only for an approved supporter and only when the server says
// dispatch is on (GET /config) — the parent decides that. Going available
// first makes sure notifications can reach this phone, since an offer that
// cannot arrive is no offer at all.

function formatUntil(iso: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

function areasLine(s: DispatchSettings): string {
  if (s.boroughs.length === 0) return "All of NYC · Choose your areas";
  return s.boroughs.join(", ");
}

export function AvailabilityCard() {
  const router = useRouter();
  const [settings, setSettings] = useState<DispatchSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pushBlocked, setPushBlocked] = useState(false);
  // Every read and write takes a ticket; only the latest one may set state,
  // so a focus refetch that started before a toggle cannot land after it and
  // flip the card back.
  const seq = useRef(0);

  // A toggle in flight owns the card: a read started meanwhile may have been
  // served before the write landed, so it is not taken at all.
  const toggling = useRef(false);

  const load = useCallback(async () => {
    if (toggling.current) return;
    const ticket = ++seq.current;
    try {
      const s = await getDispatchSettings();
      if (ticket !== seq.current) return;
      setSettings(s);
      setError(null);
    } catch {
      if (ticket !== seq.current) return;
      // The card is an extra on the feed: a failed read hides it rather than
      // putting an error above the tasks.
      setSettings(null);
    }
  }, []);

  // Re-read on focus: availability expires on the server's clock, and the
  // areas screen may have just changed the boroughs.
  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  async function toggle() {
    if (busy || !settings) return;
    const turningOn = !settings.available;
    toggling.current = true;
    setBusy(true);
    setError(null);
    try {
      if (turningOn) {
        const push = await ensurePushForOffers();
        if (push === "denied") {
          setPushBlocked(true);
          return;
        }
        setPushBlocked(false);
        if (push === "failed") {
          setError("Couldn't set up notifications on this phone. Check your connection and try again.");
          return;
        }
      }
      const ticket = ++seq.current;
      const next = await setAvailability(turningOn);
      if (ticket === seq.current) setSettings(next);
    } catch {
      setError(turningOn ? "Couldn't go available. Try again in a moment." : "Couldn't go offline. Try again in a moment.");
    } finally {
      toggling.current = false;
      setBusy(false);
    }
  }

  if (!settings) return null;

  const on = settings.available;
  const until = formatUntil(settings.available_until);
  // Available, but on a job: the server holds back offers that clash with
  // it, so "You're available" alone would promise notifications that don't
  // come.
  const onTask = on && settings.on_task_id ? settings.on_task_id : null;
  const Icon = onTask ? Briefcase : BellRing;

  return (
    <View className="mb-6">
      <Card>
        <View className="flex-row items-start gap-3">
          <Icon color={on ? color.brand : color.muted} size={18} strokeWidth={size.iconStroke} />
          <View className="flex-1">
            <Text className="text-body font-semibold text-ink">
              {onTask ? "You're on a task" : on ? "You're available" : "Get task offers"}
            </Text>
            <Text className="mt-1 text-caption text-muted">
              {onTask
                ? "Offers that clash with it are paused. You'll get them again when it's done."
                : on
                  ? `New tasks come to you as notifications until ${until || "later today"}.`
                  : "Go available and new tasks come to you as notifications. It turns off after 4 hours."}
            </Text>
            {onTask ? (
              <PressableScale
                onPress={() => router.push(`/task/${onTask}`)}
                hitSlop={8}
                className="mt-2 min-h-[44px] justify-center"
              >
                <Text className="text-caption font-semibold text-brand">Open task</Text>
              </PressableScale>
            ) : null}
            <PressableScale
              onPress={() => router.push("/profile/areas")}
              hitSlop={8}
              className="mt-2 min-h-[44px] flex-row items-center gap-1"
            >
              <MapPin color={color.brand} size={14} strokeWidth={size.iconStroke} />
              <Text className="flex-1 text-caption font-semibold text-brand" numberOfLines={1}>
                {areasLine(settings)}
              </Text>
            </PressableScale>
          </View>
        </View>
      </Card>

      <View className="mt-3">
        <Button
          label={on ? "Go offline" : "Go available"}
          variant={on ? "secondary" : "primary"}
          onPress={toggle}
          loading={busy}
        />
      </View>

      {pushBlocked ? (
        <PressableScale onPress={() => Linking.openSettings()} hitSlop={8} className="mt-2 min-h-[44px] justify-center">
          <Text className="text-caption text-danger">
            Notifications are off, so offers can't reach you. Turn them on in Settings.
          </Text>
        </PressableScale>
      ) : null}
      {error ? <Text className="mt-2 text-caption text-danger">{error}</Text> : null}
    </View>
  );
}
