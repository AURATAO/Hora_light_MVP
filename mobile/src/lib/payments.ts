import {
  PlatformPay,
  handleNextAction,
  initPaymentSheet,
  initStripe,
  presentPaymentSheet,
} from "@stripe/stripe-react-native";
import { ApiError } from "./api-error";
import { confirmTaskPayment, createSetupIntent, getPaymentMethods } from "./api";
import type { TaskPayment } from "./types";

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
 *
 * WALLETS AND OFF-SESSION — READ THIS BEFORE TRUSTING APPLE PAY HERE.
 *
 * This sheet SAVES a payment method; the money is taken later, off-session,
 * when a task is posted (CreatePreAuth in server/payments.go confirms with
 * OffSession: true). For a plain card that is exactly what the SetupIntent's
 * usage=off_session buys.
 *
 * Apple Pay is not a plain card. Stripe classifies it as customer-initiated,
 * and what PaymentSheet saves in setup mode is a device-bound token (a DPAN).
 * For merchant-initiated charges — anything taken without the customer in front
 * of the sheet — Stripe's guidance is to use Apple merchant tokens (MPANs),
 * which this integration cannot request because they require a fixed billing
 * cycle that HO:RA's variable, deferred charges do not have.
 *
 * So the open question is whether a saved Apple Pay method survives the
 * off-session pre-auth at post, or is declined with authentication_required.
 * It CANNOT be answered offline or with Stripe test cards: Apple Pay testing
 * needs a real card in a real Wallet against test keys. Until somebody posts a
 * task on a device with an Apple Pay method saved and watches what the hold
 * does, treat this as unproven.
 *
 * The blast radius if it is wrong is bounded and known: the requester's post is
 * refused with a 402 they can retry with a different card, exactly as any
 * decline is handled today (readPostFailure below). No task is created and no
 * money moves. It is a bad experience, not a broken ledger — and it is entirely
 * behind PAYMENTS_ENFORCED, which is off.
 */

const FALLBACK_PUBLISHABLE_KEY = process.env.EXPO_PUBLIC_STRIPE_PUBLISHABLE_KEY ?? "";

/**
 * The Apple Pay merchant identifier.
 *
 * MUST MATCH `app.json` exactly — the Stripe config plugin writes this same
 * string into the `com.apple.developer.in-app-payments` entitlement at prebuild,
 * and Apple Pay fails at runtime if the value the SDK is initialised with is not
 * in that entitlement. Two copies of one string is unfortunate; the alternative
 * (reading it back out of `Constants.expoConfig.plugins`) means parsing a plugin
 * tuple at runtime and getting `undefined` in any build where the shape changed,
 * which fails the same way but silently.
 *
 * BUILD-TIME, NOT RUNTIME. The entitlement is compiled into the binary, so this
 * cannot be switched by an env var or a server response the way the publishable
 * key can. Changing it needs a new native build.
 */
const APPLE_PAY_MERCHANT_ID = "merchant.co.horaapp.hora";

/**
 * The country of the BUSINESS, not the customer — Apple Pay wants to know where
 * the merchant of record is. Matches the platform Stripe account (US) and the
 * only market this beta operates in. Not the same field as the currency, which
 * comes from BillingConfig server-side.
 */
const APPLE_PAY_MERCHANT_COUNTRY = "US";

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
  // merchantIdentifier is what turns Apple Pay on. It must name a merchant ID
  // that is BOTH registered at developer.apple.com AND present in this binary's
  // entitlement — the config plugin handles the second half from app.json. A
  // merchant ID the entitlement does not carry makes PaymentSheet offer an
  // Apple Pay button that fails at authorization, which is worse than not
  // offering one, so these two values are kept in lockstep deliberately.
  await initStripe({ publishableKey, merchantIdentifier: APPLE_PAY_MERCHANT_ID });
  appliedKey = publishableKey;
}

/** What a card sheet or a challenge can end as. */
export type PaymentOutcome =
  /** `payment` is present only on the post-a-task challenge, where the confirm
   *  endpoint echoes back the hold that just landed — the success screen needs
   *  it to say what was reserved. Saving a card carries none. */
  | { status: "done"; payment?: TaskPayment }
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
    // 503 (Stripe unconfigured) and 404 (a build talking to a backend without
    // these routes yet) mean the same thing to the person holding the phone.
    if (e instanceof ApiError && (e.status === 503 || e.status === 404)) {
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
    // Cards only. This excludes methods that do not move money at checkout —
    // bank debits (SEPA, ACH) and voucher methods (OXXO, Konbini, Boleto) —
    // which report success and can still fail hours later. A pre-auth model
    // has nothing to hold on one of those, so they must not be offered.
    //
    // It is NOT what makes the saved card usable off-session; that is the
    // SetupIntent's usage=off_session, set server-side in payments_cards.go.
    // Worth being exact about: someone debugging "why does every post ask for
    // 3DS" will read this line first, and it is the wrong lever.
    allowsDelayedPaymentMethods: false,
    // Apple Pay, iOS only. Its presence here is what puts the button in the
    // sheet; the entitlement and the merchantIdentifier above are what make it
    // work. Android is unaffected — googlePay is a separate key, deliberately
    // left off (see the wallets note below).
    applePay: {
      merchantCountryCode: APPLE_PAY_MERCHANT_COUNTRY,
      // A SETUP sheet, not a purchase one: this screen saves a card for later,
      // it does not charge. ButtonType.SetUp makes the sheet say "Set Up" and
      // not "Pay", which is the difference between a correct affordance and one
      // that implies money is about to move.
      buttonType: PlatformPay.ButtonType.SetUp,
      // NO cartItems, and no `request`. Stripe's docs say to pass cartItems on a
      // SetupIntent "to display the amount you intend to charge" — but HO:RA has
      // no such amount at this moment. A card is saved from Profile, often days
      // before any task exists, and what it will eventually be charged depends
      // on a task not yet written, a duration not yet worked and a receipt not
      // yet produced. Inventing a number to fill the field would put a figure in
      // an Apple sheet that nobody is agreeing to and that will not match the
      // eventual charge.
      //
      // `request` (Apple merchant tokens / MPAN) is the documented route for
      // merchant-initiated charges, and it does not fit either: every variant
      // demands a fixed billing cycle — RecurringPaymentRequest wants an
      // intervalUnit, an intervalCount and an amount. HO:RA is deferred and
      // variable, not recurring. See the WALLETS AND OFF-SESSION note below for
      // what this means and what still has to be checked on a device.
    },
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
/**
 * Run the bank's challenge, and nothing else.
 *
 * Split out of completeCardAuthentication because settling an outstanding
 * balance needs exactly this half: there is no task to post, so there is
 * nothing to confirm afterwards — the settle endpoint is simply called again
 * and re-reads the intent's real state from Stripe.
 */
export async function runCardChallenge(details: {
  publishable_key?: string;
  client_secret?: string;
}): Promise<PaymentOutcome> {
  const { publishable_key, client_secret } = details;
  if (!client_secret) {
    return { status: "failed", message: "We couldn't complete that payment. Try again." };
  }
  await applyPublishableKey(publishable_key ?? "");
  const { error } = await handleNextAction(client_secret);
  if (error) {
    if (error.code === "Canceled") return { status: "canceled" };
    return { status: "failed", message: error.message };
  }
  return { status: "done" };
}

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
    const confirmed = await confirmTaskPayment(task_id);
    return { status: "done", payment: confirmed?.payment };
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
