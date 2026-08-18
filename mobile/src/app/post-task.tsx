import { useEffect, useRef, useState } from "react";
import { ScrollView, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import { Check, ChevronLeft, Sparkles, X } from "lucide-react-native";
import { BetaNoticeSheet } from "../components/BetaNoticeSheet";
import { CompanionshipPolicySheet } from "../components/CompanionshipPolicySheet";
import {
  TaskForm,
  emptyTaskForm,
  taskFormFromParsed,
  taskFormToPayload,
  validateTaskForm,
  type TaskFormErrors,
  type TaskFormState,
} from "../components/TaskForm";
import { Button, Input, Pill, PressableScale, Screen } from "../components/ui";
import { ApiError, createTask, parseTask, updateProfile } from "../lib/api";
import { DISABLED_CATEGORY_NOTICE, isCategoryDisabled } from "../lib/beta-notice";
import { CATEGORIES, getCategoryMeta } from "../lib/categories";
import { POST_TASK_AI_HINT, POST_TASK_AI_HINT_COPY } from "../lib/home-content";
import { useBetaNoticeGate } from "../lib/use-beta-notice-gate";
import { useCompanionshipGate } from "../lib/use-companionship-gate";
import type { TaskCategory } from "../lib/types";
import { color, size } from "../theme/tokens";
import { useAuthState } from "./_layout";

type Step = "describe" | "review" | "success";

function parseCategory(raw: string | string[] | undefined): TaskCategory | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  if (!CATEGORIES.some((c) => c.value === value)) return undefined;
  // Home's shortcut row (and any future entry point) may still pass the legacy
  // "companionship" value — normalize it to "companion" so every path produces
  // the same category values web does (see TaskForm's CATEGORY_PICKS comment).
  const category = value === "companionship" ? "companion" : (value as TaskCategory);
  // Home greys its locked circles out, but the route takes a param from
  // anywhere — an old deep link, a notification. Arrive with no category
  // rather than one that can't be posted.
  return isCategoryDisabled(category) ? undefined : category;
}

export default function PostTask() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const { refresh } = useAuthState();
  const initialCategory = parseCategory(params.category);

  // Only the "Post in seconds" education card passes ?hint=ai, so the tip
  // banner is scoped to that entry point (the hero card and Home's category
  // circles push without it). Dismissal lasts for this Post Task session.
  const hintParam = Array.isArray(params.hint) ? params.hint[0] : params.hint;
  const [aiHintVisible, setAiHintVisible] = useState(hintParam === POST_TASK_AI_HINT);

  const [step, setStep] = useState<Step>("describe");
  const [selectedCategory, setSelectedCategory] = useState<TaskCategory | undefined>(initialCategory);
  const [describeText, setDescribeText] = useState("");
  const [parsing, setParsing] = useState(false);
  const [parseError, setParseError] = useState<string | null>(null);

  const [form, setForm] = useState<TaskFormState>(() => emptyTaskForm(initialCategory));
  const [fieldErrors, setFieldErrors] = useState<TaskFormErrors>({});
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const policy = useCompanionshipGate(form.category, { active: step === "review" });
  const beta = useBetaNoticeGate();

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

  // Accepting persists onto the profile the same way the onboarding beta gate
  // does. A failed write is swallowed on purpose (web's BetaModal does the
  // same): during a five-day test window, a flaky PATCH must not lock a
  // requester out of posting — the flag is simply written again next session.
  async function handleAcceptBeta() {
    try {
      await updateProfile({ beta_accepted: true });
      await refresh();
    } catch (e) {
      if (handleAuthError(e)) return;
    }
    beta.acknowledge();
  }

  async function handleContinue() {
    setParseError(null);
    setParsing(true);
    try {
      const parsed = await parseTask(describeText.trim());
      const parsedForm = taskFormFromParsed(parsed, selectedCategory);
      setForm(parsedForm);
      // The parser picks the category itself and has a companionship example in
      // its prompt, so free text can land on a locked one. Flag it on arrival
      // instead of letting the user fill the rest of the form and then bounce.
      setFieldErrors(
        isCategoryDisabled(parsedForm.category) ? { category: DISABLED_CATEGORY_NOTICE } : {}
      );
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
    setForm(emptyTaskForm(selectedCategory));
    setFieldErrors({});
    setStep("review");
  }

  async function handleSubmit() {
    const errors = validateTaskForm(form);
    // Posting is the last line of defence for a locked category: the pickers
    // can't select one, but the AI parser can still hand us one. Scoped to
    // this screen rather than validateTaskForm, so editing a companionship
    // task posted before the lock still saves.
    if (isCategoryDisabled(form.category)) errors.category = DISABLED_CATEGORY_NOTICE;
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;

    // Hard gate: a companionship task cannot be posted until the policy has
    // been acknowledged in this flow, whatever the user dismissed earlier.
    if (policy.needsPolicy) {
      policy.request();
      return;
    }

    setSubmitError(null);
    setSubmitting(true);
    try {
      await createTask(taskFormToPayload(form));
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
    <Screen scroll={false} avoidKeyboard>
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
            {aiHintVisible ? (
              <View className="flex-row items-start gap-3 rounded-card bg-brand-tint p-4">
                <Sparkles color={color.brand} size={18} strokeWidth={size.iconStroke} />
                <Text className="flex-1 text-body text-brand">{POST_TASK_AI_HINT_COPY}</Text>
                <PressableScale onPress={() => setAiHintVisible(false)} hitSlop={12}>
                  <X color={color.brand} size={16} strokeWidth={size.iconStroke} />
                </PressableScale>
              </View>
            ) : null}
            <Input
              multiline
              numberOfLines={6}
              textAlignVertical="top"
              value={describeText}
              onChangeText={setDescribeText}
              placeholder="Describe what you need — e.g. 'Pick up my groceries from Trader Joe's, about an hour'"
              className="h-[160px]"
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
            <TaskForm form={form} onChange={setForm} errors={fieldErrors} />
            {submitError ? <Text className="text-caption text-danger">{submitError}</Text> : null}
            <Button label="Post task" onPress={handleSubmit} loading={submitting} />
          </View>
        )}
      </ScrollView>

      <CompanionshipPolicySheet
        visible={policy.open}
        onDismiss={policy.dismiss}
        onAcknowledge={policy.acknowledge}
      />

      {/* Backing out of the notice leaves Post Task entirely: the terms are a
          gate, not a tip, so there is no path past them into the form. */}
      <BetaNoticeSheet
        visible={beta.open}
        onDismiss={() => {
          beta.dismiss();
          router.back();
        }}
        onAccept={handleAcceptBeta}
      />
    </Screen>
  );
}
