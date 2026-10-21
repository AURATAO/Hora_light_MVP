# D-17 — Conversation model: who is the person talking to?

**Status:** built and reviewed to PASS 2026-10-21 (3 rounds) · Arbiter: Aura — I-M, I-N, I-Q agreed; I-O and I-P amended by ruling (M3.2c) · Tier: T3 (money-adjacent routing)
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
  **Arbiter 2026-10-21: agreed** ("money is only ever YES and NO").
- I-N. Sendblue `reply_to` is treated as absent (P5) on the evidence of
  two controlled trials; the plumbing stays (`reply_to_provider_id` is
  still stored) so it is one line to re-enable; the defect is reported to
  Sendblue with the two trials' handles. **Arbiter 2026-10-21: agreed.**
- I-O. **(Arbiter 2026-10-21: rewritten — defer, don't drop.)** When the
  thread cannot take an approval question (handoff, booking in flight, the
  card-setup page), the question is held and sent — and armed — as soon as
  the thread can take it, if the request is still pending; an expired
  request is not sent. A chat-only requester has no app, so "the app has
  it" was no fallback: only the email would have reached them, usually too
  late. (To build: M3.2c.)
- I-P. **(Arbiter 2026-10-21: version B.)** When a supporter has written
  within the last 10 minutes, only an explicit YES / NO (and their
  translations) answers the open question of ours; the casual affirmatives
  the regex otherwise accepts ("ok", "sure", "yep", "👍"…) go to the
  supporter. The person learns this where it matters: the footer on the
  relayed line says "to answer me, send YES or NO on its own — anything
  else (ok, sure, a sentence) goes to Sam", and a relayed "ok" is answered
  "(Passed 'ok' to Sam. Still waiting on your YES to book.)". With no
  supporter message in the last 10 minutes the regex is unchanged, so an
  ordinary booking is not made harder. Error direction: money never moves
  on an ambiguous word. (To build: M3.2c.)
- I-Q. Within 10 minutes of an unanswered supporter question, a short
  message in idle is that answer, by rule, even if the person meant a new
  errand ("new task:" forces one). Mid-question the rule does not apply.
  **Arbiter 2026-10-21: agreed.**

## Review log

**Round 1 (at `3705575`; Reviewer: Sonnet, isolated; Verifier: Haiku):** **FAIL** ×4 MAJOR, no BLOCKER. Money rule confirmed to hold on every new path. Fixed, no argument offered:
- MAJOR-1: a sentence during a pending approval was re-asked, never relayed, while the iMessage footer promised it would be. `onAwaitingApproval` now routes a sentence through 7b; the request stays open, re-stated in one line. Test.
- MAJOR-2: the supporter's last line targeted a task without checking it was active, and overrode the list with several supporters. The line now counts only for an active task; a supporter named in the message wins; several and no signal → list (idle) or "Which supporter is that for? Start with their name" (mid-question). Routing 7c rewritten above. Tests.
- MAJOR-3: "for Maya" dropped the draft before delivering and then claimed delivery. Now delivers first (`relaySent`), drops only on success, keeps the draft and says only that it failed otherwise. Test.
- MAJOR-4: the escape regex took `to`/`tell` + pronouns, so "For her birthday I need flowers…" was relayed and truncated. Now `for`/`tell` + an active supporter's first name (unambiguous) or "my supporter" only; P6 updated. Tests.
- Minors: setup excluded from P4 (text fixed); I-P declared (casual affirmatives); the 10-minute rule is idle-only and requires an unanswered question (I-Q); `correction` and `answer` labels so mid-question the model distinguishes a correction to our draft and an answer-in-words from words for the supporter; "no onions" no longer reads as "nothing to buy" (opt-out must be the whole message or a stated phrase); lead-in with several supporters names them; duplicate first names are ambiguous → list; bare "for Maya" with nothing pending asks what to say; plain-classifier failure in the unwired configuration goes to the supporter; weak tests rewritten (correction re-draft, approval over the name question actually armed).
- Accepted, recorded: "on the job" means any open assigned task, including one scheduled days ahead (P2 as written); the concurrency window of full-row saves (R3-7); `omitempty` on `BudgetAskIndex` for pre-M3.5 drafts (none shipped).
**Verifier (round 1, at `3705575`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Round 2 (at `8840823`):** **FAIL** ×5 MAJOR, no BLOCKER; round-1 MAJOR-4 closed, MAJOR-1 closed, MAJOR-2 partly, MAJOR-3's fix opened F4. Money rule confirmed again. Fixed, no argument offered:
- F1 (real, and it made 7a dead in production): the message being routed is already on record (`AttachMessage` runs first), so "answered" was always true. `LastRelayed` now excludes the current provider id; PG test with the current row present.
- F2: a bare "yes" in idle never reached the supporter who had just asked (it got "Got it"). `onIdle` now relays a bare YES/NO when a supporter question is open and unanswered; otherwise the acknowledgement. Test.
- F3: "the one who last wrote" was a target even after the person had answered. Now only until answered (routing 7c as written). Test.
- F4: "for Maya …" / "new task: …" while an approval was pending disarmed it. "for Maya" relays and keeps the approval armed (the draft underneath is dropped, the request is re-stated); "new task:" is answered "Please answer your supporter's request first". Test.
- F5: a worded reply to the "which task?" list went to the plain classifier ("when in doubt, new_task"), so a classifier failure drafted and dropped the hold. Now a supporter named in the reply delivers the hold to them; otherwise the context classifier decides and a failure joins the hold. Test.
- Minors: `answer` in `awaiting_confirm` re-asks ("Reply YES to book, NO to drop it, or tell me what to change") instead of re-drafting; `correction` is a valid label only for booking/budget/name questions, `answer` for any open question, and the prompt names the open question's kind; the lead-in and the mid-question line render the forms literally (“for Maya” or “for Sam”), never "for Maya / Sam"; a failed relay mid-question says only that it failed; supporter-name matching is case-sensitive ("I will be there" is not Will); stale header comments fixed.
- Accepted, recorded: `IsNoAutoExtend` runs before routing in `awaiting_confirm` (money-reducing direction); an amount in a sentence for the supporter during the budget question is taken as the budget (steps 4–5 precede 6 by design; the summary shows it before any YES); `new_task` mid-approval is re-asked and the words are not held; `awaiting_setup` / `awaiting_name` do not route to the supporter (no linked supporter can exist in `awaiting_name`; setup is the card page, minutes long); a supporter's line is shown to the classifier in quotes (it can only pick a label; no money path); a task title containing ": " can bleed into the "supporter's last line" shown to the model (cosmetic).
**Verifier (round 2, at `8840823`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Round 3 (at `e68b6db`; inputs now include `handler.go`):** **PASS.** F1–F5 traced closed (incl. the webhook's record-then-route order for F1); money rule holds on every path (`Booker.Post` only from a rule-matched YES in `onConfirm`/`FinishSetup`; `ResolveExtension` only after a whole-message YES/NO; `ClaimForPosting` needs a draft, so a dropped draft cannot be booked by a late YES). No BLOCKER/MAJOR. Minors handled in the same pass, no behaviour the Reviewer flagged as money-relevant:
- fixed: escapes match multi-line messages (`(?s)`); a spent supporter line is not shown to the model either; a `new_task` from a worded reply to the list drafts with the lead-in; "tell Sam <words>" relays the words and leaves an unrelated draft (and the approval) alone — only a bare "for Sam" hands over the draft's own words and drops it; a bare "yes" during the budget question is re-asked (only an amount answers it); a `correction` mid-budget re-drafts; the fake `LastRelayed` now mirrors the store's exclusion so forgetting the id fails a test (the F1 class); dead `supporterNames` removed.
- accepted, recorded: duplicate first names make the mid-question escape unusable (answer our question first; rare); a burst of messages recorded before any is routed may see a later one as "the answer" (benign: defaults to the supporter); P7 on a classifier outage can relay a worded "yes, approve it" to the supporter while the request stays pending — the person is told it is pending, and the supporter's human reading of it is the I-M class; the supporter's chat line now flows to the model provider — **privacy-policy list (S-20(3)): add the LLM provider as a processor of chat text** (PLAN §11).
**Verifier (round 3, at `e68b6db`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Status:** accepted by review; Arbiter (Aura) to confirm I-M..I-Q. Staging evidence owed: the bagel flow end to end (supporter asks → bare/short answer relayed; "for Sam"; approval during the budget question).

## M3.2c — the Arbiter's amendments, built (2026-10-21/22)

- **I-O, defer-don't-drop:** `NotifyStatus` holds an approval question the
  thread cannot take, as a structured record in
  `channel_conversations.deferred_approval` (a JSON array, oldest first;
  unshipped migration + staging ALTER). The hold is written only by two
  narrow, atomic store methods — `HoldApproval` (append-if-absent by
  request id) and `RemoveHold` (drop by request id) — and
  `SaveConversation` does not carry the column, so a booking's own
  full-row saves cannot wipe a question that arrived while it was in
  flight. `resumeDeferred` runs at every resume point — handoff release,
  after every booking outcome, after an approval is answered (for the next
  held one), and at the end of every inbound message when something is
  held — and asks the oldest question that is still the person's to
  answer, one at a time, recomposed from the record with the minutes that
  remain (`TimeoutMinutes − elapsed`; dropped if none). "Still theirs to
  answer" is decided at resume, not at hold time: the clock (lazy expiry —
  the row may read `pending` after the window), `Booker.ExtensionPending`
  (pending, same requester, task open, supporter still assigned), and the
  identity's user. A resolution event no longer clears holds by task — that
  would have dropped a second live request on the same task. The question
  is sent, then removed (at-least-once: a crash between repeats it, never
  loses it). On WhatsApp outside the 24h window it is kept for a later
  resume point. A booker error keeps the hold. Tests: two holds during a
  handoff → the first asked on release, the second after the answer;
  resolved in the app → dropped silently, the live one asked; timed out by
  the clock → dropped whatever the row says; minutes recomputed; booker
  error keeps the hold; held through the real claim-and-post path → follows
  the booking confirmation; PG round-trip (append, no-op repeat, remove,
  full-row save leaves it alone); DB `ExtensionPending` (fresh / stale /
  not the requester / resolved / supporter gone / task closed).
- **I-P version B:** `IsStrictYesNo` (yes/y/是/確認/sí/confirm, no/n/不要/取消/
  cancel/stop). In any awaiting state, a casual affirmative (ok, sure, 👍,
  book it…) with a supporter line in the last 10 minutes on an active task
  is relayed to that supporter and answered "(Passed “ok” to Sam.) Still
  waiting on your YES or NO — book “Laundry pickup” for $27." (per-state
  variants, card setup included). A casual word swiped on one of our own
  prompts is an answer to us (I-L wins). If the supporter-line lookup
  fails, the word is not treated as an answer (fail closed — S-11/5). If
  the relay itself fails, the person is told and the still-waiting line
  follows. Footers on relayed lines: "send YES or NO on its own. A plain
  ok/sure, or a sentence for Sam, goes to Sam." With no supporter line in
  the last 10 minutes the regex is unchanged. Tests: booking, approval, the
  11-minute boundary, prompt bubble, lookup failure, 確認.

### M3.2c review log

- **Round 1 (65fcfe5) — FAIL.** MAJOR ×4: the hold was a field in the
  full-row upsert, so `claimAndPost`/`afterPost` saves wiped a question
  that arrived while posting (the exact case I-O exists for);
  `ExtensionPending` ignored lazy expiry and task/supporter state; a
  single hold slot lost the second request; clearing by task on
  resolution dropped other live requests on that task. MINOR: hold writes
  not narrow/atomic; clear-then-send could lose a question; no resume
  point after `awaiting_setup`; `casualToSupporter` failed open on a
  lookup error and ignored the prompt bubble; 確認 missing from the strict
  set; `awaiting_setup` had no still-waiting copy; footer overstated
  ("anything else"); stored text went stale ("within 5 minutes" with
  `AtUnix` unused); WhatsApp window bypassed at resume; `d.UserID`
  unchecked; tests did not go through the real posting path. Verifier:
  only C-1/C-2 red. All fixed as described above (the hold is now a list
  of structured records written by `HoldApproval`/`RemoveHold`; minutes
  recomputed; generic resume point; send-then-remove; fail-closed gate).
  Verify also surfaced a gitleaks hit on main's placeholder Stripe key in
  `payments_phase3_test.go` (0bf9d39): a false positive, allowlisted by
  fingerprint in `.gitleaksignore` with a note.
