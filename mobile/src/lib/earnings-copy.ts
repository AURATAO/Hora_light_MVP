import type { Earnings, EarningsTransfer, TransferDisplayStatus } from "./api";
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
