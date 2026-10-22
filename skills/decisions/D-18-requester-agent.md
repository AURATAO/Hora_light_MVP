# D-18: Requester agent — the model proposes, the backend decides

**Date:** 2026-10-22
**Status:** Accepted (Arbiter: Aura) — design only; not built
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
