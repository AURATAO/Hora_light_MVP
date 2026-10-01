import { formatCost } from "./task-utils";

/**
 * The price schedule, for COPY ONLY — marketing sentences that have to name a
 * number before any task exists to quote. Every price a user is actually
 * charged or paid comes from the server (S-05); nothing here feeds arithmetic
 * that reaches a card or a payout.
 *
 * A mirror of server/billing.go's BillingConfig, and pinned to it by
 * test/pricing.test.mjs, which reads the Go file: change the schedule there
 * and the mobile suite fails until this matches. That test exists because the
 * previous copy was hand-typed and outlived the schedule it described — it
 * still promised an "$18" tier and a "$33.60" hour after both were gone.
 */
export const PRICING = {
  baseFeeDefaultCents: 1200,
  baseFeeCompanionshipCents: 2500,
  perMinuteRateCents: 50,
  includedMinutes: 15,
  platformFeeBps: 2000,
} as const;

/** "$12" for whole dollars, "$0.50" otherwise. */
function dollars(cents: number): string {
  return cents % 100 === 0 ? `$${cents / 100}` : formatCost(cents);
}

/** What the requester is charged for the service on a standard-rate task. */
export function serviceCents(baseFeeCents: number, minutes: number): number {
  const billable = Math.max(0, minutes - PRICING.includedMinutes);
  return baseFeeCents + billable * PRICING.perMinuteRateCents;
}

/** The supporter's share of a service charge. The fee rounds half up, exactly
 *  as server/payments_payouts.go computes it. */
export function supporterPayoutCents(service: number): number {
  const fee = Math.floor((service * PRICING.platformFeeBps + 5000) / 10000);
  return service - fee;
}

/** The Home "Earn on your schedule" pitch. "**…**" marks bold spans. */
export function supporterPayPitch(): string {
  const keep = 100 - PRICING.platformFeeBps / 100;
  const hour = supporterPayoutCents(serviceCents(PRICING.baseFeeDefaultCents, 60));
  return (
    `Every task pays a base fee that covers the first ${PRICING.includedMinutes} minutes, plus ` +
    `${dollars(PRICING.perMinuteRateCents)} per minute of actual work after that — and you keep ` +
    `**${keep}%** of it all. A one-hour grocery run pays **${formatCost(hour)}**. Errands and ` +
    `deliveries start at a ${dollars(PRICING.baseFeeDefaultCents)} base, companionship at ` +
    `${dollars(PRICING.baseFeeCompanionshipCents)}.`
  );
}
