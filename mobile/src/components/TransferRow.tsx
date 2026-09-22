import { Text, View } from "react-native";
import { Card } from "./ui";
import { formatCost } from "../lib/task-utils";
import { transferStatusCopy } from "../lib/earnings-copy";
import type { EarningsTransfer } from "../lib/api";

/**
 * One payout, as a supporter reads it.
 *
 * Shared by the earnings strip and the full history screen, so the split
 * below — what they MADE versus what they are being handed back — is worded
 * once. A supporter who sees one total for a shopping task cannot tell those
 * apart, and they are very different numbers.
 */
export function TransferRow({ transfer }: { transfer: EarningsTransfer }) {
  const status = transferStatusCopy(transfer);
  const failed = status.label === "Failed";
  return (
    <Card className="mb-2">
      <View className="flex-row items-start justify-between gap-3">
        <View className="flex-1">
          <Text className="text-body font-semibold text-ink" numberOfLines={1}>
            {transfer.task_title || "Task"}
          </Text>
          {/* The split, said out loud. A supporter who sees one total for a
              shopping task cannot tell what they MADE from what they are being
              handed back, and those are very different numbers. */}
          <Text className="mt-0.5 text-caption text-muted">
            {formatCost(transfer.time_cents)} time
            {transfer.receipt_cents > 0
              ? ` + ${formatCost(transfer.receipt_cents)} reimbursement`
              : ""}
          </Text>
        </View>
        <View className="items-end">
          <Text className="text-body font-semibold text-ink">
            {formatCost(transfer.amount_cents)}
          </Text>
          {/* Always a word — Paid / On its way / Failed. A failed row used
              to render exactly like a paid one. */}
          <Text className={`mt-0.5 text-caption ${failed ? "text-danger" : "text-muted"}`}>
            {status.label}
          </Text>
          {status.note ? (
            <Text className="mt-0.5 max-w-[180px] text-right text-caption text-muted">
              {status.note}
            </Text>
          ) : null}
        </View>
      </View>
    </Card>
  );
}
