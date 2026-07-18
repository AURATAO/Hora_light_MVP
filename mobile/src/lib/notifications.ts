import type { LucideIcon } from "lucide-react-native";
import { CheckCircle2, ClipboardCheck, LogIn, LogOut, MessageCircle, XCircle } from "lucide-react-native";
import type { NotificationType } from "./types";
import { color } from "../theme/tokens";

export type NotificationTint = "brand" | "danger" | "neutral";

export interface NotificationMeta {
  icon: LucideIcon;
  tint: NotificationTint;
}

// Single source of truth for how a NotificationType renders (DESIGN.md §5
// icon family). Server pre-composes human-readable title/body text, so this
// only needs to pick an icon + a tint variant per type — brand for positive
// completion events, danger for cancellation, neutral otherwise.
const NOTIFICATION_META: Record<NotificationType, NotificationMeta> = {
  ORDER_ACCEPTED: { icon: ClipboardCheck, tint: "brand" },
  CLOCK_IN: { icon: LogIn, tint: "neutral" },
  CLOCK_OUT: { icon: LogOut, tint: "neutral" },
  COMPLETED: { icon: CheckCircle2, tint: "brand" },
  COMPLETED_SUPPORTER: { icon: CheckCircle2, tint: "brand" },
  CANCELLED: { icon: XCircle, tint: "danger" },
  NEW_MESSAGE: { icon: MessageCircle, tint: "neutral" },
};

// Falls back to a neutral message icon for any type the client doesn't
// recognize yet, so an unmapped server-side addition can't crash the list.
const FALLBACK_META: NotificationMeta = { icon: MessageCircle, tint: "neutral" };

export function getNotificationMeta(type: NotificationType): NotificationMeta {
  return NOTIFICATION_META[type] ?? FALLBACK_META;
}

const TINT_STYLES: Record<NotificationTint, { bg: string; iconColor: string }> = {
  brand: { bg: "bg-brand-tint", iconColor: color.brand },
  danger: { bg: "bg-line", iconColor: color.danger },
  neutral: { bg: "bg-line", iconColor: color.muted },
};

export function getNotificationTintStyles(tint: NotificationTint): { bg: string; iconColor: string } {
  return TINT_STYLES[tint];
}
