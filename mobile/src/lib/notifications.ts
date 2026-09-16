import type { LucideIcon } from "lucide-react-native";
import {
  Banknote,
  CheckCircle2,
  ClipboardCheck,
  Clock,
  HandCoins,
  LogIn,
  LogOut,
  MessageCircle,
  Repeat2,
  TimerOff,
  XCircle,
} from "lucide-react-native";
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
  TASK_REMOVED: { icon: XCircle, tint: "danger" },
  // One type, three recipients: the incoming supporter, the outgoing one, and
  // the requester (server/admin_reassign.go). Neutral rather than brand or
  // danger because the same row reads as good news or bad depending on who is
  // looking at it — the server-composed title and body carry that.
  TASK_REASSIGNED: { icon: Repeat2, tint: "neutral" },
  NEW_MESSAGE: { icon: MessageCircle, tint: "neutral" },

  // Stripe Phase 2b. All five are neutral, and none is danger — a supporter
  // asking for another $8, or a task reaching the time everyone agreed to, is
  // ordinary and not a failure. Reserving danger for cancellations is what
  // keeps it meaning something in this list.
  BUDGET_INCREASE_REQUESTED: { icon: HandCoins, tint: "neutral" },
  TIME_EXTENSION_REQUESTED: { icon: Clock, tint: "neutral" },
  // One type for approved / denied / expired, same reasoning as
  // TASK_REASSIGNED: the outcome is in the server-composed title and body, and
  // the same row reads differently depending on which side you are on.
  EXTENSION_RESOLVED: { icon: HandCoins, tint: "neutral" },
  TIME_CAP_WARNING: { icon: Clock, tint: "neutral" },
  TIME_CAP_REACHED: { icon: TimerOff, tint: "neutral" },

  // Stripe Phase 3. The only unambiguously good money event in the list —
  // brand, like the completion events, because it reads the same way to
  // everyone who can see it (its only recipient is the supporter being paid).
  PAYOUT_SENT: { icon: Banknote, tint: "brand" },
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
