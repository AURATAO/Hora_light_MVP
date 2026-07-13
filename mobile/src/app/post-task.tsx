import { useEffect, useRef, useState } from "react";
import { Platform, ScrollView, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import DateTimePicker, { DateTimePickerAndroid } from "@react-native-community/datetimepicker";
import { Check, ChevronLeft, X } from "lucide-react-native";
import { Button, Input, Pill, PressableScale, Screen } from "../components/ui";
import { ApiError, createTask, parseTask } from "../lib/api";
import { CATEGORIES, getCategoryMeta } from "../lib/categories";
import { formatMinutes } from "../lib/task-utils";
import type { ParsedTask, TaskCategory } from "../lib/types";
import { color, size } from "../theme/tokens";

type Step = "describe" | "review" | "success";

interface FormState {
  title: string;
  category: TaskCategory | undefined;
  description: string;
  locationText: string;
  estimatedMinutes: string;
  isImmediate: boolean;
  scheduledDate: Date;
}

interface FieldErrors {
  title?: string;
  category?: string;
  scheduledAt?: string;
}

const QUICK_MINUTES = [30, 60, 90, 120];

function parseCategory(raw: string | string[] | undefined): TaskCategory | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return CATEGORIES.some((c) => c.value === value) ? (value as TaskCategory) : undefined;
}

function defaultScheduledDate(): Date {
  return new Date(Date.now() + 60 * 60 * 1000);
}

function emptyForm(category?: TaskCategory): FormState {
  return {
    title: "",
    category,
    description: "",
    locationText: "",
    estimatedMinutes: "",
    isImmediate: true,
    scheduledDate: defaultScheduledDate(),
  };
}

function formFromParsed(parsed: ParsedTask, fallbackCategory?: TaskCategory): FormState {
  const location = [parsed.location_1, parsed.location_2].filter(Boolean).join(", ");
  const isImmediate = !parsed.scheduled;

  return {
    title: parsed.title ?? "",
    category: parsed.category ?? fallbackCategory,
    description: parsed.description ?? "",
    locationText: location,
    estimatedMinutes: parsed.duration_minutes ? String(parsed.duration_minutes) : "",
    isImmediate,
    scheduledDate:
      !isImmediate && parsed.scheduled_time ? new Date(parsed.scheduled_time) : defaultScheduledDate(),
  };
}

function validate(form: FormState): FieldErrors {
  const errors: FieldErrors = {};
  if (!form.title.trim()) errors.title = "Title is required.";
  if (!form.category) errors.category = "Choose a category.";
  if (!form.isImmediate && form.scheduledDate.getTime() <= Date.now()) {
    errors.scheduledAt = "Pick a time in the future.";
  }
  return errors;
}

function openAndroidDatePicker(current: Date, onPicked: (next: Date) => void) {
  DateTimePickerAndroid.open({
    value: current,
    mode: "date",
    minimumDate: new Date(),
    onChange: (_event, selected) => {
      if (!selected) return;
      const next = new Date(current);
      next.setFullYear(selected.getFullYear(), selected.getMonth(), selected.getDate());
      onPicked(next);
    },
  });
}

function openAndroidTimePicker(current: Date, onPicked: (next: Date) => void) {
  DateTimePickerAndroid.open({
    value: current,
    mode: "time",
    onChange: (_event, selected) => {
      if (!selected) return;
      const next = new Date(current);
      next.setHours(selected.getHours(), selected.getMinutes());
      onPicked(next);
    },
  });
}

export default function PostTask() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const initialCategory = parseCategory(params.category);

  const [step, setStep] = useState<Step>("describe");
  const [selectedCategory, setSelectedCategory] = useState<TaskCategory | undefined>(initialCategory);
  const [describeText, setDescribeText] = useState("");
  const [parsing, setParsing] = useState(false);
  const [parseError, setParseError] = useState<string | null>(null);

  const [form, setForm] = useState<FormState>(() => emptyForm(initialCategory));
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const closeTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    return () => {
      if (closeTimeout.current) clearTimeout(closeTimeout.current);
    };
  }, []);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  async function handleContinue() {
    setParseError(null);
    setParsing(true);
    try {
      const parsed = await parseTask(describeText.trim());
      setForm(formFromParsed(parsed, selectedCategory));
      setFieldErrors({});
      setStep("review");
    } catch (e) {
      if (handleAuthError(e)) return;
      setParseError(
        e instanceof Error ? e.message : "Couldn't understand that — try rephrasing or fill manually."
      );
    } finally {
      setParsing(false);
    }
  }

  function handleFillManually() {
    setForm(emptyForm(selectedCategory));
    setFieldErrors({});
    setStep("review");
  }

  async function handleSubmit() {
    const errors = validate(form);
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;

    setSubmitError(null);
    setSubmitting(true);
    try {
      await createTask({
        title: form.title.trim(),
        description: form.description.trim() || undefined,
        category: form.category!,
        location_text: form.locationText.trim() || undefined,
        estimated_minutes: form.estimatedMinutes ? Number(form.estimatedMinutes) : undefined,
        prepay_amount_cents: 0,
        is_immediate: form.isImmediate,
        scheduled_at: form.isImmediate ? "" : form.scheduledDate.toISOString(),
        transport_required: "none",
      });
      setStep("success");
      closeTimeout.current = setTimeout(() => router.back(), 900);
    } catch (e) {
      if (handleAuthError(e)) return;
      setSubmitError(e instanceof Error ? e.message : "Couldn't post your task. Try again.");
    } finally {
      setSubmitting(false);
    }
  }

  if (step === "success") {
    return (
      <Screen scroll={false}>
        <View className="flex-1 items-center justify-center gap-3">
          <Check color={color.brand} size={32} strokeWidth={size.iconStroke} />
          <Text className="text-title font-semibold text-ink">Task posted</Text>
          <Text className="text-caption text-muted">Back to your tasks…</Text>
        </View>
      </Screen>
    );
  }

  return (
    <Screen scroll={false}>
      <View className="mb-6 mt-4 flex-row items-center justify-between">
        <View className="flex-row items-center">
          {step === "review" ? (
            <PressableScale
              onPress={() => setStep("describe")}
              className="mr-1 h-11 w-11 items-center justify-center rounded-pill"
              hitSlop={8}
            >
              <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
            </PressableScale>
          ) : null}
          <Text className="text-title font-semibold text-ink">Post a task</Text>
        </View>
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <X color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
      </View>

      <ScrollView className="flex-1" keyboardShouldPersistTaps="handled" showsVerticalScrollIndicator={false}>
        {step === "describe" ? (
          <View className="gap-3">
            {selectedCategory ? (
              <Pill
                label={getCategoryMeta(selectedCategory).label}
                selected
                onPress={() => setSelectedCategory(undefined)}
                className="self-start"
              />
            ) : null}
            <Input
              multiline
              numberOfLines={6}
              textAlignVertical="top"
              value={describeText}
              onChangeText={setDescribeText}
              placeholder="Describe what you need — e.g. 'Pick up my groceries from Trader Joe's, about an hour'"
              className="h-[160px] py-3"
            />
            {parseError ? <Text className="text-caption text-danger">{parseError}</Text> : null}
            <Button
              label="Continue"
              onPress={handleContinue}
              loading={parsing}
              disabled={!describeText.trim()}
              className="mt-2"
            />
            <Button label="Fill manually" variant="text" onPress={handleFillManually} />
          </View>
        ) : (
          <View className="gap-4 pb-8">
            <Input
              label="Title"
              value={form.title}
              onChangeText={(title) => setForm((f) => ({ ...f, title }))}
              placeholder="What do you need?"
              error={fieldErrors.title}
            />

            <View>
              <Text className="mb-1 text-caption text-muted">Category</Text>
              <ScrollView horizontal showsHorizontalScrollIndicator={false} className="-mx-6 px-6">
                <View className="flex-row gap-2">
                  {CATEGORIES.map((c) => (
                    <Pill
                      key={c.value}
                      label={c.label}
                      selected={form.category === c.value}
                      onPress={() => setForm((f) => ({ ...f, category: c.value }))}
                    />
                  ))}
                </View>
              </ScrollView>
              {fieldErrors.category ? (
                <Text className="mt-1 text-caption text-danger">{fieldErrors.category}</Text>
              ) : null}
            </View>

            <Input
              label="Description"
              multiline
              numberOfLines={4}
              textAlignVertical="top"
              value={form.description}
              onChangeText={(description) => setForm((f) => ({ ...f, description }))}
              className="h-[100px] py-3"
            />

            <Input
              label="Location"
              value={form.locationText}
              onChangeText={(locationText) => setForm((f) => ({ ...f, locationText }))}
              placeholder="Where?"
            />

            <View>
              <Input
                label="Estimated time (minutes)"
                keyboardType="number-pad"
                value={form.estimatedMinutes}
                onChangeText={(text) =>
                  setForm((f) => ({ ...f, estimatedMinutes: text.replace(/[^0-9]/g, "") }))
                }
                placeholder="60"
              />
              <View className="mt-2 flex-row gap-2">
                {QUICK_MINUTES.map((m) => (
                  <Pill
                    key={m}
                    label={formatMinutes(m)}
                    selected={form.estimatedMinutes === String(m)}
                    onPress={() => setForm((f) => ({ ...f, estimatedMinutes: String(m) }))}
                  />
                ))}
              </View>
            </View>

            <View>
              <Text className="mb-1 text-caption text-muted">When</Text>
              <View className="flex-row gap-2">
                <Pill
                  label="As soon as possible"
                  selected={form.isImmediate}
                  onPress={() => setForm((f) => ({ ...f, isImmediate: true }))}
                />
                <Pill
                  label="Schedule for later"
                  selected={!form.isImmediate}
                  onPress={() => setForm((f) => ({ ...f, isImmediate: false }))}
                />
              </View>
              {!form.isImmediate ? (
                <View className="mt-3">
                  {Platform.OS === "ios" ? (
                    <DateTimePicker
                      value={form.scheduledDate}
                      mode="datetime"
                      display="spinner"
                      minimumDate={new Date()}
                      onValueChange={(_event, date) => setForm((f) => ({ ...f, scheduledDate: date }))}
                    />
                  ) : (
                    <View className="flex-row gap-2">
                      <Button
                        variant="secondary"
                        label={form.scheduledDate.toLocaleDateString()}
                        onPress={() =>
                          openAndroidDatePicker(form.scheduledDate, (next) =>
                            setForm((f) => ({ ...f, scheduledDate: next }))
                          )
                        }
                      />
                      <Button
                        variant="secondary"
                        label={form.scheduledDate.toLocaleTimeString([], {
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                        onPress={() =>
                          openAndroidTimePicker(form.scheduledDate, (next) =>
                            setForm((f) => ({ ...f, scheduledDate: next }))
                          )
                        }
                      />
                    </View>
                  )}
                  {fieldErrors.scheduledAt ? (
                    <Text className="mt-1 text-caption text-danger">{fieldErrors.scheduledAt}</Text>
                  ) : null}
                </View>
              ) : null}
            </View>

            {submitError ? <Text className="text-caption text-danger">{submitError}</Text> : null}

            <Button label="Post task" onPress={handleSubmit} loading={submitting} />
          </View>
        )}
      </ScrollView>
    </Screen>
  );
}
