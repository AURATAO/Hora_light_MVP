# D-08: supporter_status gains a "rejected" state; mobile uses info@my-hora.com, web's support@horaapp.co is stale

**Date:** 2026-07-22
**Status:** Accepted
**Trigger:** Bringing the mobile supporter application flow to web parity (Work tab). Web's `app/src/components/SupporterStatusBanner.jsx` already renders a `rejected` branch, but `supporter_status` is derived in Go from two inputs only (`is_verified_supporter`, `supporter_applied_at`) and could never return `"rejected"` — the branch was dead code on both clients. Its "Contact Support" action also points at `support@horaapp.co`, a domain the product no longer uses.

## Decision
1. `public.profiles` gains a nullable `supporter_rejected_at timestamptz`
   (migration `20260722103000_profiles_supporter_rejected_at.sql`). Derivation
   in `server/main.go` `deriveSupporterStatus()` becomes:
   approved → rejected → applied → none. Rejection outranks the application
   timestamp because `supporter_applied_at` is never cleared.
2. `POST /supporter/apply` clears `supporter_rejected_at` while setting
   `supporter_applied_at`, so a rejected applicant can re-apply and returns to
   `"applied"` (second chance, no ops action required).
3. Two admin ops endpoints replace what were manual dashboard edits:
   `POST /ops/supporter-approve` (sets `is_verified_supporter = true`, clears
   `supporter_rejected_at`) and `POST /ops/supporter-reject` (sets
   `supporter_rejected_at = now()`, and `is_verified_supporter = false` so
   rejecting an already-approved supporter is a single call). Both take
   `{ profile_id }` or `{ email }`, both behind the existing hardcoded admin
   allowlist (S-14 / S-60.5 — no second list added).
4. Mobile's rejected banner contacts **`info@my-hora.com`**
   (`mobile/src/lib/constants.ts` `SUPPORT_EMAIL`), matching the live
   `my-hora.com` domain already used for the legal URLs. Web keeps
   `support@horaapp.co` for now; **backlog: web should switch to
   `info@my-hora.com` and its rejected branch becomes reachable once this
   backend change deploys** — no web change was in scope for this task.

## Constitution impact
- Standards added: none
- Standards modified/retired: none. S-05 still holds — the new state is derived
  in Go only; both clients render `supporter_status` verbatim.
- Invariants added/changed: none. `profiles` is an existing table, RLS already
  enabled with zero policies (S-10 / D-02); the migration adds no new surface
  and no policy.

## Deploy order (S-21 §3, expand step only)
The column is additive and nullable, but the Go build selects it. Apply the
migration to `akxsdkerudurzcemurrb` **before** deploying the new server build
(old code against the new schema is fine — it simply never reads the column).
No backfill; `NULL` means "not rejected", which is the correct state for every
existing row. Reversible: `ALTER TABLE public.profiles DROP COLUMN
supporter_rejected_at;` loses only rejection timestamps set after this ships.

## Context and alternatives
Deriving "rejected" from an absence (e.g. applied long ago and still not
verified) was rejected outright — it would flip real users to a terminal-looking
state purely because review was slow. A separate `supporter_applications` table
was considered and rejected as premature: one nullable timestamp answers the
only question the UI asks today, and a real application history can supersede it
later without breaking the derivation.

Rejection notifications (in-app / email to the applicant) are deliberately **not**
part of this change: ops rejects by hand today and communicates by hand, and
`notify.Create` is task-scoped (`CreateNotificationInput` is built around a task
id), so a profile-scoped notification would be new machinery. Flagged here so
S-32's "define your recipients" is answered explicitly rather than forgotten.

## Evidence
- `server/main.go` `deriveSupporterStatus()` before this change: `approved` /
  `applied` / `none` only. `app/src/components/SupporterStatusBanner.jsx`
  line ~20: `if (status === 'rejected')` — unreachable.
- `grep -rn "is_verified_supporter" server/` before this change: only reads
  (`select coalesce(is_verified_supporter, false)`), zero writes — approval was
  a manual dashboard edit, confirming there was no approve path to reuse.
- `app/src/components/SupporterStatusBanner.jsx` — `mailto:support@horaapp.co`;
  `mobile/src/lib/constants.ts` `LEGAL_URLS` — `https://www.my-hora.com/...`,
  the domain in current use.
- Post-change: `cd server && go build ./... && go vet ./... && go test ./...`
  green; `cd mobile && npx tsc --noEmit` clean; DESIGN.md §8 greps clean.
- Opportunistic fix in the same diff (§6 ratchet, handler already open):
  `patchMyProfile` never called `deriveSupporterStatus()`, so `PATCH /profile`
  answered `supporter_status: ""`. Mobile's edit-profile sheet stores that
  response, so a user who edited their profile saw a blank status until the
  next `GET /profile`.
