# D-17 — Conversation model: who is the person talking to?

**Status:** proposed 2026-10-21 (Builder) · Arbiter: Aura · Tier: T3 (money-adjacent routing)
**Supersedes:** the "a new errand still wins" rule in D-16 / M3.2 (relay.go header), and I-L's iMessage half.

## Trigger (staging, 2026-10-21 evening, Aura's tests)

1. Supporter asked in chat "what kind of bagel you want?"; the requester
   answered "I want everything 🥯" on iMessage → classified `new_task` →
   a second errand was drafted and the budget question asked. The
   supporter never saw the answer.
2. While the thread was stuck on that budget question, the supporter
   requested +$10 from the app. The question reached the phone but was
   **not armed** (`awaiting_budget` is not an arming state); the requester's
   "no" was eaten by the budget question; the request stayed pending; the
   supporter got nothing.
3. Inline replies on iMessage: Sendblue's `reply_to.message_handle` pointed
   at **our most recent outbound message every time**, regardless of which
   bubble was long-pressed (two controlled trials, Test A/B, 19:21 UTC).
   The field exists and is wrong. WhatsApp's `context.id` is reliable.

## Principles (the model)

P1. **One thread, three voices.** Us (HO:RA), the supporter (relayed,
    always prefixed "Maya · Task:"), the person. Every question we ask
    says what answers it; every relayed line says who said it.

P2. **Context sets the default.** With a supporter on the job, the person's
    words are for the supporter unless they are clearly something else.
    Before M3.2b the default was the opposite ("new errand wins"); the cost
    of each mistake is asymmetric — a wrongly relayed errand gets "that's a
    new order" from a human; a wrongly drafted reply hides the answer from
    the supporter and parks the thread on a price question.

P3. **Money answers are rule-matched and state-bound.** A bare YES / OK /
    NO (regex) answers the open question of ours — booking, approval,
    budget ("nothing") — and nothing else. The model never decides money.
    Unchanged from S-11/5.

P4. **The supporter's request interrupts.** An approval question (time or
    budget, the clock is running) is armed over any of our own questions
    (name, budget, setup, confirm); the YES/NO answers it; our question
    comes back afterwards. If the thread cannot take it (handoff, booking
    in flight), the question is **not sent** to the phone — the app has
    it. Asked-but-ignored is the worst outcome.

P5. **Reply context is a signal only where it is true.** WhatsApp inline
    replies decide (I-L, "the bubble decides"). iMessage replies carry no
    usable context (trigger 3) and are ignored; the footer on iMessage
    says "just write your reply", never "reply to this message".

P6. **Every default has a one-word way out.** A draft started while a
    supporter is active says so and takes "for Maya" / "for my supporter"
    (regex) to relay the words instead and drop the draft. "new task:" /
    "another errand" forces a draft.

P7. **Fail toward the human.** A classifier failure with a supporter on
    the job routes to the supporter, never to a draft.

## Routing (per inbound message, in this order)

1. handoff / posting states — unchanged.
2. human-request regex — unchanged.
3. inline reply (WhatsApp only) — bubble decides (routeReply).
4. open question of ours + bare YES/OK/NO — the state handler (money).
5. `awaiting_budget` + amount / "nothing" — the budget handler.
6. escape phrases (P6) — "for Maya" → relay the draft's source, drop it;
   "new task:" → draft.
7. with ≥1 active task:
   a. rule: the supporter asked a question (last relayed line ends with
      "?", within 10 min) and the reply is short (≤ 12 words) → relay;
   b. otherwise the **contextual classifier** (task titles, supporter first
      names, last relayed line, which question of ours is open) →
      `to_supporter` | `new_task` | `greeting` | `question` | `other`;
      everything but `new_task` relays; `new_task` drafts with the P6
      lead-in. Error → `to_supporter`.
   c. several active tasks → the numbered list, as before.
8. no active task — as before M3.2b.

In `awaiting_confirm` / `awaiting_budget` with an active supporter, a
non-money sentence goes through 7 as well: `to_supporter` relays and the
draft stays pending (re-shown in one line); `new_task`/`other` is the
correction / re-ask path as before.

## What this does not change

Money rule (P3); the hold/list mechanics; the approval question's
structured composition (M3.2 R6); authorization in both directions.

## Interpretations declared (Arbiter)

- I-M. "Default to the supporter" (P2) is a product rule, not a safety
  rule; the safety rule is P3. A relayed sentence never moves money.
- I-N. Sendblue `reply_to` is treated as absent (P5) on the evidence of
  two controlled trials; revisit only with new evidence from Sendblue.
- I-O. An approval question is not sent to the phone when it cannot be
  armed (P4) — the app notification and email still go out.

## Review log

(see below as rounds complete)
