import { useCallback, useState } from "react";
import { RefreshControl, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import * as WebBrowser from "expo-web-browser";
import { Banknote, CheckCircle2, ChevronLeft } from "lucide-react-native";
import { Button, Card, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import {
  ApiError,
  createLoginLink,
  createOnboardingLink,
  getEarnings,
  type Earnings,
  type EarningsTransfer,
  type OnboardingState,
} from "../../lib/api";
import { color, size } from "../../theme/tokens";

/**
 * Profile → Earnings. The whole payout surface in the app.
 *
 * Three states, and this screen is a machine over the one word the backend
 * sends:
 *
 *   not_started  no connected account. One CTA, one sentence.
 *   in_progress  Stripe wants more. Same CTA, different words, plus how much.
 *   complete     lifetime earned, recent transfers, and a way into Stripe's
 *                Express dashboard.
 *
 * WHAT THIS DELIBERATELY IS NOT. No wallet, no balance, no withdraw button.
 * Stripe pays out daily and automatically to the supporter's bank; building a
 * manual withdraw on top would mean holding somebody's money and inventing a
 * second, worse payout system beside the one that already works. "Manage
 * payouts" hands them to Stripe's dashboard, which is the complete record.
 *
 * NO NATIVE MODULE. Account Links are ordinary https URLs, opened in the
 * system in-app browser (expo-web-browser, already a dependency for the legal
 * pages). Nothing here needs an EAS rebuild to work — see the deploy note.
 */

/** Integer cents → "$12.34". Formatting, never pricing (S-05). */
function formatCents(cents: number): string {
  return `$${((cents ?? 0) / 100).toFixed(2)}`;
}

const COPY: Record<OnboardingState, { title: string; body: string; cta: string }> = {
  not_started: {
    title: "Set up payouts to start earning",
    body: "Add your bank details through Stripe. It takes a couple of minutes and you only do it once.",
    cta: "Set up payouts",
  },
  in_progress: {
    title: "Finish setting up payouts",
    body: "Stripe still needs a few details before we can pay you.",
    cta: "Continue setup",
  },
  complete: {
    title: "Payouts are set up",
    body: "Payments land in your bank automatically.",
    cta: "Manage payouts",
  },
};

function TransferRow({ transfer }: { transfer: EarningsTransfer }) {
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
            {formatCents(transfer.time_cents)} time
            {transfer.receipt_cents > 0
              ? ` + ${formatCents(transfer.receipt_cents)} reimbursement`
              : ""}
          </Text>
        </View>
        <View className="items-end">
          <Text className="text-body font-semibold text-ink">
            {formatCents(transfer.amount_cents)}
          </Text>
          {transfer.status !== "paid" ? (
            <Text className="mt-0.5 text-caption text-muted">
              {transfer.status === "failed" ? "We're sorting this out" : "On its way"}
            </Text>
          ) : null}
        </View>
      </View>
    </Card>
  );
}

export default function EarningsScreen() {
  const router = useRouter();

  const [data, setData] = useState<Earnings | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await getEarnings());
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 503)) {
        setError("Payouts aren't switched on yet. Please check back shortly.");
      } else {
        setError(e instanceof Error ? e.message : "Couldn't load your earnings");
      }
    } finally {
      setLoading(false);
    }
  }, []);

  // Re-read on every focus, which is what makes returning from the browser
  // work: dismissing the in-app browser refocuses this screen, and the account
  // is re-read from Stripe. Nothing is passed back through the redirect —
  // Stripe carries no state on it by design, and arriving at the return URL
  // means only that the flow was entered and exited, never that it completed.
  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  async function onRefresh() {
    setRefreshing(true);
    await load();
    setRefreshing(false);
  }

  async function startOnboarding() {
    if (busy) return;
    setBusy(true);
    try {
      const { url } = await createOnboardingLink();
      // openAuthSessionAsync, not openBrowserAsync: it dismisses itself the
      // moment Stripe redirects to our return URL, so the supporter lands back
      // here instead of on a page they have to close by hand.
      await WebBrowser.openAuthSessionAsync(url, null);
      await load();
    } catch (e) {
      setError(
        e instanceof ApiError && e.status === 503
          ? "Payouts aren't switched on yet. Please check back shortly."
          : "Couldn't start payout setup. Try again in a moment.",
      );
    } finally {
      setBusy(false);
    }
  }

  async function openDashboard() {
    if (busy) return;
    setBusy(true);
    try {
      const { url } = await createLoginLink();
      await WebBrowser.openBrowserAsync(url);
    } catch {
      setError("Couldn't open your payouts dashboard.");
    } finally {
      setBusy(false);
    }
  }

  const state: OnboardingState = data?.onboarding?.state ?? "not_started";
  const copy = COPY[state];
  const done = state === "complete";
  const due = data?.onboarding?.requirements_due ?? [];

  return (
    <Screen
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={color.muted} />
      }
    >
      <PressableScale
        onPress={() => router.back()}
        hitSlop={8}
        className="mb-4 mt-2 min-h-11 flex-row items-center"
      >
        <ChevronLeft color={color.ink} size={20} strokeWidth={size.iconStroke} />
        <Text className="text-body text-ink">Back</Text>
      </PressableScale>

      <Text className="mb-6 text-display text-ink">Earnings</Text>

      {loading ? (
        <View className="gap-3">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-20 w-full" />
        </View>
      ) : (
        <>
          {error ? <Text className="mb-4 text-caption text-danger">{error}</Text> : null}

          <View className="mb-6">
            <Card>
              <View className="flex-row items-start gap-3">
                {done ? (
                  <CheckCircle2 color={color.brand} size={18} strokeWidth={size.iconStroke} />
                ) : (
                  <Banknote color={color.muted} size={18} strokeWidth={size.iconStroke} />
                )}
                <View className="flex-1">
                  <Text className="text-body font-semibold text-ink">{copy.title}</Text>
                  <Text className="mt-0.5 text-caption text-muted">{copy.body}</Text>
                  {/* The COUNT, never Stripe's field names — it spells them
                      "individual.verification.document", which tells a
                      supporter nothing and reads like an error. The hosted
                      form is what explains them. */}
                  {!done && due.length > 0 ? (
                    <Text className="mt-0.5 text-caption text-muted">
                      {due.length} {due.length === 1 ? "detail" : "details"} still needed.
                    </Text>
                  ) : null}
                </View>
              </View>
            </Card>

            <View className="mt-3">
              {done ? (
                // A text action, not a second solid button: the one solid CTA
                // on this screen belongs to setting payouts up (DESIGN.md §1).
                <PressableScale
                  onPress={openDashboard}
                  disabled={busy}
                  hitSlop={8}
                  className="min-h-11 justify-center"
                >
                  <Text className="text-caption font-semibold text-brand">
                    {busy ? "Opening…" : copy.cta}
                  </Text>
                </PressableScale>
              ) : (
                <Button label={copy.cta} onPress={startOnboarding} loading={busy} />
              )}
            </View>
          </View>

          {done ? (
            <>
              <View className="mb-6">
                <Text className="mb-2 text-title font-semibold text-ink">Earned all time</Text>
                <Card>
                  <Text className="text-display text-ink">
                    {formatCents(data?.lifetime_earned_cents ?? 0)}
                  </Text>
                  <Text className="mt-0.5 text-caption text-muted">
                    Paid into your bank. Money still on its way isn't counted here.
                  </Text>
                </Card>
              </View>

              <View className="mb-8">
                <Text className="mb-2 text-title font-semibold text-ink">Recent</Text>
                {(data?.transfers ?? []).length === 0 ? (
                  <EmptyState
                    icon={Banknote}
                    title="No payments yet"
                    caption="Complete a task and your first payment appears here."
                  />
                ) : (
                  (data?.transfers ?? []).map((t) => (
                    <TransferRow key={`${t.task_id}-${t.created_at}`} transfer={t} />
                  ))
                )}
              </View>
            </>
          ) : null}
        </>
      )}
    </Screen>
  );
}
