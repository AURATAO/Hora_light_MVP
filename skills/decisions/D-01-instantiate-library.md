# D-01: Instantiate the skill library for HO:RA

**Date:** 2026-07-11
**Status:** Accepted
**Trigger:** Adoption of the generic correctness-first skill library. Bootstrap interview answered from BACKEND_REFERENCE.md (2026-07-07), the existing CLAUDE.md DB Access Rules, and the 2026 CTO-mode review; residual items below.

## Decision
HO:RA adopts the library with a **ratchet policy**: full constitution for new code (including the entire `mobile/` RN app); legacy `app/`/`server/`/schema bound only by §6 boundary rules.

Settled in this instantiation:
- **Authorization model:** Go backend is the sole authorization layer (S-01); constitution absorbs CLAUDE.md Rules 1–4.
- **Styling:** Tailwind (web) + NativeWind (mobile) + shared tokens (S-30). Rejected: StyleSheet-only (loses token sharing), CSS files (second system).
- **Identity:** internal UUID canonical; all clients use the `/auth/exchange` flow (S-04).

## Constitution impact
Standards S-01…S-60 and invariants I-01…I-07 (initial set).

## Context and alternatives
Full-compliance retrofit of legacy code rejected: cost lands before the Dec 2026 checkpoint with no user value. The ratchet reaches the same end-state asymptotically at near-zero scheduled cost.

## Evidence
BACKEND_REFERENCE.md documents the schema, the no-RLS design, the two-project split, and the concrete legacy debt now named in S-60. The CTO-mode review identified the structural risks these standards mechanize.

## Open items
- [x] D-02 CLOSED: exposure tested, rowsecurity=true on all tables, I-01a activated. Follow-ups: correct BACKEND_REFERENCE §1.3; confirm pg_policies is empty.
- [ ] **S-21 baseline:** `supabase db pull` → commit baseline migration → activate I-05.
- [ ] **S-60.4 zombie Edge Function:** check the Supabase dashboard for a live DB webhook pointing at `notify-new-task`; if none, delete the function (duplicate admin emails risk).
- [x] `TODO(confirm)` in S-30 RESOLVED by D-03: shared tokens live in root `design-tokens/colors.js`; `mobile/` scaffolded 2026-07-11.
- [ ] `TODO(confirm)` in I-04: confirm `tsconfig` strict mode is on in `app/`.
- [ ] verify.sh: install gitleaks (or chosen scanner); wire the I-01a SQL connection.
- [ ] Run verify.sh once on main; record the first baseline result here.
- [ ] Bootstrap Step 6 validation: fresh AI session + one small task; patch every hole where it guessed.
- [ ] Absorb/point: CLAUDE.md keeps its DB Access Rules section but adds "authoritative version: skills/constitution/STANDARDS.md S-01/S-10" to prevent divergence. BACKEND_REFERENCE.md remains the schema/API reference of record (constitution cites it; does not duplicate it).
