import { useEffect, useRef, useState } from "react";
import { Platform, ScrollView, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import DateTimePicker, { DateTimePickerAndroid } from "@react-native-community/datetimepicker";
import { Check, ChevronLeft, X } from "lucide-react-native";
import { Button, Input, Pill, PressableScale, Screen } from "../components/ui";
import { ApiError, createTask, estimateTaskCost, parseTask } from "../lib/api";
import { CATEGORIES, getCategoryMeta } from "../lib/categories";
import { formatCost, formatMinutes } from "../lib/task-utils";
import type { ParsedTask, TaskCategory } from "../lib/types";
import { color, size } from "../theme/tokens";

type Step = "describe" | "review" | "success";

type TransportOption = "none" | "car" | "bike" | "public";

interface LocationRow {
  id: number;
  text: string;
}

interface FormState {
  title: string;
  category: TaskCategory | undefined;
  description: string;
  locations: LocationRow[];
  estimatedMinutes: string;
  shoppingBudget: string;
  transport: TransportOption;
  isImmediate: boolean;
  scheduledDate: Date;
}

interface FieldErrors {
  title?: string;
  category?: string;
  scheduledAt?: string;
  shoppingBudget?: string;
}

const QUICK_MINUTES = [30, 60, 90, 120];

const TRANSPORT_OPTIONS: { value: TransportOption; label: string }[] = [
  { value: "none", label: "None" },
  { value: "car", label: "Car" },
  { value: "bike", label: "Bike" },
  { value: "public", label: "Public transport is fine" },
];

const MAX_LOCATIONS = 3;

// Same 6 categories, same order, as web's category picker (app/src/pages/CategoryHome.jsx
// CATEGORIES) — "companionship" there is a UI-only label that web always normalizes to
// the submitted category "companion" (app/src/pages/NewTask.jsx onSubmit); mirrored here
// so both clients ever produce the same category values.
const CATEGORY_PICKS: TaskCategory[] = [
  "delivery",
  "grocery",
  "laundry",
  "companion",
  "queue",
  "anything_else",
];

function parseCategory(raw: string | string[] | undefined): TaskCategory | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  if (!CATEGORIES.some((c) => c.value === value)) return undefined;
  // Home's shortcut row (and any future entry point) may still pass the legacy
  // "companionship" value — normalize it to "companion" so every path produces
  // the same category web does (see CATEGORY_PICKS comment above).
  return value === "companionship" ? "companion" : (value as TaskCategory);
}

function defaultScheduledDate(): Date {
  return new Date(Date.now() + 60 * 60 * 1000);
}

function emptyLocations(): LocationRow[] {
  return [{ id: 0, text: "" }];
}

function emptyForm(category?: TaskCategory): FormState {
  return {
    title: "",
    category,
    description: "",
    locations: emptyLocations(),
    estimatedMinutes: "",
    shoppingBudget: "",
    transport: "none",
    isImmediate: true,
    scheduledDate: defaultScheduledDate(),
  };
}

function formFromParsed(parsed: ParsedTask, fallbackCategory?: TaskCategory): FormState {
  const locations: LocationRow[] = [parsed.location_1, parsed.location_2]
    .filter((text) => text && text.trim())
    .map((text, i) => ({ id: i, text }));

  const isImmediate = !parsed.scheduled;

  return {
    title: parsed.title ?? "",
    category: parsed.category ?? fallbackCategory,
    description: parsed.description ?? "",
    locations: locations.length > 0 ? locations : emptyLocations(),
    estimatedMinutes: parsed.duration_minutes ? String(parsed.duration_minutes) : "",
    shoppingBudget: "",
    transport: "none",
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
  if (form.shoppingBudget !== "") {
    const n = Number(form.shoppingBudget);
    if (Number.isNaN(n) || n < 0) errors.shoppingBudget = "Invalid amount.";
  }
  return errors;
}

// location_text encoding matches web exactly (app/src/pages/NewTask.jsx onSubmit):
// non-empty rows only, joined with " | ", first row is pickup/starting point.
function encodeLocationText(locations: LocationRow[]): string {
  return locations
    .map((l) => l.text.trim())
    .filter(Boolean)
    .join(" | ");
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

  const [estimate, setEstimate] = useState<{
    baseFeeCents: number;
    timeCostCents: number;
    shoppingCents: number;
    totalCents: number;
  } | null>(null);

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

  // Pre-submission price quote — server-computed (S-05), matching web's summary
  // line but sourced from POST /tasks/estimate instead of a client-side formula.
  useEffect(() => {
    if (step !== "review" || !form.category) {
      setEstimate(null);
      return;
    }
    let cancelled = false;
    const id = setTimeout(async () => {
      try {
        const minutes = form.estimatedMinutes ? Number(form.estimatedMinutes) : 0;
        const budget = form.shoppingBudget ? Number(form.shoppingBudget) : 0;
        const prepayCents = Number.isFinite(budget) && budget > 0 ? Math.round(budget * 100) : 0;
        const result = await estimateTaskCost({
          category: form.category as TaskCategory,
          estimated_minutes: Number.isFinite(minutes) ? minutes : 0,
          prepay_amount_cents: prepayCents,
        });
        if (cancelled) return;
        setEstimate({
          baseFeeCents: result.base_fee_cents,
          timeCostCents: result.time_cost_cents,
          shoppingCents: result.shopping_cents,
          totalCents: result.total_cents,
        });
      } catch (e) {
        if (cancelled) return;
        if (handleAuthError(e)) return;
        setEstimate(null);
      }
    }, 300);
    return () => {
      cancelled = true;
      clearTimeout(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [step, form.category, form.estimatedMinutes, form.shoppingBudget]);

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

  function updateLocation(id: number, text: string) {
    setForm((f) => ({ ...f, locations: f.locations.map((l) => (l.id === id ? { ...l, text } : l)) }));
  }

  function addLocation() {
    setForm((f) => {
      if (f.locations.length >= MAX_LOCATIONS) return f;
      const nextId = Math.max(...f.locations.map((l) => l.id)) + 1;
      return { ...f, locations: [...f.locations, { id: nextId, text: "" }] };
    });
  }

  function removeLocation(id: number) {
    setForm((f) => ({ ...f, locations: f.locations.filter((l) => l.id !== id) }));
  }

  async function handleSubmit() {
    const errors = validate(form);
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;

    setSubmitError(null);
    setSubmitting(true);
    try {
      const budget = form.shoppingBudget ? Number(form.shoppingBudget) : 0;
      const prepayCents = Number.isFinite(budget) && budget > 0 ? Math.round(budget * 100) : 0;

      await createTask({
        title: form.title.trim(),
        description: form.description.trim() || undefined,
        category: form.category!,
        location_text: encodeLocationText(form.locations) || undefined,
        estimated_minutes: form.estimatedMinutes ? Number(form.estimatedMinutes) : undefined,
        prepay_amount_cents: prepayCents,
        is_immediate: form.isImmediate,
        scheduled_at: form.isImmediate ? "" : form.scheduledDate.toISOString(),
        transport_required: form.transport,
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

  const pickerCategories =
    form.category && !CATEGORY_PICKS.includes(form.category)
      ? [...CATEGORY_PICKS, form.category]
      : CATEGORY_PICKS;

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
                  {pickerCategories.map((value) => (
                    <Pill
                      key={value}
                      label={getCategoryMeta(value).label}
                      selected={form.category === value}
                      onPress={() => setForm((f) => ({ ...f, category: value }))}
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

            <View className="gap-3">
              {form.locations.map((loc, i) => (
                <View key={loc.id} className="gap-1">
                  <View className="flex-row items-center justify-between">
                    <Text className="text-caption text-muted">
                      {i === 0 ? "Pick-up / starting point" : `Drop-off / stop ${i + 1}`}
                    </Text>
                    {i > 0 ? (
                      <PressableScale onPress={() => removeLocation(loc.id)} hitSlop={8}>
                        <Text className="text-caption text-muted">Remove</Text>
                      </PressableScale>
                    ) : null}
                  </View>
                  {/* TODO(places): plain text for now — Google Places autocomplete needs
                      EXPO_PUBLIC_GOOGLE_PLACES_KEY wired to the Places API (New) REST
                      endpoints (no RN SDK required, see mobile/.env.example). Deferred so
                      it doesn't block the rest of Post Task parity. */}
                  <Input
                    value={loc.text}
                    onChangeText={(text) => updateLocation(loc.id, text)}
                    placeholder="Street address"
                  />
                </View>
              ))}
              {form.locations.length < MAX_LOCATIONS ? (
                <Button label="Add drop-off / stop" variant="text" onPress={addLocation} />
              ) : null}
            </View>

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

            <Input
              label="Shopping budget ($)"
              keyboardType="decimal-pad"
              value={form.shoppingBudget}
              onChangeText={(text) => setForm((f) => ({ ...f, shoppingBudget: text.replace(/[^0-9.]/g, "") }))}
              placeholder="e.g. 12.50"
              error={fieldErrors.shoppingBudget}
            />

            <View>
              <Text className="mb-1 text-caption text-muted">Helper needs a…</Text>
              <View className="flex-row flex-wrap gap-2">
                {TRANSPORT_OPTIONS.map((opt) => (
                  <Pill
                    key={opt.value}
                    label={opt.label}
                    selected={form.transport === opt.value}
                    onPress={() => setForm((f) => ({ ...f, transport: opt.value }))}
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

            {estimate ? (
              <View className="gap-1 rounded-card border border-line bg-surface p-4">
                <View className="flex-row justify-between">
                  <Text className="text-caption text-muted">Start</Text>
                  <Text className="text-caption text-ink">
                    {form.isImmediate ? "ASAP" : form.scheduledDate.toLocaleString()}
                  </Text>
                </View>
                <View className="flex-row justify-between">
                  <Text className="text-caption text-muted">Base fee</Text>
                  <Text className="text-caption text-ink">{formatCost(estimate.baseFeeCents)}</Text>
                </View>
                <View className="flex-row justify-between">
                  <Text className="text-caption text-muted">
                    Time ({form.estimatedMinutes || 0} min × $0.50)
                  </Text>
                  <Text className="text-caption text-ink">{formatCost(estimate.timeCostCents)}</Text>
                </View>
                {estimate.shoppingCents > 0 ? (
                  <View className="flex-row justify-between">
                    <Text className="text-caption text-muted">Shopping</Text>
                    <Text className="text-caption text-ink">{formatCost(estimate.shoppingCents)}</Text>
                  </View>
                ) : null}
                <View className="mt-1 flex-row justify-between border-t border-line pt-1">
                  <Text className="text-caption font-semibold text-ink">Total estimate</Text>
                  <Text className="text-caption font-semibold text-ink">
                    {formatCost(estimate.totalCents)}
                  </Text>
                </View>
              </View>
            ) : null}

            {submitError ? <Text className="text-caption text-danger">{submitError}</Text> : null}

            <Button label="Post task" onPress={handleSubmit} loading={submitting} />
          </View>
        )}
      </ScrollView>
    </Screen>
  );
}
