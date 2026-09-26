# D-13: Promo codes are requester-only discounts absorbed by the platform; receipts are sent from the settlement path that captures

**Date:** 2026-09-23
**Status:** Accepted. Item 1's payout arithmetic is amended by D-14: since the 20% platform fee comes off the undiscounted service, the platform's promo subsidy is the NET remainder (the discount less the fee), not the whole discount.
**Trigger:** Build 13 batch (App Store candidate polish): promo codes, itemized payment receipts, and the supporter deposit push. Tier 3 — migrations, the payments and payouts tables, and pricing-adjacent logic.

## Decision

Six things are now true that weren't:

1. **A promo code is a fixed number of cents off what the REQUESTER pays and
   nothing off what the SUPPORTER is paid.** Off the hold at post, off the
   total at settlement, never below $0 (`promo.go` promoApplied/afterPromo).
   The supporter's payout is computed from the undiscounted time + receipt.
   The part the discount took off the requester's charge is a separate
   Stripe Transfer drawn on the platform's own balance — `payouts.funding =
   'promo_subsidy'`, `payment_id NULL` — because a transfer bound to a charge
   by `source_transaction` may not exceed that charge. Two transfers for one
   task is the honest shape of "the platform absorbs it".

2. **The discount lives on `promo_redemptions`, not on `tasks`**, as a
   snapshot of the code's amount at posting. Every money path reads it
   through `promoDiscountForTask`. Deactivating or editing a code afterwards
   cannot change the terms of a task already posted under it.

3. **Per-user-once and one-code-per-task are unique indexes**
   (`uq_promo_redemptions_user_code`, `uq_promo_redemptions_task`); the
   handler's check is advisory and the INSERT inside the post transaction
   has the last word. `max_redemptions` is counted under `SELECT … FOR
   UPDATE` on the code row in the same transaction.

4. **A redemption is released when the post comes to nothing** — the hold
   refused and the task discarded, a free cancel (unaccepted, or inside the
   grace window), an admin cancel or takedown. A cancel that charges keeps
   it: the discount was applied to that charge.

5. **A promo that takes the hold below Stripe's $0.50 minimum places no
   hold** but still writes an `authorized` payments row for $0 with no
   intent, so every later path finds the live payment it expects; Capture
   takes $0 from it and the discounted total is collected as a
   `completion_balance` charge. A remainder below $0.50 at settlement is
   waived (logged) and paid to the supporter as part of the subsidy rather
   than left as a balance due nobody can collect.

6. **The requester's receipt (`RECEIPT`) is sent from `settleCompletedTask`
   and `runSettlePass` and nowhere else** — the two places money leaves a
   card — so a charge cannot happen without one and exactly one is sent per
   charge (a completion that took the hold and a balance is one receipt
   naming both). Its lines come from the same readers the task screen uses
   (`readSettlementInputs().quote()`, `taskExtensionRecords`,
   `promoDiscountForTask`). The supporter's deposit push
   (`PAYOUT_DEPOSITED`) is sent once per Stripe bank payout from the
   `payout.paid` webhook only — never from the catch-up reconciler — and
   only when that event stamped at least one row.

## Constitution impact
- Standards added: none.
- Standards modified/retired: none. CLAUDE.md Rule 1's table list gains
  `promo_codes` and `promo_redemptions` (both money tables: a client-direct
  INSERT on redemptions is a self-granted discount).
- Invariants added/changed: none. The four Rule 3 queries still return three
  rows, none, none, none after `20260923104026_promo_codes.sql` — both new
  tables ship RLS-enabled with the explicit REVOKE, no policy, no grant.
- `payouts.payment_id` is now nullable with `payouts_funding_shape` tying
  it to `funding`. "One payout per payment" (`uq_payouts_payment_id`) is
  untouched; the subsidy has its own claim, `uq_payouts_promo_subsidy_task`.

## Context and alternatives

**Why not a single transfer for the undiscounted amount.** Bound to the
charge it fails Stripe's amount check; unbound it draws on the platform's
available balance, which is empty in test mode and often short in
production. Two transfers — one bound, one platform-funded — is the only
shape where the supporter's charge-funded money never waits on the
platform's float.

**Why the redemption is written at post and not at settlement.** The hold
is placed at post from the discounted amount; the discount has to be
decided, and locked against concurrent posts, before the Stripe call.

**Why the receipt is not in the handlers.** Four paths charge a card
(complete, force-complete, a cancel that billed, settle-balance). A receipt
in each is four places to forget one; a receipt beside the capture is a
property of the code.

**Ops surface.** Endpoints (`GET/POST /admin/promo-codes`,
`POST /admin/promo-codes/:id/deactivate`, allowlisted and audited) plus a
Promo codes tab on the web ops panel. Codes are never edited: a task
posted under one carries a snapshot, so editing would only misdescribe
history.

**Companionship** (also in this batch) needed no pricing change —
`BillingConfig.BaseFeeCompanionshipCents` has been $25 since the billing
restructure. Enabling it was emptying `DISABLED_CATEGORIES` on mobile; the
hold confirmation gained `base_fee_cents`/`minutes_cost_cents` on
`TaskPayment` so it can say "$25.00 base + $7.50 time" without a client
subtracting anything (S-05).

## Evidence
- `go test ./...` with `TEST_DATABASE_URL` against postgres:16 — green
  (26.9s), including `promo_test.go` (10 cases) and `receipts_test.go`
  (5 cases): hold and capture use the discounted amount, payouts sum to the
  undiscounted amount with a `promo_subsidy` row, per-user-once at the
  index, first_task_only, expiry, discount line on the receipt, one receipt
  per charge, one deposit push per `payout.paid`.
- `mobile npm test` 32/32 (teardown + sheet accessibility);
  `mobile tsc --noEmit` clean; `app npm test` 99/99; `app npm run build` ok.
