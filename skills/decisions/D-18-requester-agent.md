# D-18: Requester agent — the model proposes, the backend decides

**Date:** 2026-10-22
**Status:** Accepted (Arbiter: Aura) — built 2026-10-22 behind `CHANNEL_AGENT` (off); in review
**Trigger:** Aura's requester-agent draft (system prompt v0.1, 20 test
cases, a Go test runner), reviewed 2026-10-22 against D-16/D-17 and
S-11/5. The draft let the model call `approve_request` directly.

## Decision
An LLM "requester agent" may replace `llm.ClassifyInContext` as the
router for what a requester's message means, on one condition: it only
**proposes** one action per message, and the backend **decides**. Money and
time move only when the deterministic gate (D-17 rules, R-1…R-4) agrees;
the model can make an outcome safer, never riskier.

- **R-5 Output format: `json_schema`** (structured output), one object
  per message with an `action` enum (approve | decline | relay | ask |
  draft_task | escalate | reply) and its fields — the same request shape
  `llm` already uses, constrained decoding guarantees exactly one valid
  object. Tool calling is reserved for a later multi-step agent ("look
  up, then answer"); the action set maps one-to-one, so the gate does not
  change if that happens.
- **R-6 One approval asked at a time** (the M3.2c design stays). Numbered
  multi-question replies, if ever wanted, need the mapping stored by the
  server (like `PendingTaskChoices`), never inferred by a model from the
  history.
- Tool paths: approve/decline → `onAwaitingApproval` behind the gate;
  relay → `RelayToChat`; draft_task → the existing draft → quote → Terms
  → YES flow (the agent never books); escalate → `handOff`; ask → options
  stored server-side; reply → text only, never claiming an action.
- Context: only the confirmed armed request (never an unconfirmed claim),
  queued requests (not answerable), the last supporter line, stored
  numbered choices; every supporter-written text and task title marked
  as untrusted data.
- Behind a feature flag; off = today's rules. Model error or timeout →
  the safe default (ask or hand off, D-17 P7).

## Constitution impact
- Standards added: none yet (to be proposed with the build: "a model never
  authorises a money or time change").
- Standards modified/retired: none
- Invariants added/changed: none

## Context and alternatives
The draft's own rules were mostly right; the gap was that nothing enforced
them outside the model. Alternatives: model decides (rejected — S-11/5);
rules only (today — safe, but the classifier is weak on context); model
proposes + rule gate (chosen). Tool calling vs json_schema: equal on
safety; json_schema chosen for one code path and exactly-one output.

## Evidence
Agent test kit v0.2 (prompt, 71 cases, runner grading every call and
reporting gate-held vs gate-passed UNSAFE, `gate.go` = backend rules,
self-tests) — delivered 2026-10-22, to move into `tools/agenttest/`. A
deliberately bad fake model: 4 UNSAFE proposals, all held by the gate.
Baseline against the production model: owed (Aura runs it locally with
the key; report.json, no key).

## Built — 2026-10-22

**Where the agent sits.** It replaces only the model call the router
already made (`InContext.ClassifyInContext` in `routeInContextLabel` and
`classifyActive`), and only when `CHANNEL_AGENT=1`. Every rule step runs
first and unchanged: the approval words (`DecideApproval`), the booking
YES (`yesRE`, I-S), I-P, "for Maya", "new task:", the human-request rule,
the 10-minute short-reply rule. So the agent is asked only about messages
the rules have already declined to treat as an answer.

**Pieces.**
- `channel.DecideApproval(ApprovalFacts) ApprovalVerdict`: the approval
  switch from `onAwaitingApproval`, as a pure function (no behaviour
  change). The router acts on it; the runner's gate calls it.
- `llm.AgentPrompt` (v0.3), `AgentSchema`, `AgentUserMessage`,
  `DecodeProposal`, `(*OpenAICompat).Propose / ProposeRaw`.
- `Router.Agent` + `propose()`; `HandoffAgentEscalated`.
- `server/cmd/agenttest`: the runner on that production code; 80 cases.

**Interpretations declared (Arbiter may overrule):**
- **A-1 Location.** The runner is `server/cmd/agenttest`, not
  `tools/agenttest`: inside the `hora-auth` module it can import the
  router's own code, so the gate IS `DecideApproval` and the prompt,
  schema and context ARE production's. The planned regex-sync test is
  unnecessary — there is no copy to drift.
- **A-2 What a proposal does.** approve / decline → the plain YES/NO
  question again (the existing "answer" path); a model never resolves a
  request or books. correction → the existing re-draft (only with a
  booking / budget / name question open). draft_task → the existing draft
  → summary → YES flow. relay → for the supporter; the errand the agent
  names is used after a supporter named in the words and before "who
  wrote last". ask → the errand list (the last-writer guess is skipped).
  escalate (with a reason) → hand-off to ops. reply → for the supporter,
  as today's greeting / question labels are when a supporter is active.
- **A-3 No model text reaches the person.** v0.3 has no `text` fields: the
  agent picks an action; every word sent is the server's own copy. The
  v0.2 `reply(text)` and `ask_clarification(question, options)` are
  reduced to actions. Free text (with its claim-checking) is a later,
  separate decision.
- **A-4 What the agent sees.** Errand titles and supporter first names as
  t1, t2…; the open question's kind; the supporter's last line (task
  label, minutes ago, answered). Not shown: ids, phone numbers, the
  person's name, amounts of the open request, queued requests, numbered
  choices, card on file, message history. The schema's task enum is the
  listed labels plus "" — constrained decoding cannot name another;
  `DecodeProposal` drops anything else to "no target".
- **A-5 Making safe.** An unknown action or undecodable output → relay
  with no target (P7). approve/decline with no approval open, correction
  with no booking question, escalate without a listed reason → relay.
  Nothing a decode does can make an outcome riskier.
- **A-6 Escalation.** The model may hand off for safety, a dispute, legal
  or medical, a repeated misunderstanding, or a person asked for. Ops get
  `agent_escalated`; the log has the reason enum, never the words. Cost:
  a wrong escalation puts the thread with ops until released.
- **A-7 Logs.** `[channel] agent identity=… action=…` and the escalation
  reason only; never the message, the supporter's line or a title.
- **A-8 Case file v0.3.** The 71 v0.2 cases are converted to actions
  (same expectations; #41 no longer allows approve — R-6); 9 cases added
  (corrections, M3.2d rules, two-errand targeting, injection in the
  message). Each approval case states the rules' verdict (`server`), and
  `go test ./cmd/agenttest` checks it against `DecideApproval` — a rule
  change that alters an outcome fails the self-test until the case is
  updated on purpose.
- **A-9 I-P wording.** As ruled (D-17 I-P), any supporter line within 10
  minutes on an active task sends a casual word to the supporter,
  answered or not; the M3.2d rulings' paraphrase "an unanswered line" is
  looser than the code. The cases follow the code (#22).

**UX costs the cases show (Arbiter to decide; nothing unsafe):**
"yes that's fine" and "no worries, get it" are passed on, not approved
(I-AE closed shapes); "可以啊" is not "可以" (particles not stripped) and
goes to the agent, whose approve only re-asks.

**Evidence.** Offline: `go test ./...` green; a fake model that always
proposes "approve" scores 29 UNSAFE and exit 2, and no outcome changes:
every approval in its report is the rules' own (`rules:approve`), every
agent approve is a re-ask. Baseline against the real model: owed (Aura,
locally, with her key — `go run ./cmd/agenttest -runs 5 -json
report.json` from `server/`; send report.json, never the key).
