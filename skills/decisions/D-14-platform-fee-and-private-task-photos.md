# D-14: A 20% platform fee on service revenue, deducted from the supporter and never from a reimbursement; task photos move to a private bucket behind signed links

**Date:** 2026-09-26
**Status:** Accepted. Amends D-13 item 1 (the promo subsidy is now the NET remainder, see below).
**Trigger:** Three fixes batch: the OTP screen had no way back to the email field; payouts sent 100% of the settlement (`ApplicationFeeBasisPoints = 0`); the requester could not read the receipt photo they were reimbursing, and the bucket serving it was public. Tier 3 — a migration on `payouts`, pricing-adjacent logic, and a storage authorization change.

## Decision

1. **`BillingConfig.PlatformFeeBps = 2000`** (was `ApplicationFeeBasisPoints = 0`). Applied ONLY to a task's service revenue — base fee + billable minutes + any approved time extension — and NEVER to a receipt reimbursement, which is the supporter's own money fronted and paid back in full. The requester's price is unchanged: no quote, hold, capture, receipt or copy on their side mentions it.

2. **The fee is computed once per task, on the undiscounted service, round half up** (`supporterPayFor` / `platformFeeCents`, payments_payouts.go). It is never computed per transfer, so a per-row rounding cannot take a cent more than the rate says. A promo-subsidised task pays the supporter 80% of the undiscounted service: the discount is the requester's and the platform's cost, not theirs.

3. **The transfers carry NET amounts, and the pay is laid out `[reimbursement, service-after-fee]` into `[hold, balance, promo]`.** Reimbursement first, because it is the supporter's own cash: if anything is short (a failed balance charge), it is earnings. The fee is attributed to the first split that carries any service, so it appears on exactly one row per task and every row reconciles. Consequence for D-13: the platform's promo subsidy is the net remainder — the discount less the fee it would otherwise have kept ($24.50 service, $10 promo → hold funds $14.50, subsidy $5.10, supporter $19.60), and a task that overran its hold by less than the fee no longer needs a second transfer.

4. **`payouts` rows carry the breakdown**: `service_cents` (gross), `fee_cents`, `reimbursement_cents`, `fee_bps`, with `payouts_breakdown_reconciles` enforcing `amount_cents = service_cents - fee_cents + reimbursement_cents` and forbidding a partial breakdown (the NOT NULL clauses are spelled out because a CHECK passes on NULL). All four are NULL on every row paid before this — no backfill, no reinterpretation — so "was this paid under the fee" is answerable from the row.

5. **Never a silent deduction.** `SupporterEarnings.time_cents` is now the service AFTER the fee (so an older client's two numbers still add up to its total) with `service_gross_cents`, `platform_fee_cents`, `platform_fee_bps` beside it; `EarningsTransfer` gains `service_gross_cents` / `platform_fee_cents` and the envelope `platform_fee_bps`; the settlement card and the Earnings row both read "$19.60 service (after 20% platform fee) + $12.40 reimbursement" from one helper per client (`earnedBreakdownLine` / `transferBreakdownLine`); the deposit notice (`PAYOUT_SENT`) and the supporter's completion notice carry the net figure and the same sentence (`payoutBreakdownLine`); Earnings prints "HO:RA takes 20% of service fees; purchase reimbursements are always paid back in full." with the rate from the backend.

6. **Task photos are served as SIGNED links behind the task's own read check** (task_photos.go). The `task-completions` bucket becomes private (`20260926_task_completions_bucket_private`); the database keeps the canonical public-form URL as a stable reference (a client echoing back a signed link is normalised at completion so a token never lands in a column); every read that hands a photo out — `GET /tasks/:id` (completion photo, now actually sent), `GET /tasks/:id/worklogs` (receipt photo), the completion email (7-day link) — signs it first. The upload endpoint answers `{url, signed_url}`.

7. **The requester sees the receipt beside the reimbursement it justifies**, with the recorded amount (and the reimbursed one when the budget capped it), tap-to-full-screen, on web and mobile. `GET /tasks/:id` now sends `completion_photo_url`, `completion_note`, `completed_at` on a completed task — both clients had typed and rendered these for months from a field no handler sent.

8. **The OTP screen has a way back and a way to resend**: "Use a different email" returns to an editable, pre-filled field and discards the pending code and its cooldown; "Resend code" re-sends to the same address behind a 30-second cooldown (`otpResend.js` / `otp-resend.ts`).

## Constitution impact
- Standards added: none.
- Standards modified/retired: none. S-05 holds — every figure the clients print is the server's, including the rate in the sentence.
- Invariants added/changed: none. The four Rule 3 queries still return three rows, none, none, none: `payouts` gains four nullable columns and a CHECK, no policy, no grant. CLAUDE.md Rule 2 is untouched — the task-photo bucket was never a permitted client-direct read; it was a public URL, which is now closed.

## Context and alternatives

**Why deduct from the supporter rather than reprice the requester.** The requester's price is a schedule they agreed to at post; the commission is the platform's share of the service it brokered. Same model as every marketplace the supporters already know.

**Why not commission the reimbursement.** It is not revenue. The supporter fronted it at the shop; taking 20% of it charges them for lending the platform their cash.

**Why the fee comes off the undiscounted service on a promo task.** The promo is the requester's discount. Computing 80% of the discounted figure would make the supporter pay for the platform's marketing.

**Why nullable breakdown columns and no backfill.** Existing paid rows were paid at 100%; rewriting them as "service X, fee 0" would describe a fee that never existed. NULL says exactly "paid before the fee".

**Why signed URLs and not a proxy.** A proxy through the Go backend would stream every photo through Render; a signed link is one small API call at read time and the bytes go straight from storage to the client. The link expires (1h screen, 7d email), and it is only ever minted inside a handler that has already authorized the caller for the task.

**Ops surfaces (reconciliation for e).** `POST /admin/payouts/:id/retry` re-sends `amount_cents` as recorded and never re-prices; it reads the breakdown for the audit row and copes with NULL. The ops routes never rendered payout amounts. `reportUnpayableSupporter` and `markPayoutFailed` audit/email the breakdown. D-13's "the platform pays the $10" is amended by item 3 above.

## Evidence
- `go test ./...` with `TEST_DATABASE_URL` against postgres:16 — green (33.4s), including `platform_fee_test.go`: fee on service only; reimbursement untouched; rounding pinned at 2000/250/1000 bps; splits at 20% incl. promo (hold $14.50 + subsidy $5.10 = 80% of $24.50) and a failed balance (reimbursement whole, service short); breakdown persisted and the CHECK refusing a partial or non-reconciling row; a legacy NULL-breakdown row read unchanged by Earnings; end-to-end settlement of a $24.50 + $12.40 task → requester charged $36.90, supporter transferred $32.00, both notices carry "$19.60 service (after the 20% platform fee) + $12.40 reimbursement". `task_photos_test.go`: URL parsing, token stripping, pass-through without storage.
- `app npm test` 106/106 (incl. `otpResend.test.mjs`, fee copy in `earningsCopy.test.mjs`); `app npm run build` ok.
- `mobile npm test` 39/39 (incl. `otp-resend.test.mjs`, `earnings-copy.test.mjs`); `mobile tsc --noEmit` clean; DESIGN.md §8 checks OK.
