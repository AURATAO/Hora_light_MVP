---
name: review
description: Operate one of the review roles (Adversarial Reviewer, Verifier, or Arbiter) for a proposed change. Use whenever asked to review, audit, approve, or check someone else's (or a previous session's) work. Enforces information isolation and evidence-only rulings.
---

# Playbook: Review

**First action: determine which role you are filling, and confirm you were not the Builder of this change.** If you built it (or a previous session of you in this same conversation built it), you may not review it — say so and stop. Role definitions live in `workflows/review-workflow.md`; this playbook is the operating manual.

## If you are the Adversarial Reviewer

**Load only:** the diff, `constitution/STANDARDS.md`, `constitution/INVARIANTS.md`. If the builder's description/self-report is in your context already, acknowledge the contamination in your output — the Arbiter needs to know your independence was compromised.

**Method:**
1. Read the constitution first, diff second. (Reading the diff first anchors you on the author's framing.)
2. For each diff hunk, ask one question: *which standards govern this hunk, and does it comply?* Not "is this good code."
3. For each touched data surface / endpoint / table, explicitly check the security-baseline standards even if the diff looks unrelated — omissions don't show up in diffs, so check what *should* be there and isn't.
4. Check the diff against the named legacy anti-patterns (STANDARDS §4) — newcomer code copies them.

**Output discipline:** `PASS`, or `FAIL: <standard ID> at <file:line> — <one line>`. No suggestions. No praise. No "consider." If something feels wrong but no standard covers it: `PASS` + a proposed-standard note for `decisions/`. You find faults against a written constitution; you do not have taste.

## If you are the Verifier

**Load nothing textual.** Check out the branch, run the verify entry point (see `constitution/INVARIANTS.md`) plus any checks the relevant playbook mandates for this change type (e.g., migration dry-run capture for `migrate` changes).

**Output discipline:** raw results — exit codes, failing test names, command output. Forbidden phrases: "probably unrelated," "flaky," "should be fine," "pre-existing failure." If you suspect a failure is pre-existing, *prove it*: run the same check on the base branch and report both raw results side by side. Evidence, not adjectives.

## If you are the Arbiter

**Load:** all three outputs (builder self-report, reviewer verdict, verifier results).

**Method:** apply the decision table in `workflows/review-workflow.md` mechanically. Your judgment applies only in the conditional-approval row (declared interpretations) and in tier classification disputes. You may not:
- Overrule a red Verifier with reasoning (fix the check via decision record first, then re-run)
- Accept a Reviewer FAIL being "argued down" by the Builder in prose — the Builder's recourse is changing the code or proposing a constitution amendment
- Approve anything whose tier was classified below what the change actually touches

**Output:** the ruling + (for Blocked / Conditionally approved) one line appended to the decision log.

## Reviewing AI-built changes specifically

Two adjustments when the Builder was a model:
1. Weight the omission check (Reviewer method step 3) heaviest — models complete what was asked and silently skip adjacent obligations (the permission-denied test, the RLS policy on the new table).
2. Distrust fluent self-reports proportionally to their fluency. A confident, well-structured self-report is not evidence; only the evidence bundle is evidence.
