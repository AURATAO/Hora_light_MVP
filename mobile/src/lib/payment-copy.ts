import type {
  ExtensionRecord,
  TaskCancellation,
  TaskPayment,
  TimeCap,
  TimeCapState,
} from "./types";
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

/**
 * The time a task is priced against, as one coherent line.
 *
 *   "Estimate: 30 min · up to 45 min with auto-extend"
 *
 * The supporter's card said "Paid time 41 of 45 min" while the requester's
 * said "based on 30 min" — two numbers, neither explaining the other, on the
 * screen where one of them decides whether to keep working. 45 is not a second
 * estimate; it is the estimate plus what the requester already agreed to.
 *
 * Mirrors app/src/lib/paymentCopy.js timeBasisNote word for word.
 */
export function timeBasisNote(cap?: TimeCap | null): string | null {
  const estimate = cap?.estimate_minutes ?? 0;
  const ceiling = cap?.cap_minutes ?? 0;
  if (!estimate || !ceiling) return null;

  const base = `Estimate: ${estimate} min`;
  if (ceiling <= estimate) return base;

  const autoExtend = (cap?.auto_extend_minutes ?? 0) > 0;
  const approved = (cap?.approved_extra_minutes ?? 0) > 0;
  const because = approved
    ? autoExtend
      ? "with auto-extend and approved extensions"
      : "with approved extensions"
    : "with auto-extend";
  return `${base} · up to ${ceiling} min ${because}`;
}

/**
 * The supporter-and-requester line for Layer 2, the early warning.
 *
 * The warning fires at the ESTIMATE rather than at the ceiling
 * (server/billing.go timeCapWarningMinutes), so on a 30-minute task with
 * auto-extend it lands at 25 logged minutes with 20 minutes of ceiling still
 * above it. "About 20 min left" is true of the ceiling and useless as a
 * warning: what the supporter needs is that they are at the number the
 * requester planned around, and that the 15 above it are a fuse rather than a
 * second estimate.
 *
 * Mirrors app/src/lib/paymentCopy.js capWarningNote word for word.
 */
export function capWarningNote(capState?: TimeCapState | null): string | null {
  if (!capState || capState.reached || !capState.warning) return null;
  const agreed = capState.cap?.agreed_minutes || capState.cap?.estimate_minutes || 0;
  const autoExtend = capState.cap?.auto_extend_minutes ?? 0;
  if (agreed > 0 && autoExtend > 0) {
    return `Approaching the ${agreed} min agreed — up to ${autoExtend} more minutes are covered by auto-extend.`;
  }
  const remaining = capState.remaining_minutes ?? 0;
  return `About ${remaining} min left on the time that was agreed.`;
}

/**
 * "You'll be charged $12.00 (base fee); …" — what cancelling right now costs.
 *
 * All three figures come off the server's `cancellation` block (S-05). The
 * sheet this feeds used to say nothing at all about money on an accepted task,
 * because an accepted task could not be cancelled from the app.
 *
 * `withinGrace` is passed in rather than read off the block because the sheet
 * can sit open across the boundary: the block says what was true when it was
 * fetched, the live countdown says what is true now.
 *
 * Mirrors app/src/lib/paymentCopy.js cancelChargeLine word for word.
 */
export function cancelChargeLine(
  cancellation?: TaskCancellation | null,
  opts?: { withinGrace?: boolean }
): string | null {
  if (!cancellation?.committed) return null;
  if (opts?.withinGrace ?? cancellation.within_grace) {
    return "Your supporter has committed, but you\u2019re still inside the free window — cancelling now costs nothing.";
  }
  const base = formatCost(cancellation.base_fee_cents ?? 0);
  const minutes = cancellation.billed_minutes ?? 0;
  if ((cancellation.time_cost_cents ?? 0) > 0) {
    return `Your supporter has committed. You\u2019ll be charged ${formatCost(cancellation.charge_cents)} — ${base} base fee plus ${formatCost(cancellation.time_cost_cents)} for the ${minutes} min worked.`;
  }
  return `Your supporter has committed. You\u2019ll be charged ${base} (base fee).`;
}

/** "$64.75 of your reserved $76.75 releases immediately." Null when there is
 *  no hold — the sheet then says nothing rather than "$0.00 releases". */
export function cancelReleaseLine(
  cancellation?: TaskCancellation | null,
  payment?: TaskPayment | null,
  opts?: { withinGrace?: boolean }
): string | null {
  const authorized = payment?.authorized_cents ?? 0;
  if (!authorized) return null;
  if (!cancellation?.committed || (opts?.withinGrace ?? cancellation.within_grace)) {
    return `Your reserved ${formatCost(authorized)} will be released immediately.`;
  }
  const release = cancellation.release_cents ?? 0;
  if (release <= 0) return null;
  return `${formatCost(release)} of your reserved ${formatCost(authorized)} releases immediately.`;
}

/**
 * "1:23" — the free window, counted down against the SERVER's deadline.
 *
 * A deadline rather than a duration, so the clock drifts by however long one
 * request took instead of by however long the sheet has been open — which on a
 * phone that has been asleep is an unbounded amount. Null once it has run out.
 */
export function cancelGraceCountdown(
  graceEndsAt?: string | null,
  nowMs: number = Date.now()
): string | null {
  if (!graceEndsAt) return null;
  const left = Date.parse(graceEndsAt) - nowMs;
  if (!Number.isFinite(left) || left <= 0) return null;
  const total = Math.ceil(left / 1000);
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

/**
 * "$8.00 more budget" / "+30 min" — what a mid-task request asked for.
 *
 * The ADDITIONAL amount, which is what both parties were asked to approve; the
 * running total is the settlement's job.
 *
 * Mirrors app/src/lib/paymentCopy.js extensionAskLabel word for word.
 */
export function extensionAskLabel(request?: ExtensionRecord | null): string {
  if (!request) return "";
  if (request.kind === "budget") {
    return `${formatCost(request.requested_cents ?? 0)} more budget`;
  }
  return `+${request.requested_minutes ?? 0} min`;
}

/**
 * When it was settled, or when it was asked if it never was. A request that
 * outlived its task has no resolved_at, and dating the row by the ask is the
 * only honest thing left to show.
 */
export function extensionDecidedAt(request?: ExtensionRecord | null): string {
  const when = request?.resolved_at || request?.requested_at;
  if (!when) return "";
  const parsed = Date.parse(when);
  if (!Number.isFinite(parsed)) return "";
  return new Date(parsed).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
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
