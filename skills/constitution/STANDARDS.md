# HO:RA Project Standards (Constitution — Layer 1)

> **Status: ACTIVE for new code. Ratchet policy for legacy code (see §6).**
> Instantiated 2026-07 from the generic library; revised against BACKEND_REFERENCE.md (2026-07-07).
> Remaining `TODO(confirm)` items tracked in decisions/D-01.

Single source of truth for what "correct" means in HO:RA. Overrides habits, training-data conventions, and any pattern found in legacy code. Amendments require a decision record.

**Scope policy (the ratchet):** all standards bind new code in full — including the entire `mobile/` React Native app. Legacy code (`app/`, `server/`, existing schema) is governed by §6 only: no imitation, no expansion, opportunistic fixes.

---

## 1. Architecture (settled — do not relitigate in code)

### S-01: The Go backend (`server/`) is the sole authorization layer and the sole write path for Hora business data.
All reads/writes to Hora tables (`users`, `profiles`, `tasks`, `worklogs`, `notifications`, `task_gps_pings`, `reviews`) go through the Go API — from web, from mobile, from any future client. Clients never query these tables via supabase-js/PostgREST.
**Rationale:** The DB has no row-level authorization by design; Go connects as the `postgres` role and enforces all permissions in application code (see CLAUDE.md DB Access Rules, which this standard supersedes-and-absorbs). Any client-direct path is an unprotected path.
**Verification:** `grep -rn "supabase.from(\|supabase.rpc(" app/src mobile/ 2>/dev/null` returns nothing (I-01b); permitted direct calls are exactly `supabase.auth.*` and public `avatars` bucket URL reads.

### S-02: Directory layout is settled: web → `app/`, Go backend → `server/`, schema artifacts → `supabase/`, mobile → `mobile/` (Expo/React Native, EAS Build). No new deployable services.
**Rationale:** Solo technical maintainer; minimal operational surface.
**Verification:** New code lands in its designated directory; reviewer flags any new top-level service or deploy target.

### S-03: This repo touches exactly ONE Supabase project: Hora MVP (`akxsdkerudurzcemurrb`). The birthday-card project (`aemwljralqsegrwivbub`) is out of scope for all Hora code.
**Rationale:** Two live projects with separate auth/storage/DB exist under the same account; cross-wiring credentials or tables would corrupt both. (BACKEND_REFERENCE §0.)
**Verification:** `grep -rn "aemwljralqsegrwivbub" app/ server/ mobile/ supabase/` returns nothing.

### S-04: Identity mapping is settled: Supabase `auth.users.id` ≠ internal `public.users.id`. The acting principal in all business logic is the **internal** UUID, resolved via `users.supabase_sub` / `users.google_sub`. Clients authenticate via Supabase Auth, then exchange for the `hora_session` cookie (`POST /auth/exchange`); mobile follows the same exchange flow.
**Rationale:** Two UUID spaces already caused documented confusion (BACKEND_REFERENCE gotcha 1). One canonical mapping, one exchange flow, for every client.
**Verification:** Reviewer checks any new auth-adjacent code resolves identity through the middleware-provided internal UUID, never from a raw Supabase token `sub` or client-supplied ID.

### S-05: Pricing and `supporter_status` are computed in Go only. Clients display what the API returns; they never store or re-derive cost or status.
**Rationale:** The formula (base + minutes×50¢) and status derivation have no DB columns; a second implementation in a client guarantees drift. (Gotchas 7–8.)
**Verification:** grep clients for the pricing constants (`* 50`, base-fee cents values) and status derivation logic → nothing.

## 2. Security baseline

### S-10: RLS is enabled on every table in `public` with **zero policies** (default-deny). RLS here is not the authorization layer — it is the lock on the PostgREST side door. Go (postgres role) bypasses it; every other role is denied everything.
**Rationale:** The anon key ships in client bundles. Without RLS, the auto-exposed Data API would allow anyone to read/write all tables directly, bypassing Go entirely. Deny-all RLS closes that door at zero cost to the Go path. *(Pending D-02 confirmation of current exposure — see decisions/D-02.)*
**Verification:** I-01a query returns zero rows; any *policy* appearing on a Hora table is itself a finding (it would mean someone opened a client-direct path, violating S-01).

### S-11: No endpoint trusts a client-supplied user ID as the acting principal. Identity comes from `dualAuth`/`tryAuth` middleware (cookie or validated Bearer), always.
**Rationale:** Acting-as-someone-else is the worst bug class in a two-sided marketplace. **Permission checks use UUID columns (`requester_id`, `assigned_to_id`), never the legacy email columns.**
**Verification:** Reviewer checklist on every diff touching `server/` handlers; grep new handlers for authorization comparisons against email columns → nothing.

### S-12: Sensitive personal data — credentials, phone numbers, precise GPS coordinates, background-check/identity data (future Checkr), payment details — never appears in logs, error messages, or analytics events.
**Rationale:** `task_gps_pings` is live user location; `profiles.phone` is PII. One leaked log line outlasts the company.
**Verification:** Reviewer checklist on diffs touching those tables' code paths; grep log/print call sites for the sensitive field names.

### S-13: New data surfaces ship with their controls in the same commit: new table → RLS enabled (deny-all) in the same migration; new endpoint → auth middleware in the same diff. "In a follow-up PR" is a forbidden phrase.
**Rationale:** Follow-ups don't happen under founder time pressure; the gap window is when incidents happen.
**Verification:** Reviewer confirms every `CREATE TABLE` is accompanied by `ENABLE ROW LEVEL SECURITY`; every new route is inside an authed group or has an explicit decision record for being public.

### S-14: Admin access changes (the ops allowlist) are Tier 3 changes with a decision record, even though the list is hardcoded in Go.
**Rationale:** The allowlist IS the admin authorization system (gotcha 9). Its hardcoded-ness is tracked debt (S-60.5), not license for casual edits.
**Verification:** Diff touching the allowlist map → Tier 3 review, owner as Arbiter.

## 3. Data & schema

### S-20: New tables/columns: snake_case; store identity as internal-UUID columns named `*_id` with FKs to `users.id`. **Never add email-as-identity columns** — that pattern is retired legacy (S-60.1/60.2).
**Rationale:** New schema converges on the reformed convention; the email-column era is closed.
**Verification:** Reviewer checks every new identifier in schema changes; any new `text` column semantically holding an identity is a FAIL.

### S-21: Schema changes require committed migration files. Since no migration files currently exist (schema was built via dashboard and is only inferred from Go queries), the first schema task is a **baseline pull** (`supabase db pull` → committed baseline migration). After the baseline: forward-only migration files, dry-run against a branch/staging DB, no production dashboard SQL. Ever.
**Rationale:** An un-versioned schema means every AI session and every new engineer works from inference, and drift is undetectable. The baseline converts the schema from folklore to fact.
**Verification:** post-baseline, I-05 drift check green; every schema-touching PR contains a migration file.

## 4. Code conventions (new code)

### S-30: Styling — web (`app/`) uses Tailwind utilities exclusively; mobile (`mobile/`) uses NativeWind with shared design tokens. No new CSS/SCSS files, no styled-components; inline StyleSheet only where NativeWind cannot express it (declare in self-report). `TODO(confirm: shared token location)`
**Rationale:** One styling system per platform, shared tokens, solo maintainer (D-01).
**Verification:** diff adds no `.css`/`.scss`/styled-components imports.

### S-31: TypeScript strict; `any` requires an inline justification comment. Go errors wrapped with context (`fmt.Errorf("...: %w", err)`), never discarded with `_`.
**Rationale:** Discarded errors are the top source of silent marketplace-state corruption.
**Verification:** `tsc --noEmit` green; `go vet ./...` green; reviewer scans new `_ =` assignments.

### S-32: Side effects (notifications, emails) follow the existing pattern: async/goroutine, never blocking the request path, and every new state-change event defines its notification recipients explicitly (see BACKEND_REFERENCE §2.3 for the event table).
**Rationale:** Consistency with the working notification system; forgotten recipients are silent UX failures.
**Verification:** Reviewer checks new state-change endpoints against the event table.

### S-33: User-facing errors: plain language, no stack traces, no internal identifiers. Full detail to server logs (minus S-12 fields).
**Verification:** Reviewer checklist on user-facing error paths.

## 5. Definition of done

### S-40: Done = (1) `./skills/scripts/verify.sh` green, (2) review workflow Approved for the change's tier, (3) declared interpretations have decision records. Tier 3 = anything touching auth, the exchange flow, RLS state, migrations, admin allowlist, pricing logic, or GPS/PII handling; owner is Arbiter.
**Verification:** `workflows/review-workflow.md`.

---

## 6. Legacy boundary (the ratchet) — concrete list

Legacy code must not get worse; named anti-patterns must not be imitated or extended. Fix opportunistically when a task already touches the site.

### S-60: Named legacy anti-patterns — do not copy, do not extend:

1. **Email-as-identity on tasks** — `tasks.requester`, `tasks.assigned_to` (text emails). Authoritative: `requester_id`, `assigned_to_id`. New code never reads emails for permissions and never adds email-identity columns.
2. **`worklogs.user` is an email, not a UUID.** Known inconsistency; join via `users.email` when unavoidable. New tables reference `users.id`.
3. **`profiles.id` drift risk** — kept equal to `users.id` only by Go patching logic. Never insert into `profiles` from outside the Go backend.
4. **Zombie Edge Function** — `supabase/functions/notify-new-task` duplicates Go's `NotifyAdminNewTask()`. Do not extend it; do not add new Edge Functions for jobs the Go backend owns. (Remediation: check for a live DB webhook, then delete — tracked in D-01.)
5. **Hardcoded admin allowlist** in `server/` main. Do not add a second hardcoded authorization list anywhere; changes to this one are Tier 3 (S-14).
6. **WhatsApp stubs** — receiver logs only; outbound is TODO. Do not build on top of the stubs as if they were functional.
7. **Un-versioned schema** — until the S-21 baseline lands, treat BACKEND_REFERENCE §1 as the schema documentation of record and update it with any change.

**Rationale:** Legacy code is the most persuasive bad documentation in the repo; naming the debt breaks the copy chain — for newcomers and for AI models alike.
**Verification:** Reviewer checks every diff against this list; grep patterns per entry where applicable.

---

## Amendment procedure
Propose via decision record → on acceptance edit here, mark superseded standards `Superseded by D-NN`. Never silently edit or delete.
