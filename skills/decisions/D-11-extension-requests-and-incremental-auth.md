# D-11: One `extension_requests` table for both mid-task asks; incremental authorization is off by default because requesting it breaks posting

**Date:** 2026-09-14
**Status:** Accepted
**Trigger:** Stripe Phase 2b. The plan specified a `budget_increase_requests` table and left the generalization question open ("generalize into `extension_requests` … OR a parallel minimal table if generalizing is messier — your call, report it"). Separately, the plan assumed an approved budget increase would attempt a Stripe incremental authorization and fall back to a supplementary payment; the smoke test found that merely *requesting* the capability fails every pre-auth on this account.

## Decision

**1. One table, two kinds.** `public.extension_requests` carries
`kind IN ('budget','time')` with `requested_cents` / `requested_minutes` as
separate nullable columns, plus CHECK constraints making the correct column
mandatory per kind and the wrong one impossible. There is no
`budget_increase_requests` table and no parallel time table.

**2. One pending request per task PER KIND**, not per task. A supporter waiting
on "can I spend $8 more" can still ask "and I need 15 more minutes"; a second
request of the *same* kind is a 409.

**3. `requested_cents` / `requested_minutes` are DELTAS**, never new totals.
Approval adds to `tasks.shopping_budget_approved_cents` / to the time ceiling.

**4. Incremental authorization is behind `STRIPE_INCREMENTAL_AUTH`, default
off.** With it off, `CreatePreAuth` does not send
`payment_method_options[card][request_incremental_authorization]` and
`ensureHoldCoversTask` does not attempt an increment — an approved increase that
outgrows its hold opens a supplementary `kind='budget_increase'` PaymentIntent
for the shortfall instead.

## Constitution impact

- Standards added: none. S-05 (pricing computed in Go) and S-10 (deny-all RLS)
  both already cover the new surfaces; `extension_requests` ships with RLS
  enabled, zero policies and the default-privilege grants revoked, per the same
  reasoning as `payments` in 20260911120000.
- Standards modified/retired: none.
- Invariants added/changed: none. CLAUDE.md Rule 3's four queries were re-run
  after the migrations and are unchanged (evidence below).

## Context and alternatives

**On the table.** Both asks are the same shape of problem — "I need permission
for something that costs money, and I need it in the next couple of minutes" —
and they share the whole machinery: one pending at a time, a five-minute
timeout that resolves to a denial, lazy expiry applied on read, both sides
notified, and the authorized hold possibly having to grow. The only thing that
differs is the unit and whether a fallback is meaningful. Two tables would have
meant two expiry paths, two notification paths and two sweeps; the polymorphic
single-`amount` variant was rejected because it would make `requested_cents`
silently mean minutes on half the rows, which is the kind of unit collision that
costs money rather than merely being untidy.

The fallback fields are budget-only. A time request needs none: the fallback is
already the billing — time past the ceiling is not charged, so an unanswered
time request simply stops the meter.

**On incremental authorization.** The intended design was: try to grow the
existing hold (one hold, one statement line), fall back to a second hold only
where the card cannot. The capability has to be requested at PaymentIntent
creation, and Stripe documents `if_available` as the safe way to do that. It is
not safe here. On this account an off-session confirm carrying that option is
refused outright:

```
payment_intent_invalid_parameter
"This account is not eligible for the requested card features."
```

The intent lands in `requires_payment_method` holding nothing — so with
`PAYMENTS_ENFORCED` on, **every single post would have 402'd**. This shipped
unconditionally for about an hour of Phase 2b development and the smoke test is
the only thing that caught it, because no offline test can: the failure is a
property of the Stripe account, not of the code.

Hence the flag, and hence `ensureHoldCoversTask` skipping the increment attempt
entirely when it is off — an intent created without the capability can never be
incremented, so attempting it would be a guaranteed-to-fail API call sitting in
the latency of a requester's Approve tap.

## Evidence

**The refusal, reproduced directly against the Stripe test API** (test mode,
canned `pm_card_visa`, customer deleted afterwards):

```
POST /v1/payment_intents  amount=5925 currency=usd capture_method=manual
  customer=… payment_method=pm_card_visa confirm=true off_session=true
  payment_method_options[card][request_incremental_authorization]=if_available
→ 400 payment_intent_invalid_parameter
  "This account is not eligible for the requested card features."
  payment_intent.status = requires_payment_method
```

The same request **without** that option authorizes normally.

**The fallback path, end to end against the real API**
(`STRIPE_SMOKE=1 go test ./ -run Phase2bSmoke -v`, all passing):

```
TestPhase2bSmokeBudgetIncreaseGrowsTheHoldSomehow
  INCREMENTAL AUTHORIZATION RESULT: method="supplementary_payment"
    shortfall=$22.75 authorized_now=$82.00 err=""
TestPhase2bSmokeCaptureLessThanTheHoldReleasesTheRest
  captured $36.90 of a $59.25 hold; $22.35 released with no refund
```

A settlement above the original hold was then captured in full across the two
holds, and no hold was left standing afterwards.

**Invariant queries (CLAUDE.md Rule 3), re-run against production after the
three Phase 2b migrations were applied:**

```
q1 pg_policies (public)            → 3 rows, all "deny all (client)":
                                     audit_logs, notifications, users
q2 client DML grants               → 0 rows
q3 tables with RLS off             → 0 rows
q4 client-callable SECURITY DEFINER → 0 rows
extension_requests                 → relrowsecurity=true, relforcerowsecurity=false
```

**A pricing bug the new tests found, fixed in the same phase.**
`TestPhase2bPreAuthCoversCappedSettlement` sweeps every duration and both base-fee
tiers against the *ceiling* settlement (full consented time + budget + the $5
auto-approved tolerance) and failed on short shopping tasks:

```
delivery 15min budget=$15.00: hold $38.00 cannot cover a settlement of $39.50
delivery 16min budget=$15.00: hold $38.75 cannot cover a settlement of $40.00
```

`PreAuthBufferCents` was standing in for both the overage tolerance *and* the
auto-extend headroom — the same $5 counted twice. `preAuthAmountCents` now adds
`OverageToleranceCents` on top when there is a budget at all; the no-budget hold
is unchanged. Undercapture would have been up to $1.50 per such task, silent
except for one log line.

---
<!--
Naming: decisions/D-NN-short-slug.md, NN monotonically increasing.
This log is append-only.
-->
