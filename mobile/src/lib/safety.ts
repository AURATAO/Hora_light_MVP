// Report / Block copy and fallbacks (App Store Guideline 1.2).
//
// The reason presets come from the server (GET /safety/report-reasons), like
// cancel reasons — server/safety.go owns the closed set. The fallback below
// only covers a failed fetch, so reporting is never blocked by a network blip;
// its values match the server's.

import type { ReportReason } from "./api";

export const FALLBACK_REPORT_REASONS: ReportReason[] = [
  { value: "harassment", label: "Harassment or threats" },
  { value: "inappropriate_messages", label: "Inappropriate or offensive messages" },
  { value: "unsafe_behavior", label: "Unsafe behavior" },
  { value: "no_show", label: "Didn't show up" },
  { value: "scam_or_payment", label: "Scam or off-app payment request" },
  { value: "other", label: "Something else" },
];

/**
 * The other party on a task, for Report / Block: the supporter for the
 * requester, the requester for the supporter; null when the task has no
 * supporter yet or the viewer is not on it.
 *
 * THE VIEWER CAN BE THEIR OWN COUNTERPART. On the App Review sandbox's
 * self-accepted task both seats are the reviewer, and the menu still has to
 * render — Guideline 1.2 is checked by opening it and seeing the Report sheet.
 * The server refuses the actual self-report / self-block with
 * "You can't report or block yourself." (server/safety.go), which the sheet
 * and the block alert both show as-is. Hiding the menu instead is what made a
 * reviewer following step 8 of the review notes find nothing (2026-09-28).
 */
export function safetyCounterpart(task: {
  meId: string | null;
  requesterId: string | null | undefined;
  assignedToId: string | null | undefined;
  requesterName?: string | null;
  supporterName?: string | null;
}): { id: string; name: string | null } | null {
  const { meId, requesterId, assignedToId } = task;
  if (!meId || !requesterId || !assignedToId) return null;
  const clean = (n: string | null | undefined) => n?.trim() || null;
  if (meId === requesterId) return { id: assignedToId, name: clean(task.supporterName) };
  if (meId === assignedToId) return { id: requesterId, name: clean(task.requesterName) };
  return null;
}

/** What a person is called in the menu when the app has no name for them. */
export function safetyTargetName(name: string | null | undefined): string {
  const n = (name ?? "").trim();
  return n || "this person";
}

export function blockConfirmTitle(name: string | null | undefined): string {
  return `Block ${safetyTargetName(name)}?`;
}

export const BLOCK_CONFIRM_BODY =
  "You won't be matched with each other on any task again, and this chat becomes read-only for both of you. The HO:RA team is notified.";

/** The line a read-only (blocked) chat shows in place of the message field. */
export const CHAT_BLOCKED_LINE =
  "This conversation is read-only because one of you blocked the other. You can still see earlier messages. Need help? Report the user from the menu above.";

/** Where the report went, after it was sent. */
export const REPORT_SENT_BODY =
  "Thanks for telling us. The HO:RA team reviews every report within 24 hours. If you're in danger, call 911.";
