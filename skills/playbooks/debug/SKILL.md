---
name: debug
description: Diagnose and fix a defect under this project's standards. Use whenever behavior deviates from expectation — bug reports, failing tests, production incidents, or "this used to work." Produces a reproduction, a root-cause statement with evidence, a minimal fix, and a regression guard.
---

# Playbook: Debug

**Prerequisites:** you have read `constitution/STANDARDS.md` and `constitution/INVARIANTS.md` in this session. If not, stop and read them now — a fix that violates the constitution is a second defect, not a fix.

**Success criterion:** the defect is reproducible on demand, provably gone after the fix, and structurally prevented from silent recurrence.

## Procedure

### 1. Reproduce before you reason
Write the smallest executable reproduction (a failing test is the ideal form). **Until you can trigger the bug on demand, you have a rumor, not a defect.** If you cannot reproduce it, output that finding with what you tried — do not "fix" what you cannot observe.

### 2. State the fault hypothesis in falsifiable form
One sentence: "X happens because Y, therefore Z should also be observable." Then check Z. If Z is absent, the hypothesis is dead — form a new one. Loop until the hypothesis survives its own prediction.

Record dead hypotheses briefly; they are evidence for the reviewer that alternatives were eliminated, not just abandoned.

### 3. Fix minimally
- The fix addresses the root cause the surviving hypothesis names — not the symptom, not "while I'm here" refactors.
- Check the fix against every standard whose subject it touches. A fix that violates a standard is Blocked before it starts.
- If the correct fix *requires* violating a standard, stop and escalate via a decision record draft. Never resolve constitution conflicts unilaterally.

### 4. Guard against regression
The reproduction from step 1 becomes a permanent test. If the defect class is broader than one case (e.g., a whole category of unchecked inputs), propose a new invariant — see the "regression → invariant" rule in `constitution/INVARIANTS.md`.

### 5. Evidence bundle for review
Submit through `workflows/review-workflow.md` with:
- The reproduction (red before fix — show the failing output)
- The same reproduction green after fix
- Full verify entry point green
- Root-cause statement + dead hypotheses list

## Classification note
A bug fix touching auth, security controls, data migrations, or any invariant's subject matter is automatically **Tier 3** in the review workflow, no matter how small the diff.
