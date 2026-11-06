import { useCallback, useState } from "react";
import { Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import { ChevronLeft, MapPin } from "lucide-react-native";
import { BoroughPicker } from "../../components/BoroughPicker";
import { Button, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, getDispatchSettings, saveDispatchBoroughs } from "../../lib/api";
import { color, size } from "../../theme/tokens";

/**
 * Profile → Task areas. The boroughs a supporter hears about first (D-24):
 * a new task goes to supporters who serve its borough, then to everyone else
 * eligible three minutes later.
 */
export default function TaskAreas() {
  const router = useRouter();
  const [all, setAll] = useState<string[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [saved, setSaved] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const s = await getDispatchSettings();
      setAll(s.all_boroughs);
      setSelected(s.boroughs);
      setSaved(s.boroughs);
      setLoadError(null);
    } catch (e) {
      if (e instanceof ApiError && e.isAuthError) {
        router.replace("/(auth)/login");
        return;
      }
      setLoadError("Couldn't load your areas. Check your connection and try again.");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  const dirty = [...selected].sort().join("|") !== [...saved].sort().join("|");

  async function save() {
    if (saving) return;
    setSaving(true);
    setSaveError(null);
    try {
      const s = await saveDispatchBoroughs(selected);
      setSelected(s.boroughs);
      setSaved(s.boroughs);
      router.back();
    } catch {
      setSaveError("Couldn't save your areas. Try again in a moment.");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Screen>
      <PressableScale
        onPress={() => router.back()}
        hitSlop={8}
        accessibilityRole="button"
        accessibilityLabel="Back"
        className="mb-4 mt-2 min-h-11 flex-row items-center"
      >
        <ChevronLeft color={color.ink} size={20} strokeWidth={size.iconStroke} />
        <Text className="text-body text-ink">Back</Text>
      </PressableScale>

      <Text className="mb-2 text-display text-ink">Task areas</Text>
      <Text className="mb-6 text-body text-muted">
        You hear about new tasks in these boroughs first. Tasks elsewhere still reach you a few minutes later.
      </Text>

      {loading ? (
        <View className="flex-row flex-wrap gap-2">
          <Skeleton className="h-9 w-28" />
          <Skeleton className="h-9 w-24" />
          <Skeleton className="h-9 w-24" />
        </View>
      ) : loadError ? (
        <EmptyState icon={MapPin} title="Couldn't load your areas" caption={loadError} actionLabel="Retry" onAction={load} />
      ) : (
        <>
          <BoroughPicker selected={selected} onChange={setSelected} options={all.length ? all : undefined} />
          <View className="mt-8">
            <Button label="Save areas" onPress={save} loading={saving} disabled={!dirty} />
            {saveError ? <Text className="mt-2 text-caption text-danger">{saveError}</Text> : null}
          </View>
        </>
      )}
    </Screen>
  );
}
