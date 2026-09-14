import { useCallback, useEffect, useRef, useState } from "react";
import { ScrollView, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import { Check, ChevronLeft, CreditCard, Sparkles, X } from "lucide-react-native";
import { BetaNoticeSheet } from "../components/BetaNoticeSheet";
import { CompanionshipPolicySheet } from "../components/CompanionshipPolicySheet";
import {
  TaskForm,
  emptyTaskForm,
  taskFormForDuplicate,
  taskFormFromParsed,
  taskFormToPayload,
  validateTaskForm,
  type TaskFormErrors,
  type TaskFormState,
} from "../components/TaskForm";
import { Button, Card, Input, Pill, PressableScale, Screen, Skeleton } from "../components/ui";
import { ApiError, createTask, getMe, getTask, parseTask, updateProfile } from "../lib/api";
import {
  completeCardAuthentication,
  getPaymentMethods,
  readPostFailure,
} from "../lib/payments";
import { DISABLED_CATEGORY_NOTICE, isCategoryDisabled } from "../lib/beta-notice";
import { CATEGORIES, getCategoryMeta } from "../lib/categories";
import { POST_TASK_AI_HINT, POST_TASK_AI_HINT_COPY } from "../lib/home-content";
import { useBetaNoticeGate } from "../lib/use-beta-notice-gate";
import { useCompanionshipGate } from "../lib/use-companionship-gate";
import type { TaskCategory, TaskCreatedVia } from "../lib/types";
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

function firstParam(raw: string | string[] | undefined): string | undefined {
  const value = Array.isArray(raw) ? raw[0] : raw;
  return value?.trim() ? value : undefined;
}

// Shown when ?duplicate= names a task this account didn't post, or one that
// can't be read. The route is deep-linkable and the entry points that use it
// are requester-only, so this is the reachable-but-not-expected case: fall back
// to an ordinary Post Task rather than to an error screen.
const DUPLICATE_UNAVAILABLE = "Couldn't reuse that task — start a new one below.";

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
  // A payment refusal, kept apart from submitError because it comes with an
  // action ("Add a card") that a generic error does not.
  const [paymentError, setPaymentError] = useState<string | null>(null);

  // Advisory gate. POST /tasks answers 402 whatever this says; it exists so a
  // requester learns they need a card BEFORE filling in the form, and so that
  // beta users see nothing about payments at all while the flag is off.
  const [needsCard, setNeedsCard] = useState(false);

  // "Post again": the source task's id, not its fields — the form fetches and
  // maps it here rather than having a whole Task serialized through navigation.
  // The result is a plain new task; nothing links it back to `duplicateId`.
  const duplicateId = firstParam(params.duplicate);
  const [prefilling, setPrefilling] = useState(duplicateId !== undefined);
  const [prefillError, setPrefillError] = useState<string | null>(null);
  // Which path filled the form, for the October repeat-usage count. Set at each
  // entry into the review step, read once at submit.
  const [origin, setOrigin] = useState<TaskCreatedVia>("form");

  const policy = useCompanionshipGate(form.category, { active: step === "review" });
  const beta = useBetaNoticeGate();

  const closeTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    return () => {
      if (closeTimeout.current) clearTimeout(closeTimeout.current);
    };
  }, []);

  // Prefill from a past task and land straight on the structured form — no AI
  // parse step. The user asked for last time's task, not a re-reading of it.
  //
  // The ownership re-check is not redundant with the entry points: both of them
  // are requester-only already, but this route takes its param from anywhere,
  // and GET /tasks/:id is readable by the assignee too. A supporter must never
  // be able to re-post a task they merely worked on, deep link or not.
  useEffect(() => {
    if (!duplicateId) return;
    let cancelled = false;
    (async () => {
      try {
        const [me, task] = await Promise.all([getMe(), getTask(duplicateId)]);
        if (cancelled) return;
        if (!me.auth) {
          router.replace("/(auth)/login");
          return;
        }
        if (task.requester_id !== me.id) {
          setPrefillError(DUPLICATE_UNAVAILABLE);
          return;
        }
        setForm(taskFormForDuplicate(task));
        setOrigin("duplicate");
        // No arrival error even when the source category was locked:
        // taskFormForDuplicate leaves it unselected, so nothing is wrong yet —
        // the picker shows its normal disabled state and validateTaskForm asks
        // for a category on submit like it would on any other empty form.
        setFieldErrors({});
        setStep("review");
      } catch (e) {
        if (cancelled) return;
        if (handleAuthError(e)) return;
        setPrefillError(DUPLICATE_UNAVAILABLE);
      } finally {
        if (!cancelled) setPrefilling(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [duplicateId]);

  // Re-checked every time the review step is entered rather than once on
  // mount: the requester can leave for Payment methods, add a card, and come
  // back, and the banner has to be gone when they do.
  const refreshPaymentGate = useCallback(async () => {
    try {
      const res = await getPaymentMethods();
      setNeedsCard(res.payments_enforced && !res.has_card);
    } catch {
      // A 503 (no Stripe configured) or any other failure means nothing is
      // being enforced that this screen can usefully warn about. The 402 from
      // POST /tasks remains the only thing that actually gates a post.
      setNeedsCard(false);
    }
  }, []);

  useEffect(() => {
    if (step === "review") refreshPaymentGate();
  }, [step, refreshPaymentGate]);

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
      setOrigin("ai_parse");
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
    setOrigin("form");
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
    setPaymentError(null);
    setSubmitting(true);
    try {
      await createTask(taskFormToPayload(form, origin));
      finishPosted();
    } catch (e) {
      if (handleAuthError(e)) return;
      const failure = readPostFailure(e);

      // A bank that wants the cardholder present. The task already exists on
      // the server, parked and invisible to everyone; running the challenge
      // and confirming is what posts it. Nothing here can post a task on its
      // own — the backend re-reads the intent from Stripe first.
      if (failure.kind === "authenticate") {
        try {
          const outcome = await completeCardAuthentication(failure.payment);
          if (outcome.status === "done") {
            finishPosted();
          } else if (outcome.status === "failed") {
            setPaymentError(outcome.message);
          } else {
            // Dismissed the bank's sheet. Nothing was posted and nothing is
            // wrong; say what to do rather than showing an error.
            setPaymentError("Your bank didn't confirm that payment. Try posting again.");
          }
        } catch (authErr) {
          if (handleAuthError(authErr)) return;
          setPaymentError(
            authErr instanceof Error ? authErr.message : "That payment wasn't approved."
          );
        }
        return;
      }

      if (failure.kind === "card") {
        setPaymentError(failure.message);
        refreshPaymentGate();
        return;
      }

      setSubmitError(failure.message);
    } finally {
      setSubmitting(false);
    }
  }

  function finishPosted() {
    setStep("success");
    closeTimeout.current = setTimeout(() => router.back(), 900);
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
          {/* No back arrow on a duplicate: the form IS the entry point there,
              so there is no describe step behind it to return to — and the
              chevron would only offer to throw the prefill away. Keyed on
              `origin` rather than on the param, so a ?duplicate= that failed to
              prefill drops the user on a normal, fully navigable Post Task. */}
          {step === "review" && origin !== "duplicate" ? (
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
        {prefilling ? (
          /* Blocks in the shape of the form that is about to replace them —
             skeletons, never a spinner (DESIGN.md §4). */
          <View className="gap-3">
            <Skeleton className="h-8 w-2/3" />
            <Skeleton className="h-[120px]" />
            <Skeleton className="h-[80px]" />
          </View>
        ) : step === "describe" ? (
          <View className="gap-3">
            {prefillError ? (
              <Text className="text-caption text-danger">{prefillError}</Text>
            ) : null}
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

            {/* Both of these render nothing while PAYMENTS_ENFORCED is off,
                which is every beta session today. */}
            {needsCard ? (
              <Card>
                <View className="flex-row items-center gap-3">
                  <CreditCard color={color.muted} size={18} strokeWidth={size.iconStroke} />
                  <View className="flex-1">
                    <Text className="text-body font-semibold text-ink">Add a card to post</Text>
                    <Text className="mt-0.5 text-caption text-muted">
                      Posting places a hold. You're only charged for the time actually worked.
                    </Text>
                  </View>
                </View>
                <Button
                  label="Add a card"
                  variant="secondary"
                  onPress={() => router.push("/profile/payment-methods")}
                  className="mt-3"
                />
              </Card>
            ) : null}

            {paymentError ? (
              <Card>
                <Text className="text-body text-danger">{paymentError}</Text>
                <Button
                  label="Try another card"
                  variant="secondary"
                  onPress={() => router.push("/profile/payment-methods")}
                  className="mt-3"
                />
              </Card>
            ) : null}

            {submitError ? <Text className="text-caption text-danger">{submitError}</Text> : null}
            <Button
              label="Post task"
              onPress={handleSubmit}
              loading={submitting}
              disabled={needsCard}
            />
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
