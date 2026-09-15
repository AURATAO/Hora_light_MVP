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

/** What the requester is told the moment a task posts. Null when there is no
 *  hold — every task posted with PAYMENTS_ENFORCED off. */
export function holdPlacedMessage(payment?: TaskPayment | null): string | null {
  if (!payment?.authorized_cents) return null;
  const card = formatCardLabel(payment);
  const on = card ? `on your card (${card})` : "on your card";
  return (
    `We've reserved ${formatCost(payment.authorized_cents)} ${on}. ` +
    `You'll only be charged for actual time and purchases when the task completes — ` +
    `anything unused is released automatically.`
  );
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
