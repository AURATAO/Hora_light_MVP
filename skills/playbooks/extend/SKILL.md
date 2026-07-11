---
name: extend
description: Add a new feature or capability under this project's standards. Use for any behavior-adding change — new endpoints, new screens, new integrations, new background jobs. Produces a scoped design note, an implementation consistent with settled architecture, and full review evidence.
---

# Playbook: Extend

**Prerequisites:** `constitution/STANDARDS.md` and `constitution/INVARIANTS.md` read in this session.

**Success criterion:** the new capability works, provably, and a future reader cannot tell it was written by a newcomer — it is indistinguishable in convention from the best existing code (and pointedly *not* like the code named in the legacy anti-patterns standard).

## Procedure

### 1. Scope before code (10 minutes, saves days)
Write a short design note answering:
- What is the smallest version that delivers the requested value?
- Which settled architecture decisions (STANDARDS section 1) does this touch? Work **within** them; relitigating settled decisions requires a decision record, not code.
- Which invariants' subject matter does this touch? (auth? schema? sensitive data?) → determines review tier now, not at PR time.
- What existing code is the pattern to imitate? Name a specific file. If the best example to imitate is legacy anti-pattern code, flag that gap in the design note.

For T2 changes the note is 5–10 lines. For T3 it goes to the owner before implementation begins.

### 2. Locate, don't invent
Search for existing helpers, patterns, and conventions before writing new ones. A duplicated abstraction is a standards violation in slow motion. Cite in your self-report which existing patterns you reused.

### 3. Implement inside the rails
- New tables/columns/routes follow the naming standards *exactly* — verify with the standard's own verification command before review, not during it.
- Every new data surface gets its security control (per the security baseline standards) **in the same commit** that creates it. "RLS/auth in a follow-up PR" is a forbidden phrase.
- Error handling follows the declared policy; no new error-handling styles.

### 4. Test the behavior, not the implementation
Tests assert what the feature promises users/callers. Implementation-detail tests that break on any refactor are cost, not evidence. Cover: the happy path, the permission-denied path, and the malformed-input path — the second one is the one newcomers and models most often skip.

### 5. Evidence bundle for review
Submit through `workflows/review-workflow.md` with:
- The design note from step 1
- Verify entry point green
- New tests green, with names readable as behavior statements
- Self-report declaring every point where the constitution was silent and you interpreted

## Anti-patterns
- Building the general version of a specific request ("while I'm at it, I made it configurable")
- Introducing a new library/pattern the constitution doesn't sanction, without a decision record
- Copying legacy code named in the anti-patterns standard because it was the nearest example
