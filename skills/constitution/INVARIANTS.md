# HO:RA Project Invariants (Constitution — Layer 1)

> **Status: ACTIVE.** `TODO(confirm)` items tracked in decisions/D-01.

Invariants must hold at all times. All checks run via `./skills/scripts/verify.sh`.

**Verifier note:** run it, report raw output. To claim a failure is pre-existing, run the same check on the base branch and show both raw results side by side.

---

### I-01a: The PostgREST side door is locked — RLS enabled with zero policies on every `public` table.
**Check:** against the Hora MVP DB (`akxsdkerudurzcemurrb`):
```sql
-- Tables where the side door is open (expected: zero rows)
select tablename from pg_tables
where schemaname = 'public' and rowsecurity = false;

-- Policies on Hora tables (expected: zero rows — a policy means someone
-- opened a client-direct path, violating S-01)
select tablename, policyname from pg_policies where schemaname = 'public';
```
→ expected: both queries return zero rows. **ACTIVE — blocking** (D-02 closed 2026-07-11: rowsecurity confirmed true on all tables; no lockdown migration was needed).
**On violation:** block merge.

### I-01b: No client-direct data access. Frontend and mobile code contains no `supabase.from(` / `supabase.rpc(` calls; permitted direct Supabase usage is exactly `supabase.auth.*` and public `avatars` URL reads.
**Check:** `grep -rn "supabase.from(\|supabase.rpc(" app/src mobile/ 2>/dev/null` → expected: no matches.
**On violation:** block merge (this is an unprotected access path — S-01).

### I-02: No privileged keys in client code or tracked files. `SUPABASE_SERVICE_ROLE_KEY`, `SUPABASE_DB_URL`, `SESSION_JWT_SECRET` exist only in server-side env.
**Check:** `grep -rn "service_role\|SUPABASE_DB_URL\|SESSION_JWT_SECRET" app/src mobile/ 2>/dev/null` → no matches; secret scanner (gitleaks) → no findings.
**On violation:** block merge + rotate the exposed credential immediately.

### I-03: Backend compiles, vets, tests green.
**Check:** `cd server && go build ./... && go vet ./... && go test ./...` → exit 0.
**On violation:** block merge; main frozen until green.

### I-04: Web (and mobile, once created) typechecks green.
**Check:** `cd app && npx tsc --noEmit` (and `cd mobile && npx tsc --noEmit`) → exit 0. `TODO(confirm: tsconfig strict is on)`
**On violation:** block merge.

### I-05: Schema matches committed migrations — zero drift. **(Dormant until the S-21 baseline migration lands; activating this is a D-01 open item.)**
**Check:** `supabase db diff --linked` → empty diff.
**On violation:** block merge; reconcile before any new migration.

### I-06: The mobile app builds. **(Dormant until `mobile/` exists.)**
**Check:** `cd mobile && npx expo export --platform ios` (or the chosen cheapest signal) → exit 0.
**On violation:** block merge.

### I-07: Cross-project isolation — no references to the birthday-card project.
**Check:** `grep -rn "aemwljralqsegrwivbub" app/ server/ mobile/ supabase/ 2>/dev/null` → no matches.
**On violation:** block merge (S-03).

---

## Maintenance rules
- Adding/removing/weakening an invariant requires a decision record.
- An invariant red >3 days without a decision record = process failure; stop feature work and fix it.
- Every incident/bug fix adds its defect class here or to verify.sh in the same PR ("regression → invariant").
