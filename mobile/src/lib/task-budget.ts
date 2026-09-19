/**
 * "Does this task owe a receipt, and up to how much."
 *
 * ONE RULE, AND IT IS A LIFECYCLE RULE. Every per-task payment decision keys
 * off the TASK's own state — has it a budget, has it a payment attached —
 * never off the live PAYMENTS_ENFORCED flag. The flag governs whether NEW
 * tasks require a card and a hold, and nothing else. A task posted while it
 * was on and completed after it went off is still a task with a budget, and
 * the server still refuses a completion that says nothing about the receipt.
 * Anything on this screen that asked the flag instead would change its mind
 * underneath a task already in flight.
 *
 * WHY THE FALLBACK CHAIN. Three sources carry the same server-side column,
 * tasks.shopping_budget_approved_cents — the one completeTask validates
 * against — and they differ only in freshness and in whether the client is
 * guaranteed to have them:
 *
 *   extensions   freshest. An approved mid-task budget increase raises the
 *                ceiling here first. But it is POLLED, and only while the task
 *                is active (useFocusEffect, gated on isTaskActive), and its
 *                failures are swallowed on purpose.
 *   settlement   arrives with the worklogs payload, itself fetched with
 *                `.catch(() => null)`.
 *   task         always there. A screen that has no task renders nothing at
 *                all, so this cannot be the missing one.
 *
 * Freshest first, always-present last. The bug this ordering exists for: a
 * supporter holding neither of the first two saw NO receipt field while the
 * server demanded one, and could not complete the task from the app at all.
 *
 * Reading low is safe and reading zero is not. The server is the authority on
 * the ceiling and says the real number in its refusal, so a stale-but-present
 * value costs at worst one corrected round trip; a zero costs the supporter
 * the ability to finish the job.
 *
 * Shared with web (app/src/lib/taskBudget.js) so the two clients cannot drift
 * into disagreeing about when a receipt is owed.
 */
export function approvedBudgetCentsFor(input: {
  extensions?: { approved_budget_cents?: number | null } | null;
  settlement?: { approved_budget_cents?: number | null } | null;
  task?: { shopping_budget_approved_cents?: number | null } | null;
}): number {
  const sources = [
    input.extensions?.approved_budget_cents,
    input.settlement?.approved_budget_cents,
    input.task?.shopping_budget_approved_cents,
  ];
  for (const value of sources) {
    if (typeof value === "number" && Number.isFinite(value)) return Math.max(0, value);
  }
  return 0;
}

/**
 * Whether the completion sheet must show the receipt step.
 *
 * Above zero the server REFUSES a completion that omits the receipt, so this
 * is not decoration — it is the difference between a completable task and a
 * dead end.
 */
export function needsReceipt(budgetCents: number): boolean {
  return budgetCents > 0;
}
