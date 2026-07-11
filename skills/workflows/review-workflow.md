# Authorization Review Workflow (Layer 3)

Multi-agent review protocol. Correctness is the only standard. Roles may be filled by humans, AI models, or a mix — the protocol is identical. One person/model may **never** fill two roles for the same change.

## Why the roles are information-isolated

The dominant failure mode of both junior reviewers and language models is *agreement contamination*: once you read the builder's persuasive explanation, you review the story instead of the change. This workflow prevents it structurally — the Adversarial Reviewer never sees the builder's narrative, and the Verifier never reads prose at all.

```
            ┌─────────────┐
            │   BUILDER    │  produces: diff + self-report
            └──────┬───────┘
          ┌────────┴──────────┐
          ▼                   ▼
 ┌─────────────────┐  ┌──────────────┐
 │ ADV. REVIEWER   │  │  VERIFIER    │
 │ sees: diff +    │  │ sees: nothing│
 │ constitution    │  │ runs: verify │
 │ ONLY            │  │ entry point  │
 └───────┬─────────┘  └──────┬───────┘
         ▼                   ▼
        ┌─────────────────────┐
        │       ARBITER       │  sees: all three outputs
        └─────────────────────┘
```

---

## Role 1 — Builder

**Input:** task description, `constitution/`, the relevant playbook.
**Output:**
1. The diff (or PR)
2. A self-report: what changed, which standards were touched, which standards required *interpretation* (ambiguity is declared, never hidden)
3. Evidence bundle: commands run and their raw output

**Builder rules:**
- Follow the matching playbook (`playbooks/`). If no playbook fits, say so in the self-report — that gap is itself a finding.
- Declare every assumption. An undeclared assumption discovered later is treated as a defect regardless of whether it was correct.

## Role 2 — Adversarial Reviewer

**Input:** the diff and the constitution. **Nothing else.** Do not read the builder's self-report, commit messages, or PR description before finishing the review. (Read them after, only to check for undeclared assumptions.)

**Output:** exactly one of:
- `PASS` — no standards violation found
- `FAIL: S-NN / I-NN` — cite the specific standard or invariant violated, with the specific location in the diff

**Reviewer rules:**
- You are a fault-finder, not an advisor. **Suggestions, style opinions, and "consider…" comments are forbidden output.** If it doesn't violate a cited standard, it passes.
- If the constitution is silent on something that feels wrong, output `PASS` + file a proposed-standard note to `decisions/`. The fix for a gap is amending the constitution, not freelancing a rejection.
- If you find yourself agreeing because the change "looks reasonable," re-read the diff hunks in reverse order. Reasonableness is not the standard; the constitution is.

**Prompt template (when the role is an AI model):**

```
You are the Adversarial Reviewer. Attached: (1) a diff, (2) STANDARDS.md, (3) INVARIANTS.md.
You have no other context, and you must not request or infer the author's intent.
Task: find violations of the attached standards in the diff. For each violation,
cite the standard ID and the exact diff location.
Output format: "PASS" or a list of "FAIL: <ID> at <file:line> — <one-line reason>".
Suggestions and style comments are forbidden. If you cannot cite a standard ID,
you may not fail the change.
```

## Role 3 — Verifier

**Input:** the branch checkout. No prose.
**Action:** run the verify entry point (see `constitution/INVARIANTS.md`) plus any playbook-mandated checks for this change type.
**Output:** raw results only — exit codes, failing test names, query outputs. No interpretation, no "this failure is probably unrelated." Unrelated-looking failures are the Arbiter's call.

## Role 4 — Arbiter

**Input:** all three outputs.
**Decision table (mechanical — no discretion in rows 1–3):**

| Reviewer | Verifier | Ruling |
|---|---|---|
| FAIL | any | **Blocked.** Return to Builder with the cited standard. |
| any | red | **Blocked.** Return to Builder with raw evidence. |
| PASS | green, but Builder declared an interpretation | **Conditionally approved:** merge + mandatory decision record documenting the interpretation. |
| PASS | green | **Approved.** |

**Arbiter rules:**
- The Arbiter may not overrule a red Verifier with reasoning. Evidence beats argument; if the check is wrong, fix the check first (via decision record), then re-run.
- Every Blocked or Conditionally-approved ruling gets one line appended to the decision log. Approved rulings need nothing.

---

## Cost-scaling: three tiers

Running four roles on a typo is waste. Classify each change before starting:

| Tier | Definition | Protocol |
|---|---|---|
| **T1 trivial** | No behavior change: comments, docs, formatting | Verifier only (verify entry point green) |
| **T2 standard** | Behavior change within existing patterns | Full workflow, roles may be AI |
| **T3 critical** | Touches security controls, auth, migrations, payments, or any INVARIANT's subject matter | Full workflow; Arbiter must be the project owner (human); Reviewer and Verifier must be different AI sessions/models than the Builder |

When in doubt between tiers, go up one. Misclassifying down is a defect; misclassifying up only costs minutes.

## Anti-patterns this workflow exists to kill

- **Narrative review:** approving because the explanation was convincing. (Isolation prevents it.)
- **Evidence-free confidence:** "should be fine." (Verifier requires raw output.)
- **Scope-creep rejection:** blocking on taste. (Reviewer must cite a standard ID.)
- **Silent interpretation:** builder quietly resolving ambiguity. (Declared-assumption rule.)
- **Self-review:** one agent playing multiple roles. (Role exclusivity rule.)
