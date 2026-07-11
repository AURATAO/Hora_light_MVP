---
name: migrate
description: Change database schema, perform data migrations, or upgrade critical dependencies. Use for ANY change to schema, stored data shape, or infrastructure-level dependencies. Always Tier 3 review. Produces a reversible-or-explicitly-irreversible migration with dry-run evidence.
---

# Playbook: Migrate

**Prerequisites:** `constitution/STANDARDS.md` (especially the schema and migration standards) and `constitution/INVARIANTS.md` read in this session.

**Classification: every change under this playbook is Tier 3.** The Arbiter is the project owner. No exceptions for "tiny" migrations — the smallest migrations have caused the largest incidents.

**Success criterion:** the migration applies cleanly to a production-shaped database, the application works against the new schema *before and after* deploy (see step 3), and there is a written answer to "what if this goes wrong."

## Procedure

### 1. Write the migration under the project's declared procedure
Follow the migration standard in the constitution exactly (tooling, direction policy, naming). If the constitution's migration procedure is a `{PLACEHOLDER}`, **stop** — bootstrap is incomplete; escalate rather than improvise.

### 2. Classify reversibility honestly
- **Reversible:** write and test the down path.
- **Irreversible** (drops, destructive backfills, type narrowing): say so in bold in the self-report, and state the recovery plan (backup point, restore procedure, acceptable data-loss window). An irreversible migration without a stated recovery plan is auto-Blocked.

### 3. Respect deploy-order reality
Schema and application code do not deploy atomically. For any change where old code will run against the new schema (or new code against old schema) for even seconds, use the expand → migrate → contract pattern: add the new shape, dual-support in code, backfill, then remove the old shape in a *later* migration. Single-step breaking changes are only acceptable with a declared maintenance window.

### 4. Dry-run against production shape
Run the migration against a branch/staging database with production-like data (or a recent snapshot). Capture:
- Apply output (clean, no warnings)
- Row counts / integrity checks before and after
- Application test suite green against the migrated schema
- Drift check: schema after migration matches committed state exactly

### 5. Security controls migrate with the schema
New tables/columns created by the migration get their security policies **in the same migration**. A migration that creates an unprotected surface violates the security baseline even if a follow-up is "planned."

### 6. Evidence bundle for review
Submit through `workflows/review-workflow.md` (Tier 3) with:
- The migration file(s) + down path or recovery plan
- Full dry-run capture from step 4
- Deploy-order statement: exact sequence of "merge → migrate → deploy" steps and what runs against what in between
- Verify entry point green post-migration

## Dependency upgrades
Major-version upgrades of framework/runtime/DB-client dependencies follow this same playbook: changelog review for breaking changes → upgrade on a branch → full verify + manual pass over the areas the changelog flags → Tier 3 review. Minor/patch upgrades may go Tier 2 if verify is green.
