# D-04: Fix I-01b/I-02 check scoping for `mobile/`

**Date:** 2026-07-11
**Status:** Accepted
**Trigger:** First `verify.sh` run after `mobile/` was scaffolded (D-03) went red on I-01b and I-02. Per `workflows/review-workflow.md`, an Arbiter may not overrule a red Verifier with reasoning — the check must be fixed first, via decision record, then re-run.

## Decision
I-01b and I-02's grep checks scanned `app/src` (scoped) but bare `mobile/`
(unscoped — includes `mobile/node_modules`). `@supabase/supabase-js`'s own
package ships doc comments and type definitions containing the literal
strings `supabase.from(`, `supabase.rpc(`, and `service_role` — none of it is
application code. Both checks now scan `mobile/src` instead of `mobile/`,
matching the scoping already used for `app/src`.

Verified clean after the fix:
```
$ grep -rn "supabase\.from(\|supabase\.rpc(" app/src mobile/src
(no output)
$ grep -rn "service_role\|SUPABASE_DB_URL\|SESSION_JWT_SECRET" app/src mobile/src
(no output)
```

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: I-01b, I-02 check commands corrected in `constitution/INVARIANTS.md` and `scripts/verify.sh` (wording/scope only, not the property being checked)

## Context and alternatives
- Considered adding `--exclude-dir=node_modules` instead of scoping to `src/`. Rejected: `src/` scoping is simpler, matches the existing `app/src` precedent, and is what the invariant is actually about (application code, not tooling/docs/config).
- Did not touch I-07's check (`app/ server/ mobile/ supabase/`, unscoped) — it currently returns zero matches even including `mobile/node_modules`, and that check's subject (cross-project ID leakage) plausibly warrants the broader scope. Revisit only if it produces a false positive.

## Evidence
- `bash skills/scripts/verify.sh` run 2026-07-11: I-01b and I-02 both printed matches exclusively from `mobile/node_modules/@supabase/*` (auth-js migration docs, `.d.ts`/`.d.mts` comments, sourcemaps) and one from `mobile/README.md` (a sentence *warning against* using `service_role`, not a usage).
- Re-run of the corrected grep commands above: zero matches in both `app/src` and `mobile/src`.
