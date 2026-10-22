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
- **Round 2 (35ee771) — FAIL.** MAJOR-1 and MAJOR-2 of round 1 closed;
  MAJOR-3/4 closed for the hold list but still open for the *armed*
  question: (1) `EXTENSION_RESOLVED` carried no request id, so a resolution
  of request B disarmed the asked question A (the person's YES then got
  "Got it" and A auto-declined) — and A's own late resolution hook disarmed
  B after the chain armed it; (2) `canArmApproval` included
  `awaiting_approval`, so a second live request was sent and armed OVER the
  first — a YES read against A would approve B. MINOR: send → RemoveHold →
  askApproval could leave a sent question unarmed with the hold gone; no
  mutual exclusion between resume points (two could send, one could orphan
  the other's arming); a YES sent before the question was asked (the
  double-tap on the booking's YES) could answer it; `ExtensionPending`
  lacked the blocked-pair filter; one poison hold blocked the rest;
  `RemoveHold` did not preserve order; the I-L exemption in the gate was
  not WhatsApp-only; `memConversations.RemoveHold` made phantom rows;
  `resumeDeferred` trusted the caller's `UserID` and `StatusTargetFor`'s
  identity for the window; tests missing for all of it.

  Fixed (all): `notify.CreateNotificationInput.ExtensionID` set on the
  requester's `EXTENSION_RESOLVED` (extensions.go) and carried on the
  event; `clearApproval(identity, user, ext)` clears only the armed
  question for THAT request, and with no id keeps it while the booker says
  it is still pending. `canArmApproval` excludes `awaiting_approval`: a
  request arriving while one is armed is held (one at a time), the same
  request again is a no-op. `ClaimHold` is one UPDATE — state, request,
  `approval_armed_at` (new column; staging ALTER applied) and hold removal
  together, conditional on an armable state and the hold being present —
  so concurrent resume points ask once; only after the claim is the
  question sent, and a failed send undoes the claim and puts the hold back.
  `onAwaitingApproval` refuses a YES/NO whose `ReceivedAt` precedes
  `ApprovalArmedAt` and re-asks. Booker errors skip the entry, not the
  list. `ExtensionPending` adds the `user_blocks` filter. `RemoveHold`/
  `ClaimHold` keep order (`WITH ORDINALITY`). The I-L exemption applies to
  WhatsApp only (P5). The WhatsApp window is the conversation's own
  `last_inbound_at` (now read with the row). Tests: second request while
  armed → held, B's resolution leaves A, no-id resolution with A pending
  leaves A, A's late resolution leaves B, B's clears; YES from before the
  question → re-ask; 8 concurrent `resumeDeferred` → one send; send
  failure → hold back, nothing armed; other user → dropped; WhatsApp
  window; PG `ClaimHold` (not from handoff, once, not while armed,
  `approval_armed_at`/`last_inbound_at` round-trip); DB `ExtensionPending`
  blocked pair; I-L on iMessage ignored.
- **Round 3 (6239b6e) — FAIL.** MAJOR: (1) `ClaimHold` armed the question
  BEFORE the send and removed the hold; a crash or a failed undo between
  the two left an armed, never-delivered question that the next YES (meant
  for the draft) would approve; (2) `NotifyStatus` still sent approval
  questions directly (check → send → `askApproval`), so two requests
  arriving together could both be sent with one armed — the YES read
  against the unarmed one approves the other. MINOR: `onAwaitingApproval`/
  `clearApproval` saved the full row (a claim landing in between was
  wiped); a held question was not resumed when the armed one ended in the
  app; the freshness guard failed open on a zero `ReceivedAt`; the undo
  re-added the hold at the end and the hold writes touched `updated_at`
  (the handoff's age); the end-of-message resume could put two questions
  in one burst; test gaps (emission of the resolved id, direct-path race,
  armed-ends-with-hold, PG draft survival/concurrency/state-list parity);
  `$2` cast in the subquery.

  Fixed (all): one arming path — every approval request is HELD, then
  `resumeDeferred`: `ClaimHold` (one UPDATE: state + request +
  `approval_armed_at = ArmedUnconfirmed`, hold KEPT, conditional on an
  armable state) → send → `ConfirmArmed` (real stamp, hold dropped, CAS on
  the request). Between claim and confirm nothing can answer it (every
  message precedes the sentinel; a YES then gets "a question is waiting"),
  and a claim older than `UnconfirmedResend` (3 min; routing's own timeout
  is 60 s) is re-sent from the hold by the next resume point — a repeat
  over a lost question. A failed send clears the claim on
  `context.WithoutCancel`; the hold was never removed, so its place is
  kept. `askApproval` deleted. `onAwaitingApproval` and `clearApproval`
  end the question with `ClearApproval` (CAS on request id). A resolution
  that clears the armed question tells the person, then resumes the next
  held one. A zero `ReceivedAt` is refused and re-asked; no margin is
  applied because the stamp is taken after the send returned (declared).
  Hold writes no longer touch `updated_at`; `updated_at` is read with the
  row for the claim's age. The end-of-message resume runs only when the
  reply left the state idle, unchanged, or in the approval itself — never
  on the heels of a new question of ours. `canArmApproval` and the SQL
  list come from one slice. The store refuses to re-hold the armed request
  (a repeated request event). Tests: end-to-end requester
  `EXTENSION_RESOLVED` carries the id (through the real approve handler
  and the chat hook) and `statusEvent` carries it; A's resolution asks
  held B at once (after the resolution text); two concurrent requests →
  one sent and armed, one held; unconfirmed claim → YES refused, re-ask,
  re-sent after 3 min and then answerable; zero send time refused; no
  burst after a new question of ours; PG: 8 concurrent claims → one win,
  draft survives claim/confirm/clear, confirm/clear CAS, SQL state list ==
  `canArmApproval` for every state.
- **Round 4 (6ad1734) — FAIL.** MAJOR: the unconfirmed-claim repair aged
  the claim from `updated_at`, which `TouchConversation` refreshes on
  every inbound message — so a person texting YES into a lost question
  was refused every time ("reply YES to approve", a loop) and the
  question was never re-sent. MINOR: an unconfirmed claim with no hold had
  no way out, and `RemoveHold` could drop the armed request's hold; the
  resend path skipped the liveness checks; a nil stamp in
  `awaiting_approval` failed open; the end-of-message resume still
  followed a re-ask of our own question, and the resolution path burst
  the resume line and the next question; a resolution landing between
  claim and confirm left a delivered question unarmed (a YES then lands on
  the state underneath); a stale full-row save could disarm a confirmed
  question; relay notes described an undelivered question; test gaps.

  Fixed (all): `approval_claimed_at` (new column; staging ALTER applied),
  written by `ClaimHold` only, ages the claim; `repairUnconfirmed` re-sends
  from the hold if the request is still live (same `holdLive` checks as
  the first send: own user, window by our clock, booker pending, text to
  ask) or clears the claim otherwise; `RemoveHold` never drops the armed
  request's hold; `HoldApproval` never re-holds it; `Unconfirmed()` treats
  a nil stamp in `awaiting_approval` as unconfirmed; the refused-YES reply
  is "Your supporter has a question for you — it's on its way, one moment"
  (never "reply YES"), and `pendingNote`/`stillWaiting` say the same while
  unconfirmed; the end-of-message resume runs only when the reply left the
  thread idle (or the approval itself); `onAwaitingApproval` omits the
  resume line when a held question follows; `ConfirmArmed` returns whether
  the claim was still there — if the request ended mid-send the person is
  told "I couldn't find that request anymore"; `SaveConversation`'s upsert
  keeps an armed question (state, request, stamps) unless the caller is
  ending it on purpose (handoff) — a stale snapshot can no longer disarm
  it, and only `ClearApproval` ends one. Tests: refused YES through
  `HandleInbound` with the row touched, then re-sent after 3 min and
  answerable; resolution mid-send → told, YES afterwards books/approves
  nothing; re-ask of our question not followed by the supporter's; PG:
  `RemoveHold`/`HoldApproval` on the armed request, stale full-row save
  keeps the question, `ClaimHold` stamps `approval_claimed_at`, the old
  round-trip test now ends the question through `ClearApproval`. Accepted
  residuals (declared): the undo after a failed send resumes to the
  current row's `ResumeState`; a re-send after 3 minutes may repeat a
  question whose first delivery succeeded but whose confirm write failed.
- **Round 5 (4f183de) — PASS.** No MAJOR. Required MINORs, all applied
  in the same commit: the upsert guard keeps all four approval fields
  whenever the existing row is in `awaiting_approval` and the caller is
  not handing off (a same-state save from a snapshot without stamps could
  wipe the claim time or restamp the sentinel over a confirm);
  `ReclaimUnconfirmed` (CAS on the claim time) so two repairs re-send
  once; "Gone" only when the request is no longer live (a handoff mid-send
  keeps the hold silently), with our own prompt appended; the undo after
  a failed send only on a definite refusal (4xx other than 408/409/429) —
  an uncertain failure leaves the claim unconfirmed for the repair;
  `CopyApprovalStillPending`/`CopyApprovalReask` say "on its way" while
  unconfirmed, and the copy adds "if it hasn't arrived in a few minutes,
  text me again" (the repair runs on the next message, not a timer); a
  repair that clears a dead claim answers the YES with "Gone" plus our
  prompt; the resume line after an answer is kept unless a LIVE hold
  follows. Tests: definite vs uncertain send failures; 8 concurrent
  repairs → one re-send; dead claim cleared with a reply and the draft
  not booked; PG: same-state save / touch / hold leave the claim time,
  reclaim CAS, handoff leaves and a re-claim follows, `ClearApproval`
  nulls the claim time. Accepted residuals (declared): the repair runs at
  the next inbound or status event, not on a timer; a double-tapped YES at
  the approval question lands its second YES on the re-shown draft
  summary and books it (pre-existing — `onConfirm` has no sent-before
  guard; a ruling is requested below).
- **Ruling requested (Arbiter):** should `onConfirm` refuse a YES whose
  provider send time precedes the moment the draft summary was shown
  (symmetry with the approval guard)? It closes the double-tap-after-an-
  approval case at the cost of a re-ask when a provider timestamp lags.

## Arbiter rulings — 2026-10-22 (Aura), to build as M3.2d

Recorded as given, in the order asked; nothing below is built yet.

- **R-1 `onConfirm` sent-before guard: YES, no margin.** A YES (or any
  booking confirmation) whose provider send time precedes the moment the
  draft summary was last shown is not a booking; the summary is re-asked.
  Symmetric with `approval_armed_at`. No tolerance: a double tap lands
  within about a second, and a margin would let it through; the accepted
  cost is a rare extra re-ask when a provider's timestamp runs behind our
  clock. A missing send time is refused, as for approvals.
- **R-2 A "no" with a reason, and "no thanks", decline.** "no, he needs to
  finish now", "no thanks", "nope, too much" decline the armed request.
  The punctuation after the no-word is required, so "no problem" / "no
  worries" are never a decline. I-P still applies: within 10 minutes of an
  unanswered supporter line only a bare strict no ("no", "n", 不要, 取消,
  cancel, stop) declines; "no, the red one" goes to the supporter.
- **R-3 The yes list widens; "yes + a message" is two actions.**
  - Accepted as a whole message (case-insensitive; trailing punctuation
    and emoji allowed; letters may repeat — "Yesss", "Kk", "Okie"):
    Yes, Yeah, Yep, Yup, Yea, Ya, Yessir, Hell yes, OK, Okay, K, Kk, Oki,
    Okie, Sure, Fine, Alright, All right, Why not, Of course, Absolutely,
    Definitely, For sure, 100%, Let's do it, Go ahead, Go for it, I'm down,
    I'm in, Sounds good, Sounds great, Works for me, Perfect, Great,
    Awesome, Amazing, Wonderful, 👍, ✅, 🙌, 👌 — plus the existing
    yep/book it/do it/好/好的/可以/確認/sí.
  - **Reaction words** (Great, Perfect, Awesome, Amazing, Wonderful, Fine,
    👍, 🙌, 👌) count as yes ONLY when our last outbound message in the
    thread IS the approval question. Anything sent after it (a status
    update, a relayed supporter line) → re-ask. Plain-consent words (Yes,
    OK, Sure, Of course, Absolutely, …) are not limited this way.
  - I-P unchanged: with an unanswered supporter line ≤10 minutes old, all
    of these except the strict set go to the supporter.
  - **Yes + message** ("yes but tell him to hurry", "ok, thanks Alex!"):
    approve AND relay the rest to the supporter — when the message starts
    with a yes word from the list and the rest has NO condition marker
    (`$`, a digit, max, min, only, if, unless, under, over, less, more
    than, up to, at most, 不超過, 只要, 如果, 最多). With a condition
    ("yes but max $5", "yes if it's organic"): nothing approved, nothing
    declined — the person's words go to the supporter, the request stays
    open, and the person is told it is still waiting (YES / NO).
- **R-4 Money and time stay rule-decided (S-11/5) — no conflict with R-3.**
  The Arbiter sets the rules; the backend executes them; a model never
  decides. Widening the list changes the rule, in code. A model may only
  make an outcome safer (relay, ask, escalate); when a model and the rule
  disagree, the safer side wins.
- **R-5 / R-6** (json_schema; one approval asked at a time) — recorded in
  D-18, which they belong to.

## M3.2d — R-1…R-3 built (2026-10-22)

- **R-1** `question_shown_at` (new column, only moves forward — the upsert
  takes GREATEST) is stamped when a draft summary is shown
  (`showSummary`), when an approval ends and the thread returns to our
  earlier question (`ClearApproval(…, shownAt)`), and on handoff release.
  `onConfirm` re-shows the summary instead of booking for a YES whose send
  time precedes it, or has none.
- **R-2/R-3** `approvalwords.go`: `approvalYes` (the ruled list, plain vs
  reaction words), `approvalNo` (whole-message no-words, or a no-word +
  punctuation + a reason), `yesWithMessage` (a yes-word prefix, a
  separator, the rest; `conditionRE` decides conditional). Used by
  `onAwaitingApproval` and by the I-P gate in `HandleInbound` when the
  state is `awaiting_approval`.
- Reaction words: `last_outbound_at` (stamped on every send by
  `TouchOutbound`, database clock) is captured into
  `approval_question_out_at` when the question is confirmed sent; a
  reaction word answers only while the two are equal.
- Yes + message: `approval_task_id` (set by `ClaimHold`) names the
  supporter; approve first, then relay the rest ("(Passed … to Sam.)");
  conditional → relay the whole message, nothing resolved, still-waiting
  line.

**Interpretations declared (Arbiter to confirm):**
- **I-S** R-2/R-3 apply to answers to the supporter's money/time requests
  only. The booking YES and our other questions keep yesRE/noRE: "Great"
  does not book; "no, make it 4pm" at a summary is still a correction,
  not a cancel.
- **I-T** `question_shown_at` also moves when an approval ends in any way
  (answered, resolved in the app, cleared): a YES sent before that moment
  was about the approval and never books.
- **I-U** (superseded in round 3 by I-AE) `conditionRE` lists what makes
  a tail conditional — amounts and digits, `?`, the ruled R-3 markers,
  conditional phrases, hedges, negations and contractions, deferrals,
  question and auxiliary words, complaints, number words and units,
  Spanish/Italian and Chinese markers (the code is the list). Since round
  3 it is one check among several: a tail approves only when it is ALSO
  on the harmless list (I-AE).
- **I-V** (rewritten, round 2) The outbound stamp is taken BEFORE every
  send, and a failure to take it FAILS the send (fail closed for the
  reaction-word rule). Consequence, accepted: on a database error a
  message that used to go out does not — a booking confirmation after the
  charge (the inbound is replayed and gets "Got it" instead of "Booked";
  no double booking: the claim and R-1 hold), the 3DS handoff notice, a
  status/receipt message (not retried; email and push still carry it), an
  ops message, the setup link. On first contact (no conversation row) the
  stamp is zero and the send proceeds; a zero question stamp is stored as
  NULL, so reaction words are refused.
- **I-W** Approve-then-relay: if the relay fails after the approval, the
  approval stands and the person is told the message could not be passed
  on.
- The agent test kit's `gate.go` (D-18) is updated to these rules when it
  moves into the repo.
- **I-X** (round 1) A yes + message relays the person's WHOLE message,
  as written, to the request's supporter ("Sam: yes but tell him to
  hurry"); the part after the yes is only used to decide.
- **I-Y** (round 1) Under I-P, a yes + message goes whole to the supporter
  of the LAST supporter line, which may not be the approval's task; the
  person is told whom it went to, and nothing moves.
- **I-Z** (round 1, extended round 2) R-1 compares the provider's send
  time with our clock. A provider clock running seconds behind ours would
  re-ask every booking YES for that person; each refusal is logged with
  the gap so it shows up. And our stamps are "the provider API returned",
  not "the phone received it": a YES sent after the API returned but
  before delivery (Sendblue answers "queued") passes. Both accepted under
  the no-margin ruling; revisit if the logs show it.
- **I-AA** (round 2) Stamp order is not delivery order: a message stamped
  just before the question but delivered after it leaves the question
  "last", so a reaction word about it could approve. The window is the
  gap between two sends to one thread (milliseconds; most likely a
  supporter chat line arriving with a request). Accepted; serialising
  sends per thread would close it.
- **I-AB** (round 2) The R-3 emoji rule: only listed friendly emoji
  (🙏 😊 🙂 😀 😄 😁 😃 😂 🤣 😎 💪 🎉 🥳 💯 ❤) and the answer emoji (👍 ✅
  🙌 👌) may follow a yes; any other emoji in a tail makes it conditional
  (relayed, request kept open). Repeated emoji count once ("👍👍").
- **I-AD** (round 3) Spelling variants accepted beyond the ruled list's
  exact forms: "lets do it", "im down", "im in" (no apostrophe) and "sì"
  (Italian grave accent beside "sí"). Arbiter may remove them.
- **I-AE** (round 3, amended round 4) A "yes + message" approves only
  when the WHOLE message after the yes is one of these closed shapes
  (after an optional but / and / also / just / oh; the request's
  supporter's first name stands in for NAME): "thanks", "thank you",
  "thx", "ty", "cheers", each with an optional NAME; "please"; "(please)
  hurry (up)"; "(please) tell him / her / them / NAME to hurry (up) /
  thanks / thank you"; "(please) let him / her / them / NAME know".
  Everything else — any other English however harmless it looks ("and
  grab napkins", "get it", "thanks anyway", "thanks Bob" when the
  supporter is Alex), any Chinese, Spanish or Italian — is passed to the
  supporter with the request kept open. The set is closed by construction;
  the cost is more relays of things that meant yes.
- **I-AF** (round 3) R-1's "summary shown" is an in-flight mark while the
  question is being sent (`ShownUnconfirmed`; no booking YES counts
  then) and the delivery time after; a draft from before R-1 (no stamp)
  is put again once instead of booked; a stale `awaiting_confirm` with no
  draft returns to idle. On WhatsApp, a reaction word or a yes + message
  swiped on any message is asked again (the reply may be about that
  message).
- **I-AG** (round 4) WhatsApp swipes: the armed question's own provider
  message id is kept (`approval_question_pid`, new forward-only
  migration). A swipe on the question itself answers it like any reply; a
  reaction word or approving yes + message swiped on ANOTHER message is
  asked again; a conditional message is passed on wherever it was swiped;
  a strict YES works anywhere. Negation scoping in a no-reason: a negation
  within two words before a yes-word negates it ("no, don't do it"
  declines); "don't worry" / "no worries" / "no problem" reassure ("no,
  don't worry, it's fine" is mixed). A concurrent handler's `markShown`
  can clear another question's in-flight mark (I-AA class, accepted).
- **I-AC** (round 2) The condition list cannot be complete; two
  structural backstops: a tail longer than 8 words is conditional, and
  the ambiguous heads y / ya / yea / si / k / kk / book / book it /
  confirm / do it / 是 never start a "yes + message" (as a whole message
  they are still a yes). Each backstop errs toward relaying. "no problem"
  / "no worries" alone are not in the ruled list and are relayed, not
  approved — Arbiter to add if wanted.

### M3.2d review log

- **Round 1 (3391e96) — FAIL.** MAJOR: (1) the R-1 stamp was taken before
  the summary was delivered, so a YES sent while it was on its way booked;
  (2) the failed-booking retry prompts ("…YES to retry") were not stamped;
  (3) the reaction rule captured "whatever was last sent at confirm time"
  as the question's stamp, the stamp was taken after the send, and a touch
  failure was fail-open; (4) `conditionRE` missed curly apostrophes
  ("don’t"), contractions, and most no-words as tails ("yeah nah", "ok
  nvm", "yes cancel"); (5) marker-free tails still approved: number words,
  fullwidth digits, deferrals ("let me think"), questions without "?",
  complaints ("that's too much"), Spanish/Italian, negative emoji;
  (6) "no! go ahead" / "no, it's fine" / "no, why?" declined; (7) the
  migration file was edited in place. MINOR: approve-then-relay double
  replies and silent drops; "yes no problem"/"ok great" read as
  conditional/relayed; "book it" missing; "kkkkk" and "ok-ish" approved;
  skin-tone 👍 and NFD "sí" unrecognised; I-P with a different task;
  clock skew unlogged; the upsert's else-branch kept a stale task id.
  Verifier: no RED (I-05 INCOMPLETE).

  Fixed (all): `MarkQuestionShown` after delivery at every site that puts
  our question (summary, retry prompts, resume lines after an answer, a
  cleared claim, a request ended mid-send, handoff release), plus the
  pre-send stamp; `TouchOutbound` runs BEFORE the provider call, returns
  its stamp, and its failure fails the send (fail closed); `sendStamped`
  hands the question's own stamp to `ConfirmArmed`; word rules rewritten —
  NFKC, straight quotes, skin tones/ZWJ dropped, one-letter runs not
  collapsed, hyphen not a separator, `conditionRE` a superset of the
  no-words plus contractions, nothing/none/non, numbers and number words,
  currency, deferrals, question words, complaints, Spanish/Italian,
  Chinese numerals, unknown emoji; yes-only tails ("no problem", "great")
  are a plain yes; `mixedNo` (a no whose reason affirms or asks) is passed
  on with the request kept open; approve-then-relay composes one reply
  (passed / couldn't pass / not passed because the request had ended);
  "book it"/"book" restored; the four columns moved to a new forward-only
  migration `20261022120000_channel_approval_columns.sql` and the old file
  restored; else-branch NULLs the task id; refusals log the time gap.
  Tests: every no-word (straight and curly) as a tail, the conditional
  table, edges, mixed no, YES while the summary is on its way, double tap
  after a declined card, reaction + message, relay failure, ended request,
  no task id, guards over the wider words, fail-closed send, PG: the
  question's own stamp kept across an intervening message, unknown
  address, forward-only mark, handoff and re-claim resets.
- **Round 2 (1914e50) — FAIL.** MAJOR: (1) `conditionRE` ran on
  letter-collapsed text, so its own doubled spellings could never match —
  the ruled "unless" and "less", "three dollars", "steep", "too"; `più`
  after `\b` was dead (RE2's `\b` is ASCII); (2) the unknown-bubble guard
  in `routeReply` still used yesRE, so the wider words resolved an
  approval swiped on a message we cannot identify; (3) the failed-booking
  retry prompt had no pre-send stamp — a YES processed while it was being
  sent retried the charge; (4) marker-free tails still approved
  (conditional phrases, hedges, auxiliary questions, more contractions,
  Chinese deferrals, es/it), ambiguous heads ("y tho", "si es barato")
  and laughter heads ("kkkkk, lol"). MINOR: I-V contradicted the code;
  stamp order vs delivery order; API-return vs delivery; `mixedNo` too
  broad on negated affirmatives; the emoji rule undeclared; plain yeses
  relayed ("yes thanks", "👍👍"); a nil draft could panic in the R-1
  re-show; a failed pass-on lacked the still-waiting line. Verifier: no
  RED (I-05 INCOMPLETE).

  Fixed (all): markers matched on the cleaned, uncollapsed tail (and the
  collapsed one); non-ASCII words outside `\b`; the ruled R-3 markers
  tested verbatim; `routeReply` uses the approval words in
  `awaiting_approval`; the retry prompts stamped before the send as well
  as after; condition classes added; I-AC backstops (length cap, weak
  heads; a head must be a real list word); `mixedNo` treats a negated
  reason as a decline; polite and repeated-emoji tails are a plain yes;
  `showSummary` guards a nil draft; a failed pass-on still says the
  question waits; I-V rewritten; I-Z extended; I-AA, I-AB, I-AC declared.
  Tests: ruled markers verbatim, the round-2 not-approved table, plain
  yeses that must keep working, negated reasons, unknown bubble × wider
  words, a YES processed during the retry prompt (shown to fail without
  the fix), failed pass-on.
- **Round 3 (b225b7b) — FAIL.** MAJOR: marker-free tails still approved
  — Chinese contrast and complaint (但是, 太貴, 有點高; and a Chinese tail
  counts as one "word", so the cap never fired), English deferrals ("I'll
  get back to you", "let you know", "consider", "decide"), Spanish/Italian
  conditions and complaints ("si es barato", "se costa poco", "es mucho",
  "è troppo"), unaccented "manana" / "piu". The reviewer asked for the
  default to be inverted. MINOR: a negation overrode "go ahead" ("no,
  don't worry, go ahead" declined); the no-reason was matched on
  collapsed text ("approve" → "aprove"); stretched markers ("lesss");
  the retry pre-stamp still left a window for a YES sent after it; the
  nil-draft guard left `awaiting_confirm`; WhatsApp swipes of a reaction
  on another bubble; undeclared spelling variants; I-U stale; no legacy
  note for unstamped drafts; zero-width characters. Verifier: no RED
  (I-05 INCOMPLETE).

  Fixed (all): the harmless-tail allowlist (I-AE) — default conditional;
  `reasonOf` reads the reason uncollapsed; a strong yes beside a negation
  is mixed; markers also matched with letter runs capped at two;
  `ShownUnconfirmed` in-flight mark at every pre-send stamp, replaced by
  the delivery time; an unstamped draft is put again; a draft-less
  confirm goes idle; WhatsApp swipes asked again; zero-width and soft
  hyphen stripped; I-U, I-AD, I-AE, I-AF declared. Tests: every input the
  reviewer listed, harmless instructions that still approve, mixed with
  negation, a YES after the stamp during the retry prompt (shown to fail
  without the fix), legacy and empty drafts, a WhatsApp swipe.
- **Round 4 (cffffbf) — FAIL.** MAJOR: (1) the allowlist checked only
  the first word, so "yes, get the cheapest", "sure lol", "ok thanks
  anyway", "yes, tell him to skip it" approved; (2) the WhatsApp swipe
  rule bounced a conditional message unrelayed and could not tell a swipe
  on the question itself from one elsewhere. MINOR: negation scoping in
  `mixedReason` both ways; over-conditional tails (accepted, I-AE); the
  draft-less confirm handled only behind the R-1 guard; no SQL test of
  the in-flight mark; the concurrent-clear case undeclared. Verifier: no
  RED (I-05 INCOMPLETE).

  Fixed (all): I-AE amended to closed whole shapes with the supporter's
  name; I-AG — the question's message id kept, swipes resolved against it,
  conditional swipes passed on; negation scoping and reassurance phrases;
  `onConfirm` sends a draft-less confirm to idle first; PG test of the
  in-flight mark (kept by GREATEST, replaced by `MarkQuestionShown`) and
  of the pid. Tests: the reviewer's approving inputs (now all passed on),
  the shapes that still approve, negated / reassuring reasons, swipes on
  the question / elsewhere / conditional / strict YES, draft-less confirm
  with a valid stamp, recovery after a resolution left the mark.
