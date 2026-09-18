import { useEffect, useState } from "react";
import { getPaymentMethods } from "./api";

/**
 * "Is the platform actually charging for tasks right now?" — the
 * `payments_enforced` flag from GET /payments/methods, and nothing else from
 * that payload.
 *
 * TRI-STATE ON PURPOSE. `null` means not yet known, and callers must keep it
 * distinct from `false`: the beta notice's settlement line says "settle
 * directly with your supporter" on a definite `false`, and that sentence must
 * never appear while payments are on (see betaSettlementLine in
 * ./beta-notice). Collapsing unknown into `false` would show the off-platform
 * wording for the moment before the request lands, and permanently on any
 * device where it fails.
 *
 * A FAILED READ STAYS `null`, which is the difference between this hook and
 * post-task's `refreshPaymentGate`. That one resolves a failure to "not
 * enforced" and is right to: it decides whether to show an advisory "add a
 * card" banner, and the 402 from POST /tasks is the real gate behind it. This
 * one decides which of two sentences about money to print, where being wrong
 * is not recoverable by a later request.
 *
 * `enabled` exists because GET /payments/methods reaches Stripe, and the sheet
 * that shows this line stays MOUNTED behind a `visible` prop. Without the gate
 * it would fetch on every Post Task entry to render a sentence the user is not
 * being shown — and would race the gate that same screen runs for its "add a
 * card" banner. Two concurrent reads share one Stripe idempotency key for
 * "create this user's customer" and the loser comes back 409, which the
 * handler answers as a 500. The web half of this feature was caught doing
 * exactly that in a local sweep.
 */
export function usePaymentsEnforced(enabled: boolean = true): boolean | null {
  const [enforced, setEnforced] = useState<boolean | null>(null);

  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    getPaymentMethods()
      .then((res) => {
        if (alive) setEnforced(!!res.payments_enforced);
      })
      .catch(() => {
        // Unknown, and it stays unknown. A 503 (no Stripe configured), an
        // expired session, or no network all land here; none of them is
        // evidence about the flag.
      });
    return () => {
      alive = false;
    };
  }, [enabled]);

  return enforced;
}
