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

export function formatMinutes(minutes: number): string {
  if (minutes < 60) return `${minutes} min`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder === 0 ? `${hours} hr` : `${hours} hr ${remainder} min`;
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
