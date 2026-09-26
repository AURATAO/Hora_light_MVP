import type { Earnings, EarningsTransfer, TransferDisplayStatus } from "./api";
import type { SupporterEarnings } from "./types";
import { formatCost } from "./task-utils";

/**
 * Earnings copy — the supporter's side of the money, in words. Mirrors
 * app/src/lib/earningsCopy.js, which carries the tests.
 *
 *   1. A real number, always. "$0.00 paid out" is a fact; "isn't counted
 *      here" is a hedge, and hedges read as bad news.
 *   2. One word per row, and "paid" is reserved for money in the BANK.
 */

/** Under "Earned all time": where the money is, as two numbers. Empty when
 *  nothing has been earned. */
export function earningsWhereaboutsLine(
  data: Pick<Earnings, "lifetime_earned_cents" | "in_transit_cents" | "paid_out_cents"> | null,
): string {
  if (!data?.lifetime_earned_cents) return "";
  return `${formatCost(data.in_transit_cents ?? 0)} on its way to your bank · ${formatCost(
    data.paid_out_cents ?? 0,
  )} paid out`;
}

export const TRANSFER_STATUS_COPY: Record<TransferDisplayStatus, { label: string; note: string }> = {
  paid: { label: "Paid", note: "" },
  on_its_way: { label: "On its way", note: "" },
  failed: { label: "Failed", note: "We're on it — you won't lose this payment." },
};

/** The row's status line. An older backend without display_status degrades
 *  to the row status, where "paid" cannot tell bank from balance. */
export function transferStatusCopy(transfer: EarningsTransfer): { label: string; note: string } {
  const key: TransferDisplayStatus =
    transfer.display_status ?? (transfer.status === "failed" ? "failed" : "on_its_way");
  return TRANSFER_STATUS_COPY[key] ?? TRANSFER_STATUS_COPY.on_its_way;
}

/** "20%" for 2000 basis points, "2.5%" for 250 — never a rounded rate. */
export function platformFeePercent(bps: number | undefined): string {
  const n = Number(bps) || 0;
  if (n % 100 === 0) return `${n / 100}%`;
  return `${(n / 100).toFixed(2).replace(/\.?0+$/, "")}%`;
}

/**
 * The one-line explainer under Earnings. Reads the rate the backend sends so
 * the sentence and the arithmetic cannot disagree (S-05); an older backend
 * without it gets the shipped rate.
 */
export function platformFeeExplainer(bps: number = 2000): string {
  return `HO:RA takes ${platformFeePercent(bps)} of service fees; purchase reimbursements are always paid back in full.`;
}

/**
 * What a payment was made of, as a supporter reads it — the settlement's
 * `earned` block and an Earnings transfer row use the same words:
 *
 *   $19.60 service (after 20% platform fee) + $12.40 reimbursement
 *   $27.00 service + $12.40 reimbursement      ← sent before the fee existed
 *
 * `time` is the service AFTER the fee (the backend's time_cents); the fee
 * clause appears only when a fee was actually taken. Never a silent deduction.
 */
export function earningsBreakdownLine(input: {
  time: number;
  reimbursement: number;
  feeCents?: number;
  feeBps?: number;
}): string {
  const parts: string[] = [];
  const fee = input.feeCents ?? 0;
  if (input.time > 0 || fee > 0) {
    const clause = fee > 0 ? ` (after ${platformFeePercent(input.feeBps ?? 2000)} platform fee)` : "";
    parts.push(`${formatCost(input.time || 0)} service${clause}`);
  }
  if (input.reimbursement > 0) parts.push(`${formatCost(input.reimbursement)} reimbursement`);
  return parts.join(" + ");
}

/** The settlement card's `earned` block. */
export function earnedBreakdownLine(earned: SupporterEarnings): string {
  return earningsBreakdownLine({
    time: earned.time_cents,
    reimbursement: earned.reimbursement_cents,
    feeCents: earned.platform_fee_cents,
    feeBps: earned.platform_fee_bps,
  });
}

/** One Earnings transfer row. */
export function transferBreakdownLine(transfer: EarningsTransfer): string {
  return earningsBreakdownLine({
    time: transfer.time_cents,
    reimbursement: transfer.receipt_cents,
    feeCents: transfer.platform_fee_cents,
  });
}
