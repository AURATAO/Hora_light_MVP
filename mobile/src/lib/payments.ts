import {
  handleNextAction,
  initPaymentSheet,
  initStripe,
  presentPaymentSheet,
} from "@stripe/stripe-react-native";
import { ApiError } from "./api-error";
import { confirmTaskPayment, createSetupIntent, getPaymentMethods } from "./api";

/**
 * Stripe orchestration for the app. Everything native-SDK-shaped lives here so
 * no screen imports @stripe/stripe-react-native directly.
 *
 * WHY PAYMENTSHEET AND NOT CARDFIELD. CardField gives you a styled text input
 * and leaves everything around it as your problem: no 3DS presentation, no
 * Apple Pay, no saved-card list, no per-country field rules (postal code where
 * it is required and not where it isn't), and a validation surface you own and
 * must keep current with the card networks. PaymentSheet is Stripe's own
 * native UI — it handles 3DS/SCA end to end, presents Apple Pay when the
 * device has it, localises itself, and is the component Stripe actually
 * regression-tests against issuer behaviour. The cost is that it looks like
 * Stripe rather than like HO:RA, which is the correct trade for the one screen
 * in the app where looking like a bank is reassuring rather than off-brand:
 * a card sheet that looks bespoke reads as a phishing form.
 *
 * The one thing that IS ours is where the sheet is entered from and what
 * surrounds it — the card list, the empty state, the error copy. Those are
 * DESIGN.md components; only the card entry itself is Stripe's.
 *
 * KEYS. The publishable key comes from the backend on every call that needs
 * it, so rotating it is one Render env change rather than a native rebuild.
 * EXPO_PUBLIC_STRIPE_PUBLISHABLE_KEY is the fallback for a build talking to a
 * backend that has not been given one yet.
 */

const FALLBACK_PUBLISHABLE_KEY = process.env.EXPO_PUBLIC_STRIPE_PUBLISHABLE_KEY ?? "";

// initStripe is a native call and is idempotent per key, but calling it on
// every sheet present is still a bridge round trip for nothing. Remembering
// the last key applied keeps it to once per key per process — and re-running
// it when the key CHANGES is the point: a backend that rotates its key mid
// session must not keep being handed the old one.
let appliedKey = "";

async function applyPublishableKey(key: string): Promise<void> {
  const publishableKey = key || FALLBACK_PUBLISHABLE_KEY;
  if (!publishableKey) {
    throw new Error("Card payments aren't set up yet. Please try again later.");
  }
  if (publishableKey === appliedKey) return;
  // No merchantIdentifier: that is Apple Pay's, and HO:RA has no Apple Pay
  // merchant ID registered. Passing one that does not exist makes PaymentSheet
  // offer a button that fails; omitting it makes the sheet card-only, which is
  // what this phase actually supports.
  await initStripe({ publishableKey });
  appliedKey = publishableKey;
}

/** What a card sheet or a challenge can end as. */
export type PaymentOutcome =
  | { status: "done" }
  /** The person closed the sheet. Not an error — nothing to show. */
  | { status: "canceled" }
  | { status: "failed"; message: string };

/**
 * Save a card: mint a SetupIntent on the backend, present PaymentSheet in
 * setup mode, done. 3DS at save time happens inside the sheet.
 *
 * Nothing is returned about the card itself — the caller re-reads
 * getPaymentMethods() afterwards, so what the UI shows is what Stripe actually
 * has rather than what this call hoped it saved.
 */
export async function presentAddCardSheet(): Promise<PaymentOutcome> {
  let session;
  try {
    session = await createSetupIntent();
  } catch (e) {
    if (e instanceof ApiError && e.status === 503) {
      return { status: "failed", message: "Card payments are temporarily unavailable." };
    }
    throw e;
  }

  await applyPublishableKey(session.publishable_key);

  const init = await initPaymentSheet({
    merchantDisplayName: session.merchant_display_name || "HO:RA",
    customerId: session.customer_id,
    customerEphemeralKeySecret: session.ephemeral_key,
    setupIntentClientSecret: session.client_secret,
    // The card has to work at post time with nobody looking at the phone.
    // Without this the sheet collects a card the issuer expects a challenge
    // for on every single use, which turns every post into a 3DS prompt.
    allowsDelayedPaymentMethods: false,
    returnURL: "hora://stripe-redirect",
  });
  if (init.error) {
    return { status: "failed", message: init.error.message };
  }

  const result = await presentPaymentSheet();
  if (result.error) {
    // Stripe reports a dismissed sheet as an error with code "Canceled".
    // Treating that as a failure would pop an alert every time somebody
    // changes their mind.
    if (result.error.code === "Canceled") return { status: "canceled" };
    return { status: "failed", message: result.error.message };
  }
  return { status: "done" };
}

/**
 * The 3DS second half of a post.
 *
 * An off-session hold that the issuer wants the cardholder present for comes
 * back from POST /tasks as a 402 carrying the intent's client secret. The task
 * already exists, parked and invisible to everyone; running the challenge and
 * then asking the backend to read the outcome is what posts it.
 *
 * The backend re-reads the intent from Stripe rather than trusting this call,
 * so nothing here can post a task that was not actually paid for.
 */
export async function completeCardAuthentication(details: {
  publishable_key?: string;
  client_secret?: string;
  task_id?: string;
}): Promise<PaymentOutcome> {
  const { publishable_key, client_secret, task_id } = details;
  if (!client_secret || !task_id) {
    return { status: "failed", message: "We couldn't complete that payment. Try posting again." };
  }

  await applyPublishableKey(publishable_key ?? "");

  const { error } = await handleNextAction(client_secret);
  if (error) {
    // Whether the bank refused or the requester backed out, the task stays
    // unposted — the confirm below is what tells the backend to discard it,
    // so an abandoned challenge doesn't leave a row parked forever.
    await confirmTaskPayment(task_id).catch(() => {});
    if (error.code === "Canceled") return { status: "canceled" };
    return { status: "failed", message: error.message };
  }

  try {
    await confirmTaskPayment(task_id);
    return { status: "done" };
  } catch (e) {
    return {
      status: "failed",
      message: paymentMessageFrom(e) ?? "That payment wasn't approved. Try another card.",
    };
  }
}

/**
 * What a failed POST /tasks means, and what the screen should do next.
 *
 *   'authenticate' — run completeCardAuthentication with `payment`
 *   'card'         — send the requester to Payment methods
 *   'other'        — an ordinary error; show `message`
 */
export type PostFailure =
  | { kind: "authenticate"; payment: Record<string, unknown>; message: string }
  | { kind: "card"; message: string }
  | { kind: "other"; message: string };

export function readPostFailure(e: unknown): PostFailure {
  if (!(e instanceof ApiError) || e.status !== 402) {
    return {
      kind: "other",
      message: e instanceof Error ? e.message : "Couldn't post your task. Try again.",
    };
  }
  const body = (e.body ?? {}) as Record<string, unknown>;
  const message = typeof body.message === "string" ? body.message : "";

  if (body.error === "payment_authentication_required") {
    return { kind: "authenticate", payment: body, message };
  }
  return {
    kind: "card",
    message:
      message ||
      (body.error === "payment_method_required"
        ? "Add a card before posting a task."
        : "We couldn't place a hold on your card. Try another card."),
  };
}

/** The backend's own wording for a payment failure, when it sent one. */
function paymentMessageFrom(e: unknown): string | null {
  if (!(e instanceof ApiError)) return null;
  const body = (e.body ?? {}) as Record<string, unknown>;
  return typeof body.message === "string" && body.message ? body.message : null;
}

/** Brand slugs as Stripe spells them → what a person calls the card. */
const BRAND_LABELS: Record<string, string> = {
  visa: "Visa",
  mastercard: "Mastercard",
  amex: "American Express",
  discover: "Discover",
  diners: "Diners Club",
  jcb: "JCB",
  unionpay: "UnionPay",
};

export function brandLabel(brand: string): string {
  return BRAND_LABELS[brand] ?? "Card";
}

/** "04 / 2029" — zero-padded, because "4 / 2029" doesn't read as an expiry. */
export function formatExpiry(month: number, year: number): string {
  return `${String(month).padStart(2, "0")} / ${year}`;
}

/**
 * True once the card is past its printed expiry. The printed month is still
 * good — a card marked 04/29 works to the end of April 2029 — so this compares
 * against the first day of the following month.
 */
export function isCardExpired(month: number, year: number): boolean {
  return new Date(year, month, 1) <= new Date();
}

/** Re-exported so screens never import the SDK directly. */
export { getPaymentMethods };
export type { SavedCard as SavedCardRow } from "./api";
