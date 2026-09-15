import { useCallback, useEffect, useState } from "react";
import { Text, View } from "react-native";
import { Button } from "./ui";
import { ApiError, getOutstandingBalance, settleBalance } from "../lib/api";
import { outstandingBalanceMessage } from "../lib/payment-copy";
import { runCardChallenge } from "../lib/payments";
import type { OutstandingBalance } from "../lib/types";

/**
 * The wall a requester hits when a completion could not be charged.
 *
 * WHY A PERSISTENT BANNER. The balance blocks posting (POST /tasks answers
 * 403), and a block whose reason was announced once, days ago, is
 * indistinguishable from a broken app. It stays until the money is settled.
 *
 * Renders NOTHING for everybody who owes nothing, which is very nearly
 * everybody — one read on focus, no polling. A balance can only appear when a
 * task completes, which does not happen while you stare at a screen, and the
 * 403 on the next post is the backstop.
 *
 * A supporter never sees this: they never have a balance, because payouts are
 * deliberately not gated on one. The platform carries the float — see
 * skills/payments-runbook.md.
 */
export function OutstandingBalanceBanner() {
  const [outstanding, setOutstanding] = useState<OutstandingBalance | null>(null);
  const [settling, setSettling] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const res = await getOutstandingBalance();
      setOutstanding(res?.outstanding ?? null);
    } catch {
      // Payments unconfigured (503), signed out, offline. Silence is right:
      // failing to confirm somebody is fine must not itself become a message.
      setOutstanding(null);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function settle() {
    setSettling(true);
    setError(null);
    try {
      await settleBalance();
      await refresh();
    } catch (e) {
      const body = e instanceof ApiError ? (e.body as Record<string, unknown> | null) : null;
      // The issuer wants the cardholder present. Not a failure — it is the one
      // outcome a retry can actually fix, and the reason this is a button
      // rather than a background job.
      if (body?.error === "payment_authentication_required" && typeof body.client_secret === "string") {
        const outcome = await runCardChallenge({
          publishable_key: typeof body.publishable_key === "string" ? body.publishable_key : undefined,
          client_secret: body.client_secret,
        });
        if (outcome.status === "failed") {
          setError(outcome.message);
        } else if (outcome.status === "done") {
          // The "confirm" is the settle call again: the server re-reads the
          // intent's real state rather than believing this client.
          try {
            await settleBalance();
          } catch (retryErr) {
            setError(messageFrom(retryErr));
          }
        }
        await refresh();
        return;
      }
      setError(messageFrom(e));
      await refresh();
    } finally {
      setSettling(false);
    }
  }

  const message = outstandingBalanceMessage(outstanding);
  if (!message) return null;

  return (
    <View className="mb-4 gap-3 rounded-card border border-danger bg-surface p-4">
      <Text className="text-caption text-danger">{message}</Text>
      {error ? <Text className="text-caption text-muted">{error}</Text> : null}
      <Button label="Settle now" onPress={settle} loading={settling} />
    </View>
  );
}

function messageFrom(e: unknown): string {
  if (e instanceof ApiError) {
    const body = e.body as { message?: string } | null;
    if (body?.message) return body.message;
  }
  return "That card was declined. Try another card.";
}
