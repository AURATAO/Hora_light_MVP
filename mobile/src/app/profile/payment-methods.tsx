import { useCallback, useState } from "react";
import { Alert, Text, View } from "react-native";
import { useFocusEffect, useRouter } from "expo-router";
import { ChevronLeft, CreditCard } from "lucide-react-native";
import { Button, Card, EmptyState, PressableScale, Screen, Skeleton } from "../../components/ui";
import { ApiError, deletePaymentMethod } from "../../lib/api";
import {
  brandLabel,
  formatExpiry,
  getPaymentMethods,
  isCardExpired,
  presentAddCardSheet,
  type SavedCardRow,
} from "../../lib/payments";
import { color, size } from "../../theme/tokens";

/**
 * Profile → Payment methods. The whole card surface in the app: list, add,
 * remove.
 *
 * Card entry itself is Stripe's PaymentSheet (see lib/payments.ts for why);
 * everything around it — the rows, the empty state, the copy — is ours and
 * built from the DESIGN.md components.
 */

function CardRow({
  card,
  onRemove,
  removing,
}: {
  card: SavedCardRow;
  onRemove: (card: SavedCardRow) => void;
  removing: boolean;
}) {
  const expired = isCardExpired(card.exp_month, card.exp_year);

  return (
    <Card className="mb-2">
      <View className="flex-row items-center justify-between">
        <View className="flex-1 flex-row items-center gap-3 pr-2">
          <CreditCard color={color.muted} size={18} strokeWidth={size.iconStroke} />
          <View className="flex-1">
            <Text className="text-body font-semibold text-ink">
              {brandLabel(card.brand)} ···· {card.last4}
            </Text>
            <Text className={`mt-0.5 text-caption ${expired ? "text-danger" : "text-muted"}`}>
              {expired ? "Expired " : "Expires "}
              {formatExpiry(card.exp_month, card.exp_year)}
              {card.is_default && !expired ? " · used for new tasks" : ""}
            </Text>
          </View>
        </View>
        {/* A text action, not a second solid button: one solid CTA per screen
            belongs to "Add a card" (DESIGN.md §1). */}
        <PressableScale onPress={() => onRemove(card)} disabled={removing} hitSlop={8}>
          <Text className="text-caption font-semibold text-danger">
            {removing ? "Removing…" : "Remove"}
          </Text>
        </PressableScale>
      </View>
    </Card>
  );
}

export default function PaymentMethodsScreen() {
  const router = useRouter();

  const [cards, setCards] = useState<SavedCardRow[]>([]);
  const [loading, setLoading] = useState(true);
  // Stripe isn't configured on the backend at all. The screen says so once
  // rather than showing an empty list and an Add button that 503s.
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [removingId, setRemovingId] = useState<string | null>(null);

  function handleAuthError(e: unknown): boolean {
    if (e instanceof ApiError && e.isAuthError) {
      router.replace("/(auth)/login");
      return true;
    }
    return false;
  }

  const load = useCallback(async () => {
    try {
      const res = await getPaymentMethods();
      setCards(res.cards ?? []);
      setUnavailable(false);
      setError(null);
    } catch (e) {
      if (handleAuthError(e)) return;
      if (e instanceof ApiError && e.status === 503) {
        setUnavailable(true);
      } else {
        setError(e instanceof Error ? e.message : "Couldn't load your cards");
      }
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusEffect(
    useCallback(() => {
      load();
    }, [load])
  );

  async function handleAdd() {
    setAdding(true);
    try {
      const outcome = await presentAddCardSheet();
      if (outcome.status === "failed") {
        Alert.alert("Couldn't save that card", outcome.message);
      }
      // Re-read either way: on success to show what Stripe actually saved, and
      // on cancel because the sheet can save a card and then be dismissed.
      if (outcome.status !== "canceled") await load();
    } catch (e) {
      if (handleAuthError(e)) return;
      Alert.alert("Couldn't add a card", e instanceof Error ? e.message : "Try again.");
    } finally {
      setAdding(false);
    }
  }

  function handleRemove(card: SavedCardRow) {
    Alert.alert(
      "Remove this card?",
      `${brandLabel(card.brand)} ending ${card.last4} will no longer be used for new tasks.`,
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Remove",
          style: "destructive",
          onPress: async () => {
            setRemovingId(card.id);
            try {
              await deletePaymentMethod(card.id);
              setCards((prev) => prev.filter((c) => c.id !== card.id));
            } catch (e) {
              if (handleAuthError(e)) return;
              // A 409 means the card is holding funds for a task in flight;
              // the backend names the situation, and its wording is more use
              // than anything generic written here.
              const message =
                e instanceof ApiError && typeof (e.body as { message?: string })?.message === "string"
                  ? (e.body as { message: string }).message
                  : "Couldn't remove that card.";
              Alert.alert("Card still in use", message);
            } finally {
              setRemovingId(null);
            }
          },
        },
      ]
    );
  }

  return (
    <Screen>
      <View className="mb-6 mt-4 flex-row items-center">
        <PressableScale
          onPress={() => router.back()}
          className="mr-1 h-11 w-11 items-center justify-center rounded-pill"
          hitSlop={8}
        >
          <ChevronLeft color={color.ink} size={22} strokeWidth={size.iconStroke} />
        </PressableScale>
        <Text className="text-title font-semibold text-ink">Payment methods</Text>
      </View>

      {loading ? (
        <View className="gap-2">
          <Skeleton className="h-[76px]" />
          <Skeleton className="h-[76px]" />
        </View>
      ) : unavailable ? (
        <EmptyState
          icon={CreditCard}
          title="Not available yet"
          caption="Card payments aren't switched on for this account. Nothing to do here for now."
        />
      ) : error ? (
        <EmptyState
          icon={CreditCard}
          title="Couldn't load your cards"
          caption={error}
          actionLabel="Retry"
          onAction={load}
        />
      ) : (
        <>
          <Text className="mb-4 text-caption text-muted">
            Posting a task places a hold on your card. You're only charged for the time actually
            worked — the rest is released.
          </Text>

          {cards.length === 0 ? (
            <EmptyState
              icon={CreditCard}
              title="Add a card"
              caption="You'll need one to post a task. Nothing is charged until a task is done."
              actionLabel={adding ? "Opening…" : "Add a card"}
              onAction={handleAdd}
            />
          ) : (
            <>
              {cards.map((card) => (
                <CardRow
                  key={card.id}
                  card={card}
                  onRemove={handleRemove}
                  removing={removingId === card.id}
                />
              ))}
              <Button
                label="Add another card"
                variant="secondary"
                onPress={handleAdd}
                loading={adding}
                className="mt-2"
              />
            </>
          )}
        </>
      )}
    </Screen>
  );
}
