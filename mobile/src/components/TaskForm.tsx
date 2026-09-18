import { useEffect, useState } from "react";
import { ScrollView, Text, View } from "react-native";
import { useRouter } from "expo-router";
import { AddressField } from "./AddressField";
import { ScheduledTimeField } from "./ScheduledTimeField";
import { Button, Checkbox, Input, Pill, PressableScale } from "./ui";
import { ApiError, estimateTaskCost, type CreateTaskPayload } from "../lib/api";
import { DISABLED_CATEGORY_NOTICE, isCategoryDisabled } from "../lib/beta-notice";
import { getCategoryMeta } from "../lib/categories";
import { highBudgetWarning, surgeRateNote } from "../lib/payment-copy";
import { formatCost, formatMinutes, formatScheduledAt, zeroSeconds } from "../lib/task-utils";
import type { ParsedTask, Task, TaskCategory, TaskCreatedVia } from "../lib/types";

/**
 * The task form — Post Task's review step (screen 2) and the requester's edit
 * screen are the same fields, the same validation and the same payload, so both
 * mount this one component. Each screen keeps only what differs: its own header,
 * its own CTA, and what it does with `taskFormToPayload(form)`.
 */

export type TransportOption = "none" | "car" | "bike" | "public";

export interface LocationRow {
  id: number;
  text: string;
}

export interface TaskFormState {
  title: string;
  category: TaskCategory | undefined;
  description: string;
  locations: LocationRow[];
  estimatedMinutes: string;
  shoppingBudget: string;
  transport: TransportOption;
  isImmediate: boolean;
  scheduledDate: Date;
  /** Consent for the supporter to run up to 15 minutes past the estimate
   *  without stopping to ask (tasks.auto_extend_consent). */
  autoExtend: boolean;
}

export interface TaskFormErrors {
  title?: string;
  category?: string;
  scheduledAt?: string;
  shoppingBudget?: string;
}

const QUICK_MINUTES = [30, 60, 90, 120];

// (SHOPPING_BUDGET_CAP_DOLLARS = 30 lived here, described as mirroring
// BillingConfig.ShoppingBudgetCapCents. That field no longer exists: the cap
// was removed from Go AND from its DB CHECK when the whole budget began being
// reserved on the card at post, because a requester who sees and authorizes
// the exact figure needs a warning, not a refusal — see the note on
// OverageToleranceCents in server/billing.go. What replaced it is the advisory
// HighBudgetWarningCents, which this form already renders from the server's
// quote (`high_budget_warning_cents`).
//
// The constant outlived the rule it mirrored, so this form was refusing $31
// budgets the backend would have accepted, citing a beta cap nothing enforced.)

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

function defaultScheduledDate(): Date {
  return zeroSeconds(new Date(Date.now() + 60 * 60 * 1000));
}

function emptyLocations(): LocationRow[] {
  return [{ id: 0, text: "" }];
}

export function emptyTaskForm(category?: TaskCategory): TaskFormState {
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
    // Defaults ON, matching the column default and the behaviour every task
    // posted before this checkbox existed already has: overtime has always
    // billed without a per-task gate, so starting unchecked would be a change
    // of terms wearing a new control's clothes.
    autoExtend: true,
  };
}

export function taskFormFromParsed(parsed: ParsedTask, fallbackCategory?: TaskCategory): TaskFormState {
  const locations: LocationRow[] = [parsed.location_1, parsed.location_2]
    .filter((text) => text && text.trim())
    .map((text, i) => ({ id: i, text }));

  const isImmediate = !parsed.scheduled;

  return {
    ...emptyTaskForm(),
    title: parsed.title ?? "",
    category: parsed.category ?? fallbackCategory,
    description: parsed.description ?? "",
    locations: locations.length > 0 ? locations : emptyLocations(),
    estimatedMinutes: parsed.duration_minutes ? String(parsed.duration_minutes) : "",
    isImmediate,
    scheduledDate:
      !isImmediate && parsed.scheduled_time ? new Date(parsed.scheduled_time) : defaultScheduledDate(),
  };
}

function isTransportOption(value: string | null): value is TransportOption {
  return TRANSPORT_OPTIONS.some((o) => o.value === value);
}

/**
 * Prefill from a posted task, i.e. the inverse of `taskFormToPayload`. Rows come
 * back from `location_text` by the same " | " encoding both clients write, and
 * are NOT truncated to MAX_LOCATIONS — a task that somehow carries more stops
 * than the form can add must still round-trip through a save unchanged.
 */
export function taskFormFromTask(task: Task): TaskFormState {
  const locations: LocationRow[] = (task.location_text ?? "")
    .split(" | ")
    .map((text) => text.trim())
    .filter(Boolean)
    .map((text, i) => ({ id: i, text }));

  // `is_immediate` predates some rows; fall back to "scheduled iff it carries a
  // scheduled_at". Note the server stamps scheduled_at = now() for immediate
  // tasks, so the column alone can't answer this for newer rows.
  const isImmediate = task.is_immediate ?? !task.scheduled_at;
  const cents = task.prepay_amount_cents ?? 0;

  return {
    title: task.title,
    category: task.category,
    description: task.description ?? "",
    locations: locations.length > 0 ? locations : emptyLocations(),
    estimatedMinutes: task.estimated_minutes ? String(task.estimated_minutes) : "",
    shoppingBudget: cents > 0 ? (cents % 100 === 0 ? String(cents / 100) : (cents / 100).toFixed(2)) : "",
    transport: isTransportOption(task.transport_required) ? task.transport_required : "none",
    isImmediate,
    scheduledDate:
      !isImmediate && task.scheduled_at ? new Date(task.scheduled_at) : defaultScheduledDate(),
    // Absent (any list response, or a backend that predates the column) reads
    // as consented — the same "unchanged, not refused" rule the server applies
    // to an omitted field.
    autoExtend: task.auto_extend_consent ?? true,
  };
}

/**
 * Prefill for "Post again" — `taskFormFromTask` plus the two things that only
 * differ because the result is a NEW task rather than the old one reopened.
 * Deliberately a thin wrapper and not a second mapping: every field the form
 * carries is still mapped in exactly one place, so a new field added to
 * `taskFormFromTask` reaches this path for free.
 *
 * Nothing runtime comes across — status, assignee, worklogs, reviews and
 * timestamps are not part of TaskFormState in the first place, so a duplicate
 * can only ever post as a fresh open task.
 */
export function taskFormForDuplicate(task: Task): TaskFormState {
  const form = taskFormFromTask(task);

  // A locked category can't be posted this round, and the source task is by
  // definition an old one that could be. Arrive with the picker empty and its
  // standard disabled state visible rather than refusing the duplicate outright
  // — the user re-picks and keeps the rest of their prefill.
  const category = isCategoryDisabled(form.category) ? undefined : form.category;

  // Every scheduled task worth re-posting has a scheduled_at in the past, so
  // carrying the old timestamp over would fail validation on essentially all of
  // them. Keep the scheduled-vs-ASAP choice, reset the clock: "same task
  // tomorrow" is the case this feature exists for.
  const staleSchedule = !form.isImmediate && form.scheduledDate.getTime() <= Date.now();

  return {
    ...form,
    category,
    scheduledDate: staleSchedule ? defaultScheduledDate() : form.scheduledDate,
  };
}

export function validateTaskForm(form: TaskFormState): TaskFormErrors {
  const errors: TaskFormErrors = {};
  if (!form.title.trim()) errors.title = "Title is required.";
  if (!form.category) errors.category = "Choose a category.";
  if (!form.isImmediate && form.scheduledDate.getTime() <= Date.now()) {
    errors.scheduledAt = "Pick a time in the future.";
  }
  if (form.shoppingBudget !== "") {
    const n = Number(form.shoppingBudget);
    if (Number.isNaN(n) || n < 0) errors.shoppingBudget = "Invalid amount.";
    // No ceiling. A large budget is WARNED about, in red, from the server's
    // own threshold (high_budget_warning_cents) — nothing refuses it, here or
    // in Go. The whole amount is reserved on the requester's card at post, so
    // they have already seen and authorized the exact number.
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

/**
 * The wire payload for both POST /tasks and PATCH /tasks/:id. It is the same
 * shape for both on purpose: the PATCH handler binds a full createTaskInput and
 * overwrites every column it names (server/main.go updateTask), so a partial
 * body there does not patch — it blanks whatever it leaves out.
 *
 * Call `validateTaskForm` first; `category` is asserted here.
 *
 * `createdVia` is write-only attribution for POST /tasks (see TaskCreatedVia).
 * The edit screen passes nothing: PATCH ignores the field, and how a task was
 * created doesn't change when it is edited.
 */
export function taskFormToPayload(
  form: TaskFormState,
  createdVia?: TaskCreatedVia
): CreateTaskPayload {
  const budget = form.shoppingBudget ? Number(form.shoppingBudget) : 0;
  const prepayCents = Number.isFinite(budget) && budget > 0 ? Math.round(budget * 100) : 0;

  return {
    title: form.title.trim(),
    description: form.description.trim() || undefined,
    category: form.category as TaskCategory,
    location_text: encodeLocationText(form.locations) || undefined,
    estimated_minutes: form.estimatedMinutes ? Number(form.estimatedMinutes) : undefined,
    prepay_amount_cents: prepayCents,
    is_immediate: form.isImmediate,
    scheduled_at: form.isImmediate ? "" : zeroSeconds(form.scheduledDate).toISOString(),
    transport_required: form.transport,
    created_via: createdVia,
    auto_extend_consent: form.autoExtend,
  };
}

export interface TaskFormProps {
  form: TaskFormState;
  onChange: (update: (form: TaskFormState) => TaskFormState) => void;
  errors: TaskFormErrors;
}

export function TaskForm({ form, onChange, errors }: TaskFormProps) {
  const router = useRouter();

  const [estimate, setEstimate] = useState<{
    baseFeeCents: number;
    includedMinutes: number;
    billableMinutes: number;
    perMinuteRateCents: number;
    timeCostCents: number;
    shoppingCents: number;
    totalCents: number;
    surgeRate: boolean;
    highBudgetWarningCents: number;
  } | null>(null);

  // Pre-submission price quote — server-computed (S-05), matching web's summary
  // line but sourced from POST /tasks/estimate instead of a client-side formula.
  // Gated on category AND a positive duration (not just category): on the AI
  // path estimated_minutes is always prefilled by the parser, so gating on
  // category alone happened to look fine there — but on the "Fill manually"
  // path duration starts empty, and defaulting it to 0 rendered a degenerate
  // "$12 total, 0 min" card the moment a category was picked, which read as
  // broken rather than genuinely appearing. Requiring both fields makes the
  // two paths behave identically.
  useEffect(() => {
    const minutes = form.estimatedMinutes ? Number(form.estimatedMinutes) : NaN;
    if (!form.category || !Number.isFinite(minutes) || minutes <= 0) {
      setEstimate(null);
      return;
    }
    let cancelled = false;
    const id = setTimeout(async () => {
      try {
        const budget = form.shoppingBudget ? Number(form.shoppingBudget) : 0;
        const prepayCents = Number.isFinite(budget) && budget > 0 ? Math.round(budget * 100) : 0;
        const result = await estimateTaskCost({
          category: form.category as TaskCategory,
          estimated_minutes: minutes,
          prepay_amount_cents: prepayCents,
          // The rate depends on when the work happens, not on when this form
          // was opened: a 21:30 task filled in at 6pm is quoted the evening
          // rate.
          is_immediate: form.isImmediate,
          scheduled_at: form.isImmediate ? "" : zeroSeconds(form.scheduledDate).toISOString(),
        });
        if (cancelled) return;
        setEstimate({
          baseFeeCents: result.base_fee_cents,
          // Defaults cover the window where a shipped app is talking to a
          // backend that predates these fields — the card renders the old way
          // instead of showing "undefined min".
          includedMinutes: result.included_minutes ?? 0,
          billableMinutes: result.billable_minutes ?? minutes,
          perMinuteRateCents: result.per_minute_rate_cents ?? 50,
          timeCostCents: result.time_cost_cents,
          // The key was renamed in Stripe Phase 1; the backend sends both.
          shoppingCents: result.shopping_budget_cents ?? result.shopping_cents ?? 0,
          totalCents: result.total_cents,
          surgeRate: result.surge_rate ?? false,
          highBudgetWarningCents: result.high_budget_warning_cents ?? 0,
        });
      } catch (e) {
        if (cancelled) return;
        if (e instanceof ApiError && e.isAuthError) {
          router.replace("/(auth)/login");
          return;
        }
        setEstimate(null);
      }
    }, 300);
    return () => {
      cancelled = true;
      clearTimeout(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form.category, form.estimatedMinutes, form.shoppingBudget]);

  function updateLocation(id: number, text: string) {
    onChange((f) => ({ ...f, locations: f.locations.map((l) => (l.id === id ? { ...l, text } : l)) }));
  }

  function addLocation() {
    onChange((f) => {
      if (f.locations.length >= MAX_LOCATIONS) return f;
      const nextId = Math.max(...f.locations.map((l) => l.id)) + 1;
      return { ...f, locations: [...f.locations, { id: nextId, text: "" }] };
    });
  }

  function removeLocation(id: number) {
    onChange((f) => ({ ...f, locations: f.locations.filter((l) => l.id !== id) }));
  }

  // A task posted before this picker existed (or by web, which has a wider
  // category list) keeps its own category selectable rather than silently
  // losing it on the next save.
  const pickerCategories =
    form.category && !CATEGORY_PICKS.includes(form.category)
      ? [...CATEGORY_PICKS, form.category]
      : CATEGORY_PICKS;

  // No outer padding: each host screen wraps this in its own `gap-4` block and
  // appends its own CTA, so the 16pt field rhythm carries through to the button.
  return (
    <View className="gap-4">
      {/* `grow`, not a plain field: AI-parsed and hand-typed titles both run
          past one line often enough that a fixed-height box is the common case,
          not the edge one. The posted value is unchanged — still a single-line
          string (see Input's `grow`). */}
      <Input
        label="Title"
        grow
        value={form.title}
        onChangeText={(title) => onChange((f) => ({ ...f, title }))}
        placeholder="What do you need?"
        error={errors.title}
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
                // Locked for this round: shown in place, dimmed, un-selectable.
                disabled={isCategoryDisabled(value)}
                onPress={() => onChange((f) => ({ ...f, category: value }))}
              />
            ))}
          </View>
        </ScrollView>
        {/* A locked category can't be picked here, but it can still arrive
            pre-set — the AI parser returns "companionship" of its own accord.
            Say so at the picker rather than only on a rejected submit. */}
        {!errors.category && isCategoryDisabled(form.category) ? (
          <Text className="mt-1 text-caption text-muted">{DISABLED_CATEGORY_NOTICE}</Text>
        ) : null}
        {errors.category ? <Text className="mt-1 text-caption text-danger">{errors.category}</Text> : null}
      </View>

      <Input
        label="Description"
        multiline
        numberOfLines={4}
        textAlignVertical="top"
        value={form.description}
        onChangeText={(description) => onChange((f) => ({ ...f, description }))}
        className="h-[100px]"
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
            {/* Places autocomplete + Apt/Suite line, same NYC bias and same
                apt-splicing as web's AddressInput — see AddressField. Free
                text still posts fine if the user ignores the suggestions. */}
            <AddressField
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
            onChange((f) => ({ ...f, estimatedMinutes: text.replace(/[^0-9]/g, "") }))
          }
          placeholder="60"
        />
        <View className="mt-2 flex-row gap-2">
          {QUICK_MINUTES.map((m) => (
            <Pill
              key={m}
              label={formatMinutes(m)}
              selected={form.estimatedMinutes === String(m)}
              onPress={() => onChange((f) => ({ ...f, estimatedMinutes: String(m) }))}
            />
          ))}
        </View>
      </View>

      {/* A term of the estimate above, so it sits with it rather than in a
          settings block of its own. Phrased as what it permits, with the cap
          and the rate stated — the requester is agreeing to a billing term. */}
      <PressableScale
        onPress={() => onChange((f) => ({ ...f, autoExtend: !f.autoExtend }))}
        className="-ml-3 flex-row items-center"
      >
        <Checkbox
          checked={form.autoExtend}
          onChange={(autoExtend) => onChange((f) => ({ ...f, autoExtend }))}
          accessibilityLabel="Allow up to 15 extra minutes at the same rate"
        />
        <View className="flex-1 pr-1">
          <Text className="text-body text-ink">
            Allow up to 15 extra minutes at the same rate if the task runs long
          </Text>
          <Text className="mt-0.5 text-caption text-muted">
            Without this your supporter has to stop and ask before going over.
          </Text>
        </View>
      </PressableScale>

      <Input
        label="Shopping budget ($)"
        keyboardType="decimal-pad"
        value={form.shoppingBudget}
        onChangeText={(text) => onChange((f) => ({ ...f, shoppingBudget: text.replace(/[^0-9.]/g, "") }))}
        placeholder="e.g. 12.50"
        error={errors.shoppingBudget}
      />

      <View>
        <Text className="mb-1 text-caption text-muted">Helper needs a…</Text>
        <View className="flex-row flex-wrap gap-2">
          {TRANSPORT_OPTIONS.map((opt) => (
            <Pill
              key={opt.value}
              label={opt.label}
              selected={form.transport === opt.value}
              onPress={() => onChange((f) => ({ ...f, transport: opt.value }))}
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
            onPress={() => onChange((f) => ({ ...f, isImmediate: true }))}
          />
          <Pill
            label="Schedule for later"
            selected={!form.isImmediate}
            onPress={() => onChange((f) => ({ ...f, isImmediate: false }))}
          />
        </View>
        {!form.isImmediate ? (
          <ScheduledTimeField
            value={form.scheduledDate}
            onChange={(scheduledDate) => onChange((f) => ({ ...f, scheduledDate }))}
            error={errors.scheduledAt}
          />
        ) : null}
      </View>

      {estimate ? (
        <View className="gap-1 rounded-card border border-line bg-surface p-4">
          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">Start</Text>
            <Text className="text-caption text-ink">
              {form.isImmediate ? "ASAP" : formatScheduledAt(form.scheduledDate)}
            </Text>
          </View>
          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">
              Base fee{estimate.includedMinutes > 0 ? ` (first ${estimate.includedMinutes} min included)` : ""}
            </Text>
            <Text className="text-caption text-ink">{formatCost(estimate.baseFeeCents)}</Text>
          </View>
          <View className="flex-row justify-between">
            <Text className="text-caption text-muted">
              Time ({estimate.billableMinutes} billable min × {formatCost(estimate.perMinuteRateCents)})
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
            <Text className="text-caption font-semibold text-ink">{formatCost(estimate.totalCents)}</Text>
          </View>
          {/* Why this task costs more than usual. The wording, the rate and the
              included block all come from the server quote. */}
          {surgeRateNote({
            surge_rate: estimate.surgeRate,
            per_minute_rate_cents: estimate.perMinuteRateCents,
            included_minutes: estimate.includedMinutes,
          }) ? (
            <Text className="text-caption text-muted">
              {surgeRateNote({
                surge_rate: estimate.surgeRate,
                per_minute_rate_cents: estimate.perMinuteRateCents,
                included_minutes: estimate.includedMinutes,
              })}
            </Text>
          ) : null}
          <Text className="text-caption text-muted">
            This exact amount is reserved on your card when you post. You&apos;re charged for
            what&apos;s actually used.
          </Text>
        </View>
      ) : null}

      {/* A lot of money about to be reserved. Warns in the danger colour and
          blocks nothing — the post button below stays enabled. */}
      {estimate &&
      highBudgetWarning(shoppingBudgetCents(form), {
        high_budget_warning_cents: estimate.highBudgetWarningCents,
      }) ? (
        <View className="mt-3 rounded-card border border-danger bg-surface p-3">
          <Text className="text-caption text-danger">
            {highBudgetWarning(shoppingBudgetCents(form), {
              high_budget_warning_cents: estimate.highBudgetWarningCents,
            })}
          </Text>
        </View>
      ) : null}
    </View>
  );
}

// The typed budget in integer cents, for the warning threshold. The only place
// this file turns a typed string into money, and it does no pricing — the
// threshold it is compared against comes from the server.
function shoppingBudgetCents(form: TaskFormState): number {
  const budget = form.shoppingBudget ? Number(form.shoppingBudget) : 0;
  return Number.isFinite(budget) && budget > 0 ? Math.round(budget * 100) : 0;
}
