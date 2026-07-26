import type { Task } from "./types";

export type DerivedTaskStatus = "open" | "assigned" | "completed" | "cancelled";

export function deriveTaskStatus(task: Task): DerivedTaskStatus {
  if (task.status === "completed") return "completed";
  if (task.status === "cancelled") return "cancelled";
  return task.assigned_to_id ? "assigned" : "open";
}

const STATUS_LABEL: Record<DerivedTaskStatus, string> = {
  open: "Open",
  assigned: "Assigned",
  completed: "Completed",
  cancelled: "Cancelled",
};

export function statusLabel(status: DerivedTaskStatus): string {
  return STATUS_LABEL[status];
}

export function formatCost(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`;
}

const SCHEDULED_AT_FORMAT: Intl.DateTimeFormatOptions = {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
};

// Scheduled times render as date + HH:mm everywhere (available cards, review
// step, task detail) — never seconds. The stored RFC3339 value keeps :00
// seconds; this is display precision only.
export function formatScheduledAt(value: Date | string): string {
  const date = typeof value === "string" ? new Date(value) : value;
  return date.toLocaleString([], SCHEDULED_AT_FORMAT);
}

// Scheduling is minute-precision: zero seconds/ms so the committed Date (and the
// RFC3339 string it serializes to) never carries sub-minute noise from an
// AI-parsed time or a `Date.now()`-derived default.
export function zeroSeconds(date: Date): Date {
  const next = new Date(date);
  next.setSeconds(0, 0);
  return next;
}

export function formatMinutes(minutes: number): string {
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder === 0 ? `${hours} hr` : `${hours} hr ${remainder} min`;
}

export function formatElapsed(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${pad(minutes)}:${pad(seconds)}`;
}

export function formatRelativeTime(isoDate: string): string {
  const diffMs = Date.now() - new Date(isoDate).getTime();
  const minutes = Math.round(diffMs / (60 * 1000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

// "Last seen" freshness for the supporter's live-location row. Spelled out
// ("2 min ago", not "2m ago") because it's a standalone sentence the requester
// reads while tracking someone, where the terse list-metadata form reads as too
// clipped. `nowMs` is injected so the caller's per-second ticker re-derives it
// on each render without this reaching for Date.now() itself.
export function formatLastSeen(isoDate: string, nowMs: number): string {
  const minutes = Math.round((nowMs - new Date(isoDate).getTime()) / (60 * 1000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hr ago`;
  const days = Math.round(hours / 24);
  return `${days} d ago`;
}

// Beyond this age we still show the last-known position but style it muted and
// stop implying it's live (spec: stale honesty at 5 minutes).
export const LOCATION_STALE_MS = 5 * 60 * 1000;

// Forgot-to-clock-out reminders (client-side only, no backend watchdog yet —
// see skills/decisions for the deferred server-side version). Tuning the
// whole cadence — how soon the first nudge fires and how often it repeats —
// is a one-line change to these five numbers.
export const OVERTIME_REMINDER = {
  /** First reminder fires at max(estimated * this, estimated + firstReminderFloorMinutes)... */
  firstReminderMultiplier: 1.25,
  /** ...but never later than estimated + this many minutes. */
  firstReminderFloorMinutes: 15,
  firstReminderCapMinutes: 45,
  /** After the first reminder, repeat every N minutes. */
  repeatIntervalMinutes: 30,
  /** How many repeat reminders to pre-schedule after the first. */
  repeatCount: 3,
} as const;

export function computeFirstReminderDelayMinutes(estimatedMinutes: number): number {
  const floor = Math.max(
    estimatedMinutes * OVERTIME_REMINDER.firstReminderMultiplier,
    estimatedMinutes + OVERTIME_REMINDER.firstReminderFloorMinutes
  );
  return Math.min(floor, estimatedMinutes + OVERTIME_REMINDER.firstReminderCapMinutes);
}
