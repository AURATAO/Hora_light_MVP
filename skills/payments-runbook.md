# Operational runbook — payments

> Companion to `supporter-review-runbook.md`. This one covers money: what the
> system does on its own, what it deliberately does NOT do, and the handful of
> things that land on a human.
>
> Scope as of Stripe Phase 2b (2026-09-14). Everything here assumes
> **test mode** and `PAYMENTS_ENFORCED` off unless stated — with the flag off,
> no task has a hold and none of the alerts below can fire.

## The one rule everything else follows from

**A money problem is never a supporter's problem.** Nothing in the payment path
can block a task from being completed, and nothing cancels a task from a timer.
When money goes wrong the task still closes, the row is marked, and ops get an
email. That is a deliberate trade: settling a payment by hand is cheap,
stranding somebody in a stranger's kitchen is not.

The consequence is that **the ops inbox is load-bearing**. If nobody reads these
emails, the failure mode is silent uncollected revenue.

---

## What arrives in the ops inbox

All four are sent to the `ADMIN_EMAILS` allowlist (default: the addresses in
`defaultOpsAdmins`, `server/main.go`).

### 1. `[HO:RA] Capture failed — $X uncollected`

**Means:** a task was completed and the charge did not go through. The
authorization had landed; the capture did not — an expired hold (card networks
release an uncaptured manual-capture authorization after about 7 days), Stripe
unreachable, or the intent moved underneath us.

**State:** task `completed`. `payments.status = 'capture_failed'`. An
`audit_logs` row with `action='PAYMENT_CAPTURE_FAILED'` carrying
`owed_cents`, `time_cost_cents`, `receipt_cents` and the error.
`tasks.settled_total_cents` is **NULL** — nothing was recorded as settled,
because nothing was.

**Do:** open the Stripe dashboard → Payments, search the task id in metadata.

- If the intent is still capturable, capture it for the `owed_cents` in the
  audit row.
- If the authorization has lapsed, the money cannot be taken from that intent
  at all. Charge the saved card separately, or write it off. **Say which in a
  note on the task** — an uncollected task that nobody recorded a decision on
  looks identical to one nobody noticed.

**Do not** re-run the completion or edit the worklogs to "make it settle". The
task is finished; the ledger row is the thing that is wrong.

### 2. `[HO:RA] Unresponsive requester — N min past the time cap`

**Means:** a supporter has worked past the time the requester agreed to pay for,
the requester was notified twice (time-cap warning, then cap reached), and
`BillingConfig.GracePeriodMinutes` (30 min) elapsed with no approval and no
pending request.

**State:** task still `open` and still completable. Time past the cap is **not**
accruing — the supporter is working unpaid. `audit_logs` has
`action='TIME_CAP_UNRESPONSIVE'` with `over_by_minutes` and both emails.

**Do:** phone both sides. The supporter may want to be told it is fine to stop;
the requester may simply not have their phone. If work past the cap should be
paid for, the requester approves a time extension in the app — that raises the
ceiling and re-arms the warnings, and the settlement follows automatically.
There is no ops action that raises a ceiling, deliberately: only the person
paying can agree to pay more.

**Do not** cancel the task. Nothing in the system does, at any point.

### 3. `[HO:RA] Payment disputed — $X (reason)`

Unchanged from Phase 1. A dispute has a response deadline in days and loses by
default if missed. `audit_logs` `action='PAYMENT_DISPUTED'`; respond in the
Stripe dashboard → Payments → Disputes. A dispute that could not be traced to a
task is filed against the nil UUID — those are *more* urgent, not less.

### 4. Log-only: `[payments][EXPIRING HOLD]` and `[payments][STRANDED HOLD]`

Not emails — they appear in the Render logs, from the 6-hourly watcher.

- **EXPIRING HOLD**: an `open` task carrying an `authorized` hold more than 6
  days old. Capture will fail once it lapses (see §1). A HO:RA task still open
  at six days is already anomalous — chase it before a supporter works it.
- **STRANDED HOLD**: a release that Stripe refused. The hold expires on its own
  within a week; release it by hand if the requester complains that funds are
  held. Also written to `audit_logs` as `PAYMENT_RELEASE_FAILED`.

---

## Refunds are manual, and adjust-time refuses after a capture

**There is no refund path in this codebase.** Not a gap to be filled in passing —
the happy path never needs one, because a manual-capture hold captured for less
than it held releases the remainder by itself. Refunds exist only for
after-the-fact corrections, and during beta those are a Stripe-dashboard
operation.

The place this surfaces is the ops panel's **Adjust time**:

| When | What happens |
|---|---|
| Before the capture (task `open`, or `completed` with payments off) | The adjustment applies and every settlement figure re-derives from the worklogs. Nothing is stored, so nothing has to be corrected. |
| After the capture | **409 `already_captured`**, naming the capture date and the settled amount. |

That 409 is the correct answer, not an obstacle: re-pricing a settled task would
produce a number that disagrees with what the card was actually charged, and
correcting a capture *is* a refund.

**To fix a settled task's billing by hand:**

1. Stripe dashboard → Payments → search the task id in metadata.
2. Refund the difference (partial refunds are supported), or capture more if the
   hold is somehow still open — it will not be, a captured intent is final.
3. Write an `audit_logs` note recording what was refunded and why. The task's
   `settled_total_cents` is **not** updated by a dashboard refund, so the audit
   row is the only record that the two disagree.

Automating this is a Phase 3 concern. It needs a requester-facing story ("you
were refunded $4.50 because…"), an idempotency story, and a decision about who
may trigger it — none of which belong in the phase that first captures money at
all.

---

## What the requester is told, and when

Worth knowing before answering a support message, because the answer is usually
"it already happened and their bank hasn't caught up".

| Moment | What they see |
|---|---|
| post succeeds | the amount reserved and the card it is on |
| task open | "On hold — $X reserved · Visa ••4242", and that it is not a charge |
| cancelling | the amount that will be released, before they confirm |
| cancelled | what was charged, what was released, and that a statement can lag **1–7 days** |
| completed | the itemized settlement (§"What a settlement is made of") |

**"The money is still showing on my statement."** Almost always this. Releasing
an authorization is instant on Stripe's side and invisible on the cardholder's
until their bank posts the reversal, which is on the bank's schedule. Check the
payments row: `canceled` means it is done and there is nothing to chase. A
capture that took *less* than the hold behaves the same way — the remainder is
freed with no refund and no event.

**A hold with no card named** ("reserved on your card", no brand) is a hold
placed before `payments.card_brand` existed, or one where Stripe returned no
charge detail. Harmless and display-only; the amount is still correct.

**The supporter must never be shown any of this.** The hold, the amount and the
card are attached to the task payload for the requester alone. If a supporter
ever reports seeing a reserved amount, that is a security bug, not a copy bug —
escalate it rather than editing the wording.

## What a settlement is made of

Both parties see the same breakdown on the task screen, from
`GET /tasks/:id/worklogs`. If somebody queries a charge, this is the whole of it:

```
base fee            $12.00, or $25.00 for companionship — covers the first 15 min
time                billable minutes x $0.50
                    billable = max(billed_minutes - 15, 0)
                    billed_minutes = min(logged_minutes, the consented ceiling)
receipt             the verified receipt, up to approved budget + $5
total               the sum, clamped again to what was actually authorized
```

**The consented ceiling** is the requester's estimate, plus 15 minutes if they
ticked auto-extend consent at post, plus every minute of every time extension
they approved. Time past it is worked but not billed. A settlement where
`logged_minutes > billed_minutes` is not a bug — it is a supporter who ran over
without an approved extension, and the screen says so in those words to both of
them.

**Supplementary holds.** A task can carry two Stripe PaymentIntents: the
original `task_payment` and a `budget_increase` opened when an approved budget
increase outgrew the original hold. That is expected on this account —
incremental authorization is not available on it (see D-11) — and settlement
captures the main hold first, then the supplementary ones, then releases
whatever is left. In the dashboard they appear as two charges on one task; both
carry the task id in metadata.

---

## Flags, and what turning each one on does

| Variable | Off (default) | On |
|---|---|---|
| `PAYMENTS_ENFORCED` | Posting needs no card, no hold, no `payments` row. **This is the running beta.** | Posting requires a saved card and places a pre-auth; a failed hold means no task (402). |
| `STRIPE_INCREMENTAL_AUTH` | An approved budget increase that outgrows its hold opens a second hold. | The pre-auth asks Stripe for incremental-authorization support and an approved increase grows the existing hold. |

**`STRIPE_INCREMENTAL_AUTH` must not be turned on until Stripe has enabled
flexible payments on the account.** Requesting the capability on an ineligible
account does not degrade gracefully — the off-session confirm fails with
`payment_intent_invalid_parameter` ("This account is not eligible for the
requested card features") and **every post 402s**. Verified in test mode
2026-09-14. Before flipping it: enable it in the dashboard, then re-run
`STRIPE_SMOKE=1 go test ./ -run Phase2bSmoke -v` and check the logged
`INCREMENTAL AUTHORIZATION RESULT` line says `method="incremental_authorization"`.
