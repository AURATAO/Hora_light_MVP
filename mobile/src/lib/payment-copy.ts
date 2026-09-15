import type { TaskPayment } from "./types";
import { formatCost } from "./task-utils";

/**
 * Every sentence the app says about a card hold, in one place.
 *
 * Mirrors app/src/lib/paymentCopy.js word for word — the two clients do not
 * share code, and a requester who posts on web and cancels on their phone must
 * be told the same thing both times. The web copy is pinned by
 * app/src/lib/paymentCopy.test.mjs; this file is its twin.
 *
 * The gap being closed was not a bug in the money — the hold, the capture and
 * the release were all correct. It was that an off-session pre-auth is SILENT:
 * a live tester posted a task, saw nothing about their card, concluded the post
 * had failed, and cancelled it.
 *
 * TWO RULES:
 *
 * 1. ALWAYS A REAL NUMBER. Never "your card may be charged". Where the amount
 *    is unknown these return null and the caller renders nothing — which is
 *    honest. They never fall back to a hedge or to $0.00.
 *
 * 2. NO ARITHMETIC. Every figure is computed in Go and rendered verbatim
 *    (S-05). "Charged $X, released $Y" uses the server's own captured_cents
 *    and released_cents rather than subtracting one from the other.
 */

const BRAND_LABELS: Record<string, string> = {
  visa: "Visa",
  mastercard: "Mastercard",
  amex: "American Express",
  discover: "Discover",
  diners: "Diners Club",
  jcb: "JCB",
  unionpay: "UnionPay",
};

/** "Visa ••4242", or "" when the card is unknown — the caller drops the clause
 *  rather than writing "your card (unknown)". */
export function formatCardLabel(payment?: TaskPayment | null): string {
  const last4 = payment?.card_last4;
  if (!last4) return "";
  const brand = payment?.card_brand ?? "";
  return `${BRAND_LABELS[brand] ?? "Card"} ••${last4}`;
}

/**
 * The post-success confirmation, as two lines.
 *
 *   primary    "$49.50 reserved — $19.50 time + $30.00 budget"
 *   secondary  "Charged only for what's used. Rest released automatically."
 *
 * Short on purpose: the long version was a paragraph, and a paragraph on a
 * success screen is a paragraph nobody reads — which defeats the point, since
 * the reason this exists is a requester seeing nothing and assuming the post
 * had failed.
 *
 * No card here. Which card it landed on matters when you are looking at a live
 * task; at the moment of posting the number is the message. The card stays on
 * the task-detail line.
 */
export interface HoldMessage {
  primary: string;
  secondary: string;
}

export function holdPlacedMessage(payment?: TaskPayment | null): HoldMessage | null {
  if (!payment?.authorized_cents) return null;
  return {
    primary: holdBreakdown(payment),
    secondary: "Charged only for what's used. Rest released automatically.",
  };
}

// The split comes from the server and only when it reconciles with the total;
// nothing is added up here (S-05). No budget means the single number rather
// than "— $19.50 time + $0.00 budget", which is noise pretending to be detail.
function holdBreakdown(payment: TaskPayment): string {
  const total = formatCost(payment.authorized_cents);
  const budget = payment.shopping_budget_cents ?? 0;
  const time = payment.time_cost_cents ?? 0;
  if (budget > 0 && time > 0) {
    return `${total} reserved — ${formatCost(time)} time + ${formatCost(budget)} budget`;
  }
  return `${total} reserved`;
}

/** Why a task is quoted more than usual. Null unless the evening rate applies;
 *  every number comes from the server quote. */
export function surgeRateNote(quote?: {
  surge_rate?: boolean;
  per_minute_rate_cents?: number;
  included_minutes?: number;
} | null): string | null {
  if (!quote?.surge_rate) return null;
  return `Evening rate: ${formatCost(quote.per_minute_rate_cents ?? 0)}/min after the first ${quote.included_minutes ?? 0} minutes.`;
}

/** The red notice when a lot of money is about to be reserved. The threshold
 *  comes from the server so both clients warn at the same number (S-05). */
export function highBudgetWarning(
  budgetCents: number,
  quote?: { high_budget_warning_cents?: number } | null
): string | null {
  const threshold = quote?.high_budget_warning_cents;
  if (!threshold || !budgetCents || budgetCents < threshold) return null;
  return "High budget — this full amount will be reserved on your card.";
}

/** The persistent banner when a completion could not be charged. Names the
 *  amount and the task, because "you have a balance" with no number is the
 *  same vagueness that caused the original incident. */
export function outstandingBalanceMessage(
  outstanding?: { total_cents?: number; task_title?: string; task_count?: number } | null
): string | null {
  if (!outstanding?.total_cents) return null;
  const from = outstanding.task_title ? ` from \u201C${outstanding.task_title}\u201D` : "";
  const more = (outstanding.task_count ?? 1) > 1 ? ` and ${(outstanding.task_count ?? 1) - 1} more` : "";
  return `You have an outstanding balance of ${formatCost(outstanding.total_cents)}${from}${more} — settle it to keep posting.`;
}

/** The short form for a task-detail row: "$76.75 reserved · Visa ••4242". */
export function holdSummary(payment?: TaskPayment | null): string | null {
  if (!payment?.authorized_cents) return null;
  const card = formatCardLabel(payment);
  return card
    ? `${formatCost(payment.authorized_cents)} reserved · ${card}`
    : `${formatCost(payment.authorized_cents)} reserved`;
}

/** The warning inside the cancel sheet, BEFORE anything happens. */
export function holdWillBeReleasedMessage(payment?: TaskPayment | null): string | null {
  if (!payment?.authorized_cents) return null;
  return `Your reserved ${formatCost(payment.authorized_cents)} will be released immediately.`;
}

// Banks post an authorization reversal on their own schedule, so the requester
// keeps seeing the hold until they do. Saying so is the difference between
// "released" and a support message three days later.
const STATEMENT_NOTE =
  "Depending on your bank, it may take 1–7 days to disappear from your statement.";

export interface ReleaseAmounts {
  captured_cents?: number;
  released_cents?: number;
}

/** What the requester is told after a cancel succeeds. Null when no hold was
 *  ever placed, and the caller then says nothing about money. */
export function holdReleasedMessage(result?: ReleaseAmounts | null): string | null {
  const released = result?.released_cents ?? 0;
  const captured = result?.captured_cents ?? 0;
  if (!released && !captured) return null;

  if (captured > 0) {
    const tail =
      released > 0
        ? ` the remaining ${formatCost(released)} hold has been released.`
        : " the rest of the hold has been released.";
    return `Charged ${formatCost(captured)} for completed time;${tail} ${STATEMENT_NOTE}`;
  }
  return `Reserved ${formatCost(released)} released. ${STATEMENT_NOTE}`;
}
