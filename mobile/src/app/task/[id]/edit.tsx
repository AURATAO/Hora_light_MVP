import { useCallback, useEffect, useState } from "react";
import { ScrollView, Text, View } from "react-native";
import { useLocalSearchParams, useRouter } from "expo-router";
import { CircleAlert, Lock, X } from "lucide-react-native";
import { CompanionshipPolicySheet } from "../../../components/CompanionshipPolicySheet";
import {
  TaskForm,
  taskFormFromTask,
  taskFormToPayload,
  validateTaskForm,
  type TaskFormErrors,
  type TaskFormState,
} from "../../../components/TaskForm";
import { Button, EmptyState, PressableScale, Screen, Skeleton } from "../../../components/ui";
import { ApiError, getMe, getTask, updateTask } from "../../../lib/api";
import { isCompanionCategory } from "../../../lib/companionship-policy";
import { deriveTaskStatus } from "../../../lib/task-utils";
import { useCompanionshipGate } from "../../../lib/use-companionship-gate";
import type { Task } from "../../../lib/types";
import { color, size } from "../../../theme/tokens";

// server/main.go updateTask rejects an accepted task with this exact string,
// both from its up-front check and from the guarded UPDATE that catches an
// accept landing mid-save. Matching it here is the same contract the accept
// button relies on for "not available" (see task/[id].tsx handleAccept).
const ACCEPTED_ERROR = "cannot edit after it has been accepted";
const CLOSED_ERROR = "only open tasks can be edited";

const LOCKED_ACCEPTED = "This task was just accepted — changes can't be saved. Coordinate in chat instead.";
const LOCKED_CLOSED = "This task is no longer open, so changes can't be saved.";
const LOCKED_NOT_MINE = "Only the person who posted a task can edit it.";

// The same rule the Edit action is gated on in task/[id].tsx, re-checked here
// because this route is reachable on its own (deep link, or a back-navigation
// to a task that was accepted in the meantime). Returns the reason it's locked,
// or null when the form should open.
function lockReason(task: Task, meId: string): string | null {
  if (task.requester_id !== meId) return LOCKED_NOT_MINE;
  const status = deriveTaskStatus(task);
  if (status === "assigned") return LOCKED_ACCEPTED;
  if (status !== "open") return LOCKED_CLOSED;
  return null;
}

export default function EditTask() {
  const router = useRouter();
  const params = useLocalSearchParams();
  const id = Array.isArray(params.id) ? params.id[0] : (params.id ?? "");

  const [task, setTask] = useState<Task | null>(null);
  const [form, setForm] = useState<TaskFormState | null>(null);
  const [fieldErrors, setFieldErrors] = useState<TaskFormErrors>({});
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  // Set when the server refuses the edit because the task stopped being
  // editable under us. Replaces the form outright — there is nothing left to
  // save, and leaving the fields up would invite a second doomed attempt.
  const [lockedNotice, setLockedNotice] = useState<string | null>(null);

  // A task already posted as companionship had the policy accepted when it was
  // posted; only switching an ordinary task TO companionship here needs the
  // sheet. `active` waits for the prefill so it can't fire against an empty form.
  const policy = useCompanionshipGate(form?.category, {
    active: form !== null && lockedNotice === null,
    preAcknowledged: isCompanionCategory(task?.category),
  });

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const load = useCallback(async () => {
    try {
      const [me, t] = await Promise.all([getMe(), getTask(id)]);
      if (!me.auth) {
        router.replace("/(auth)/login");
        return;
      }
      setTask(t);
      const locked = lockReason(t, me.id);
      setForm(locked ? null : taskFormFromTask(t));
      setLockedNotice(locked);
      setLoadError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      setLoadError(e instanceof Error ? e.message : "Couldn't load this task");
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  async function handleSave() {
    if (!form) return;
    const errors = validateTaskForm(form);
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) return;

    if (policy.needsPolicy) {
      policy.request();
      return;
    }

    setSaveError(null);
    setSaving(true);
    try {
      // A full payload, not a diff: PATCH /tasks/:id binds a whole
      // createTaskInput and overwrites every column it names, so anything left
      // out would be blanked rather than kept (see taskFormToPayload).
      await updateTask(id, taskFormToPayload(form));
      router.back();
    } catch (e) {
      if (handleAuthError(e)) return;
      if (e instanceof ApiError && (e.message === ACCEPTED_ERROR || e.message === CLOSED_ERROR)) {
        // Lost the race with an accept (or a completion/cancellation) between
        // loading this form and saving it. Replace the form with what actually
        // happened, then refetch — load() refines the reason from the fresh
        // task, and leaves this one standing if that refetch itself fails.
        setLockedNotice(e.message === CLOSED_ERROR ? LOCKED_CLOSED : LOCKED_ACCEPTED);
        setForm(null);
        await load();
      } else {
        setSaveError(e instanceof Error ? e.message : "Couldn't save your changes. Try again.");
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Screen scroll={false} avoidKeyboard>
      <View className="mb-6 mt-4 flex-row items-center justify-between">
        <Text className="text-title font-semibold text-ink">Edit task</Text>
        <PressableScale
          onPress={() => router.back()}
          className="h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
          accessibilityRole="button"
          accessibilityLabel="Close"
        >
          <X color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
      </View>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-8 w-2/3" />
          <Skeleton className="h-[120px]" />
          <Skeleton className="h-[80px]" />
        </View>
      ) : /* Lock before load error: a failed refetch must not bury the reason
             the edit was just refused. */
      lockedNotice ? (
        <EmptyState
          icon={Lock}
          title="This task is locked"
          caption={lockedNotice}
          actionLabel="Back to task"
          onAction={() => router.back()}
        />
      ) : loadError ? (
        <EmptyState
          icon={CircleAlert}
          title="Couldn't load this task"
          caption={loadError}
          actionLabel="Retry"
          onAction={load}
        />
      ) : form ? (
        <ScrollView
          className="flex-1"
          keyboardShouldPersistTaps="handled"
          showsVerticalScrollIndicator={false}
        >
          <View className="gap-4 pb-8">
            <TaskForm
              form={form}
              onChange={(update) => setForm((f) => (f ? update(f) : f))}
              errors={fieldErrors}
            />
            {saveError ? <Text className="text-caption text-danger">{saveError}</Text> : null}
            <Button label="Save changes" onPress={handleSave} loading={saving} />
          </View>
        </ScrollView>
      ) : null}

      <CompanionshipPolicySheet
        visible={policy.open}
        onDismiss={policy.dismiss}
        onAcknowledge={policy.acknowledge}
      />
    </Screen>
  );
}
