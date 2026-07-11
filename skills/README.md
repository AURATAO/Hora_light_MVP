# Skill Library — Correctness-First Project Continuation Kit

A reusable, project-agnostic skill library that lets **any engineer or context-capable AI model** continue a project at low cost, under the project's own standards, with **correctness as the only acceptance criterion**.

## What this is

Most handover documentation captures *steps*. This library captures *judgment*: the standards a change must satisfy, the machine-verifiable evidence that proves it satisfies them, and a multi-agent review workflow that blocks anything unproven.

## The three layers

```
skill-library/
├── constitution/        LAYER 1 — What is always true here
│   ├── STANDARDS.md     Project standards. Every rule has a rationale AND a verification method.
│   └── INVARIANTS.md    Properties that must never break. Every invariant has a check command.
├── playbooks/           LAYER 2 — How to do recurring work correctly
│   ├── debug/SKILL.md
│   ├── extend/SKILL.md
│   ├── migrate/SKILL.md
│   └── review/SKILL.md
├── workflows/           LAYER 3 — How changes get authorized
│   └── review-workflow.md   Builder / Adversarial Reviewer / Verifier / Arbiter protocol
├── decisions/           Append-only decision log (cheap future context)
│   └── TEMPLATE.md
└── bootstrap/           How to instantiate this library for a NEW project
    └── BOOTSTRAP.md
```

## Core rules (apply to humans and AI equally)

1. **The constitution is the single source of truth.** If code and constitution disagree, the constitution wins until a logged decision changes it.
2. **A claim without executable evidence is unproven.** "It should work" counts for nothing. A green test, a passing typecheck, a query result — those count.
3. **No change merges without passing the review workflow.** Correctness is the only standard; style preferences never block, standards violations always block.
4. **Every non-obvious ruling gets a decision record.** Future readers (human or AI) should never have to guess why.

## How to use this library

**Starting a new project (or adopting for an existing one):**
Read `bootstrap/BOOTSTRAP.md` and run the instantiation interview. It produces a filled-in `constitution/` for that project. The playbooks and workflows need no modification — they reference the constitution abstractly.

**As an engineer joining the project:**
Read `constitution/STANDARDS.md` end to end once (it is deliberately short). Then use playbooks on demand. Never memorize — the library is the memory.

**As an AI model in a fresh session:**
Load, in order: (1) `constitution/STANDARDS.md`, (2) `constitution/INVARIANTS.md`, (3) the one playbook matching your current task. Do not load everything; the library is designed for progressive disclosure. Assume you have no other project context — each playbook is self-sufficient.

**Placement in a repo:** copy this directory to `<repo>/skills/` (or `.claude/skills/` for Claude Code). Keep it in version control; the constitution evolves with the project via logged decisions.

## Non-goals

- This library does not teach general programming. It encodes *this project's* standards, not universal best practice.
- It does not replace CI. It defines what CI must prove, and adds adversarial review that CI cannot perform.
