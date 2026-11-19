# Operational runbook — a supporter leaves an accepted task

> Companion to `payments-runbook.md`. Rules set by Aura on 2026-11-18.
> The supporter app has no button that releases a task, on purpose: every drop
> goes through a person at HO:RA, so we always know why it happened. What it
> has (from build 21) is **I can't do this task**: a form that tells us, and
> changes nothing on the task.

## The three rules

1. **Valid reason → we make it right after the fact.** Unsafe situation,
   emergency, the customer unreachable or the task not as described. The
   system pays nothing automatically; ops decide what is fair from the facts
   and pay it by hand (see "Paying a supporter by hand").
2. **No valid reason → the logged time is not paid.** The time is voided and
   the task goes to another supporter, who starts from zero. The customer pays
   for one supporter's time, once.
3. **Money already spent on the customer's items → we refund the supporter
   against the receipt**, whatever the reason for leaving. Get the receipt
   photo from the supporter and keep it.

Feeling unsafe is always a valid reason. Do not ask a supporter to justify it.

## How we hear about it

- The supporter taps **I can't do this task** on the task. Ops get an email
  with the reason and whether anything was bought, and the task shows a red
  flag in Ops → Task feed. Nothing else has happened: they are still assigned,
  their timer is still running, the customer has not been told.
- The supporter messages HO:RA (WhatsApp from Profile, or the task chat).
- The customer says nobody came, or their supporter stopped answering.
- The time-cap alert email (`payments-runbook.md`) for a task with no activity.

First reply to the supporter, always: **ask whether they bought anything and
where the items are now.** That decides which row below applies. (The form
asks the first half; you still need to ask where the items are.)

## What to do

All actions are in Ops → Task feed.

| Situation | Action | Customer pays | Supporter who left gets |
|---|---|---|---|
| Accepted, not clocked in | **Reassign** → pick the new supporter | New supporter's time only | Nothing (nothing was logged) |
| Clocked in, nothing bought, someone else can take it | **Reassign** → fill in "Why" → **Void time & hand over** | New supporter's time only | Nothing from the system; rule 1 if the reason was valid |
| Clocked in, nobody else can take it | **Cancel** → "The supporter left or can't finish" | Nothing; the card hold is released | Nothing from the system; rule 1 if the reason was valid |
| Items already bought | See "Items already bought" first, then one of the rows above | — | Refund of the receipt (rule 3) |

"Void time & hand over" needs `feat/reassign-after-start` to be live. Until it
is, a clocked-in task can only be cancelled (third row).

What the app shows after a hand-over:

- **The supporter who left:** the task disappears from My jobs and they can no
  longer open it. They get one notification: taken off the task, and asked to
  message HO:RA because how the logged time is handled depends on the
  situation. The message promises nothing and refuses nothing; the decision
  is ours, under the three rules above.
- **The new supporter:** the task appears in My jobs as Accepted, with the
  clock at zero, and they are added to the task chat. They did not go through
  the accept screen, so **message them first and get a yes** — do not assign a
  task to someone who has not agreed to it.
- **The customer:** one message — their supporter couldn't finish, the named
  new supporter is taking over, and they only pay for the new supporter's time.

What the app shows after a cancel for this reason:

- **The customer:** their supporter couldn't finish, nothing was charged, and
  they can ask us to set it up again.
- **The supporter:** taken off the task, and asked to message HO:RA.

## The customer wants out after a hand-over

The 3-minute free-cancel window restarts when the new supporter is assigned.
If the customer cancels **themselves** after that, the system charges the base
fee plus the new supporter's time and pays the new supporter, exactly as for
any accepted task. That is correct for the supporter and wrong for the
customer: they are paying because our first supporter left.

Until this is automatic:

1. If the customer says they no longer want it, ask them **not** to cancel
   themselves. Use ops **Cancel** (nothing is charged).
2. Pay the new supporter the base fee by hand if they had already set off
   (see "Paying a supporter by hand"). HO:RA absorbs it.
3. If the customer already cancelled and was charged, refund them in Stripe.

Who absorbs this cost as a standing rule is still to be decided (PLAN 13e).

## Items already bought

The system cannot reimburse a receipt on a task that is not completed, and a
hand-over does not carry the first supporter's purchase. So this part is manual.

1. Ask the supporter for the **receipt photo** and where the items are.
2. Decide where the items go: to the customer directly, or handed to the new
   supporter. Tell both people in the task chat.
3. Refund the supporter the receipt amount by hand (next section).
4. If the new supporter completes the task **without** buying again, they
   submit no receipt, so the customer is charged for time only. The customer
   still owes for the items: tell them the amount, and collect it separately.
   Until that is built into the system, record it in the tracking sheet and
   raise it with Aura before promising the customer anything.
5. If the items are lost or unusable, the customer does not pay for them. That
   cost is ours; record it.

## Paying a supporter by hand

Used for rule 1 (fair pay for a valid reason) and rule 3 (receipt refund).

- Paid from the company bank account, by whoever holds that access.
- Before paying, write one line in the tracking sheet: date, task id,
  supporter, amount, which rule, the reason, and the receipt photo link for
  rule 3. A payment with no line is a payment nobody can explain later.
- A fair starting point for rule 1 is what the task would have paid for the
  time logged (the audit log keeps the voided minutes: Ops can read them from
  the reassign toast, and Aura can read the full sessions from `audit_logs`,
  action `TASK_REASSIGNED`, reason `swap_voided`).
- Tell the supporter the amount and when to expect it.

## What not to do

- **Do not use Force** on a dropped task. It charges the customer for the time
  logged and pays the supporter who left.
- **Do not use Remove.** It tells the customer their task broke our rules.
- **Do not use Cancel when the customer asked to cancel** and the supporter
  has been on the task more than 3 minutes. Ops Cancel pays the supporter
  nothing. Ask the customer to cancel themselves (by text or in the app): that
  path charges the base fee plus time and pays the supporter.
- Do not promise a supporter pay, or a customer a refund, before the line is
  in the tracking sheet.

## Keep count

Every drop gets a line in the tracking sheet even when no money moves: date,
supporter, task, reason, valid or not. Two drops without a valid reason is a
conversation with the supporter; what happens after that is Aura's decision.
