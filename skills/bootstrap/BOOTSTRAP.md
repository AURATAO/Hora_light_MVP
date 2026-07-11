---
name: bootstrap
description: Instantiate this generic skill library for a specific project. Use this whenever adopting the library for a new codebase, when constitution/ still contains {PLACEHOLDER} tokens, or when a project pivot invalidates the existing constitution. Produces a filled-in constitution/ and wired verification commands.
---

# Bootstrap: Instantiating the Library for a Project

> **Status for this copy: ALREADY INSTANTIATED for HO:RA.** The constitution/ is filled.
> Re-run this playbook only for a new project or after a pivot. Remaining `TODO(confirm)`
> items are tracked in decisions/D-01-instantiate-library.md.

The playbooks and workflows in this library are project-agnostic. Only the `constitution/` is project-specific. Bootstrapping = filling the constitution with real, verifiable content for one project.

**Success criterion:** after bootstrap, a stranger (human or AI) with only this library and repo access can make a correct change without asking the founder anything.

## Prerequisites

- Read access to the target repository
- Ability to run the project's toolchain (tests, linter, typecheck, DB queries)
- The project owner available for the interview (≈30–60 min), OR an existing architecture review / audit document to extract answers from

## Step 1 — The Instantiation Interview

Ask the owner (or extract from existing audit docs). Record answers verbatim first; distill later.

**Architecture & stack**
1. What are the runtime components and how do they talk? (e.g., mobile client → API → DB)
2. Which architectural decisions are *settled* and must not be relitigated? For each: what was the alternative, and why was it rejected?
3. What is the repository layout convention? (monorepo? where does new code of each kind go?)

**Security & data**
4. What is the trust boundary? Who/what is untrusted?
5. What are the non-negotiable security controls? (e.g., row-level security on every table, auth on every endpoint) — for each, how do you *check* it currently?
6. What data is sensitive, and what must never be logged or exposed?

**Correctness machinery**
7. What commands constitute "the build is healthy"? (test, typecheck, lint, dry-run migrate) Exact commands, exact expected outcomes.
8. What is the migration/schema-change procedure? What has gone wrong before?
9. What known legacy debt exists that new code must not imitate? (name the anti-patterns explicitly)

**Conventions**
10. Naming conventions (files, DB tables/columns, API routes, branches, commits)
11. Error-handling policy (wrap? propagate? user-facing message rules?)
12. What does "done" mean here? (tests required? docs? decision record?)

## Step 2 — Distill into STANDARDS.md

For every answer, write a standard in the mandatory three-part form:

```
### S-NN: <short imperative rule>
**Rationale:** <why — one or two sentences, so future readers don't relitigate>
**Verification:** <a command, query, or concrete inspection procedure that proves compliance>
```

**Hard rule: a standard without a Verification field may not be added.** If you cannot state how to check it, it is a preference, not a standard — either derive a check or drop it. This single constraint is what makes the library usable by AI models: verification requires no taste.

## Step 3 — Extract INVARIANTS.md

From the security and correctness answers, list properties that must hold at *all times* (not just at review time). Each gets:

```
### I-NN: <property statement>
**Check:** <executable command with expected output>
**On violation:** <block merge / page owner / rollback — pick one>
```

Aim for 5–15 invariants. More than that means some are actually standards (review-time), not invariants (always-true).

## Step 4 — Wire the verifier

Create a single entry point the Verifier role (see `workflows/review-workflow.md`) can run:

```
scripts/verify.sh   (or make verify / npm run verify — match project convention)
```

It must run: all invariant checks + build-health commands from Step 1 Q7. Exit code 0 = evidence of health; anything else = red. Keep it fast enough that nobody is tempted to skip it (<5 min ideally).

## Step 5 — Seed the decision log

Write the first decision record (`decisions/`) documenting the bootstrap itself: date, who was interviewed, which answers were uncertain and need revisiting. Uncertainty recorded is cheaper than certainty invented.

## Step 6 — Validation (do not skip)

Give the freshly bootstrapped library to a fresh AI session (or an engineer who has never seen the repo) with one small real task. Watch where they get stuck or guess. Every guess reveals a hole in the constitution — patch it. Repeat until a stranger can complete a small task with zero founder questions.

## Re-bootstrapping

Pivots, stack changes, or major refactors can invalidate parts of the constitution. Do not edit standards silently: retire them with a decision record (`Superseded by D-NN`), then add replacements. History of *why standards changed* is itself valuable context.
