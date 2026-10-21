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
    (name, budget, confirm, a held relay message); the YES/NO answers it;
    our question comes back afterwards. If the thread cannot take it
    (handoff, booking in flight, the card-setup page in progress), the
    question is **not sent** to the phone — the app has it.
    Asked-but-ignored is the worst outcome.

P5. **Reply context is a signal only where it is true.** WhatsApp inline
    replies decide (I-L, "the bubble decides"). iMessage replies carry no
    usable context (trigger 3) and are ignored; the footer on iMessage
    says "just write your reply", never "reply to this message".

P6. **Every default has a one-word way out.** A draft started while a
    supporter is active says so (once) and takes "for Maya" / "tell Maya" /
    "for my supporter" (regex; the name must be an active supporter's,
    unambiguous; never a pronoun) to relay the words instead — the draft
    is dropped only once the supporter has them. "new task:" / "another
    errand" forces a draft.

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
7. with ≥1 active task (the supporter's last line counts only while that
   task is still active and the person has not written since it):
   a. rule, **idle only**: the supporter asked a question (last relayed
      line ends with "?", within 10 min, unanswered) and the reply is short
      (≤ 12 words) → relay to that supporter;
   b. otherwise the **contextual classifier** (task titles, supporter first
      names, last relayed line within 2 h, which question of ours is open)
      → `to_supporter` | `new_task` | `correction` | `answer` | `greeting` |
      `question` | `other`. `to_supporter`/`greeting`/`question`/`other`
      relay; `new_task` drafts with the P6 lead-in (idle) or is the
      correction path (mid-question); `correction` re-drafts;
      `answer` (our question answered in words — "yes, approve it") is
      re-asked for a plain YES/NO, never relayed, never acted on. Error →
      `to_supporter`.
   c. which supporter: one named in the message, else the one who last
      wrote, else the only one; several and no signal → the numbered list
      (idle) or a line naming the way out ("for Maya …") mid-question,
      where the list cannot hold.
8. no active task — as before M3.2b.

In `awaiting_confirm` / `awaiting_budget` / `awaiting_approval` with an
active supporter, a non-money sentence goes through 7b as well:
`to_supporter` relays and our question stays open, re-stated in one line.

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
- I-P. With no reply context on iMessage, every affirmative the YES regex
  accepts ("ok", "sure", "yep", "👍", "好"…) answers the open question of
  ours; the footer names "YES, OK or NO" as examples. A yes meant for the
  supporter is written as a sentence ("yes please, the large one") and
  the classifier routes it. Casual affirmatives are the rule's law, not
  the footer's; this is accepted.
- I-Q. Within 10 minutes of an unanswered supporter question, a short
  message in idle is that answer, by rule, even if the person meant a new
  errand ("new task:" forces one). Mid-question the rule does not apply.

## Review log

**Round 1 (at `3705575`; Reviewer: Sonnet, isolated; Verifier: Haiku):** **FAIL** ×4 MAJOR, no BLOCKER. Money rule confirmed to hold on every new path. Fixed, no argument offered:
- MAJOR-1: a sentence during a pending approval was re-asked, never relayed, while the iMessage footer promised it would be. `onAwaitingApproval` now routes a sentence through 7b; the request stays open, re-stated in one line. Test.
- MAJOR-2: the supporter's last line targeted a task without checking it was active, and overrode the list with several supporters. The line now counts only for an active task; a supporter named in the message wins; several and no signal → list (idle) or "Which supporter is that for? Start with their name" (mid-question). Routing 7c rewritten above. Tests.
- MAJOR-3: "for Maya" dropped the draft before delivering and then claimed delivery. Now delivers first (`relaySent`), drops only on success, keeps the draft and says only that it failed otherwise. Test.
- MAJOR-4: the escape regex took `to`/`tell` + pronouns, so "For her birthday I need flowers…" was relayed and truncated. Now `for`/`tell` + an active supporter's first name (unambiguous) or "my supporter" only; P6 updated. Tests.
- Minors: setup excluded from P4 (text fixed); I-P declared (casual affirmatives); the 10-minute rule is idle-only and requires an unanswered question (I-Q); `correction` and `answer` labels so mid-question the model distinguishes a correction to our draft and an answer-in-words from words for the supporter; "no onions" no longer reads as "nothing to buy" (opt-out must be the whole message or a stated phrase); lead-in with several supporters names them; duplicate first names are ambiguous → list; bare "for Maya" with nothing pending asks what to say; plain-classifier failure in the unwired configuration goes to the supporter; weak tests rewritten (correction re-draft, approval over the name question actually armed).
- Accepted, recorded: "on the job" means any open assigned task, including one scheduled days ahead (P2 as written); the concurrency window of full-row saves (R3-7); `omitempty` on `BudgetAskIndex` for pre-M3.5 drafts (none shipped).
**Verifier (round 1, at `3705575`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
