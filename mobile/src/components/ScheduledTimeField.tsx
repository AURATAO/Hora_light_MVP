import { useState } from "react";
import { Modal, Platform, Pressable, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import DateTimePicker, { DateTimePickerAndroid } from "@react-native-community/datetimepicker";
import { Button, PressableScale } from "./ui";
import { formatScheduledAt, zeroSeconds } from "../lib/task-utils";
import { space } from "../theme/tokens";

export interface ScheduledTimeFieldProps {
  value: Date;
  onChange: (next: Date) => void;
  error?: string;
}

// Minute-precision only (DESIGN + task spec): the picker offers 5-minute steps
// and every committed value has its seconds/ms zeroed.
const MINUTE_INTERVAL = 5;

// Android's date/time pickers are already modal dialogs with a native OK/Cancel,
// so the value is locked until deliberately reopened — no extra confirm needed.
// Only the two field buttons live inline; the dialogs open on press.
function openAndroidDatePicker(current: Date, onPicked: (next: Date) => void) {
  DateTimePickerAndroid.open({
    value: current,
    mode: "date",
    minimumDate: new Date(),
    onChange: (_event, selected) => {
      if (!selected) return;
      const next = new Date(current);
      next.setFullYear(selected.getFullYear(), selected.getMonth(), selected.getDate());
      onPicked(zeroSeconds(next));
    },
  });
}

function openAndroidTimePicker(current: Date, onPicked: (next: Date) => void) {
  DateTimePickerAndroid.open({
    value: current,
    mode: "time",
    minuteInterval: MINUTE_INTERVAL,
    onChange: (_event, selected) => {
      if (!selected) return;
      const next = new Date(current);
      next.setHours(selected.getHours(), selected.getMinutes());
      onPicked(zeroSeconds(next));
    },
  });
}

// Scheduled-time field with explicit confirmation. iOS previously rendered a
// bare inline spinner in the form: it stayed in selection mode, so any stray
// swipe silently changed the committed value. Now iOS shows a tappable summary
// row and opens a bottom sheet holding a *draft* — swiping only moves the draft,
// and the outer value changes only when the user taps Done (Cancel/backdrop
// discards). Android keeps its native modal date/time dialogs, which already
// confirm.
export function ScheduledTimeField({ value, onChange, error }: ScheduledTimeFieldProps) {
  const insets = useSafeAreaInsets();
  const [sheetOpen, setSheetOpen] = useState(false);
  const [draft, setDraft] = useState<Date>(value);

  function openSheet() {
    setDraft(zeroSeconds(value));
    setSheetOpen(true);
  }

  function confirm() {
    onChange(zeroSeconds(draft));
    setSheetOpen(false);
  }

  return (
    <View className="mt-3">
      {Platform.OS === "ios" ? (
        <>
          <PressableScale
            onPress={openSheet}
            className="h-[52px] flex-row items-center justify-between rounded-sm border border-line bg-surface px-4"
          >
            <Text className="text-body text-ink">{formatScheduledAt(value)}</Text>
            <Text className="text-caption text-brand">Change</Text>
          </PressableScale>

          <Modal visible={sheetOpen} transparent animationType="slide" onRequestClose={() => setSheetOpen(false)}>
            <View className="flex-1 justify-end">
              <Pressable className="absolute inset-0 bg-ink/40" onPress={() => setSheetOpen(false)} />
              <View
                className="rounded-t-card bg-surface"
                style={{ paddingBottom: Math.max(insets.bottom, space[6]) }}
              >
                <View className="flex-row items-center justify-between border-b border-line px-6 py-4">
                  <PressableScale onPress={() => setSheetOpen(false)} hitSlop={8}>
                    <Text className="text-body text-muted">Cancel</Text>
                  </PressableScale>
                  <Text className="text-body font-semibold text-ink">Schedule</Text>
                  <PressableScale onPress={confirm} hitSlop={8}>
                    <Text className="text-body font-semibold text-brand">Done</Text>
                  </PressableScale>
                </View>
                <View className="items-center px-6 pt-2">
                  <DateTimePicker
                    value={draft}
                    mode="datetime"
                    display="spinner"
                    minuteInterval={MINUTE_INTERVAL}
                    minimumDate={new Date()}
                    onValueChange={(_event, date) => setDraft(date)}
                  />
                </View>
              </View>
            </View>
          </Modal>
        </>
      ) : (
        <View className="flex-row gap-2">
          <Button
            variant="secondary"
            label={value.toLocaleDateString()}
            onPress={() => openAndroidDatePicker(value, onChange)}
          />
          <Button
            variant="secondary"
            label={value.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
            onPress={() => openAndroidTimePicker(value, onChange)}
          />
        </View>
      )}
      {error ? <Text className="mt-1 text-caption text-danger">{error}</Text> : null}
    </View>
  );
}
