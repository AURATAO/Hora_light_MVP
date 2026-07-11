# D-05: Supabase JWT signing-key rotation readiness (HS256 → ES256)

**Date:** 2026-07-12
**Status:** Accepted
**Trigger:** Owner is rotating Supabase project `akxsdkerudurzcemurrb` from the legacy HS256 shared JWT secret to asymmetric signing keys (ECC P-256 / ES256), ahead of eventually revoking the legacy secret and legacy anon/service_role API keys. Requested a Bearer-token verification audit before clicking "Rotate keys" (S-40 Tier 3: touches auth).

## Decision
1. **ES256 already verifies today, everywhere it's checked — no fix was required for that.** `/auth/exchange` (`server/main.go`) and `dualAuth`/`tryAuth`'s Bearer path both ultimately delegate non-HS256 tokens to `jwks.Keyfunc`, and `github.com/MicahParks/keyfunc/v2` (v2.1.0, already the pinned/latest version) parses EC/P-256 JWK entries into `*ecdsa.PublicKey`, which `golang-jwt/jwt/v5`'s built-in ES256 method verifies natively. Confirmed live: `GET https://akxsdkerudurzcemurrb.supabase.co/auth/v1/.well-known/jwks.json` already returns an ES256 key (`kid: 6a592ba2-83ec-4b9f-a20f-cf773654fad8`) pre-rotation.
2. **Fixed an adjacent, pre-existing gap (owner opted in after being asked):** `dualAuth`/`tryAuth`'s Bearer-token verification had no HS256 fallback — only `/auth/exchange` did. Refactored the duplicated alg-switch (HS256 → `SUPABASE_JWT_SECRET`; everything else → JWKS) into one `supabaseKeyfunc()` helper (`server/main.go`, near the `jwks` var), used by all three call sites: `/auth/exchange`, `dualAuth`, `tryAuth`. Marked with `// TODO: remove the HS256 branch once the legacy JWT secret is revoked.` This gap was dormant (web runs `VITE_AUTH_MODE=cookie`; mobile is cookie-only, never sends Bearer), so it wasn't strictly required by the rotation itself, but closes it for defense-in-depth before the legacy secret goes away.
3. **JWKS refresh is periodic (hourly), not on-demand for unknown `kid`s** (`keyfunc.Options` in `server/main.go` doesn't set `RefreshUnknownKID`). Not fixed — the key involved in this rotation is already published and the running backend already has a chance to have cached it; a redeploy/restart at rotation time is recommended as a safety margin (see Evidence) rather than a code change. Revisit `RefreshUnknownKID` (with `RefreshRateLimit`) only if a future rotation needs tighter guarantees.
4. **Legacy API key migration (service_role/anon → new secret/publishable keys) was audited but explicitly not executed** — owner wants a single coordinated switch later. Checklist:
   - `server/main.go` `newStorageClientV1()` (avatar uploads, via `storage_go.NewClient`): only sends `Authorization: Bearer <key>`, no `apikey` header at all. The new secret key is not a JWT, so this client needs code changes, not just an env swap — `storage-go` v0.8.1 (latest available) always sets `Authorization: Bearer <token>` unconditionally in its constructor, so a clean apikey-only client isn't possible via its public API as-is. Needs either testing whether the server tolerates a non-JWT Bearer value alongside a valid `apikey` override, or bypassing the library for raw HTTP calls (matching the pattern already used for completion-photo uploads).
   - `server/main.go` completion-photo upload (raw `http.Request`, ~line 2412): already sends both `Authorization: Bearer <serviceKey>` and `apikey: <serviceKey>` (comment: "Kong gateway requires this"). Likely only needs the env value swapped; the `Authorization` line may need removal if a non-JWT value there causes rejection — test before cutover.
   - `app/src/lib/supabaseClient.js` reads `VITE_SUPABASE_ANON_KEY` from `app/.env` — swap to the new publishable key value; no code change expected (`createClient()` accepts both formats).
   - `mobile/src/lib/supabase.ts` reads `EXPO_PUBLIC_SUPABASE_ANON_KEY` — same, value-only swap, confirmed but not touched (mobile foundation build, D-03).
   - `supabase/functions/notify-new-task/index.ts` — no Supabase key usage at all (only `SMTP_PASS` for Postmark). Nothing to change.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none (I-03 re-verified green post-change)

## Context and alternatives
- Considered leaving the Bearer HS256 gap alone since it's dormant and outside the literal ES256 question. Owner chose to fix it now since the code was already open and the pattern was proven elsewhere in the same file (see conversation).
- Considered bumping `storage-go` for the avatar-upload apikey issue — no newer version exists in the module proxy (`v0.8.1` is latest), so there's nothing to upgrade to yet; noted as a checklist item for the coordinated key-swap instead of solved here.

## Evidence
- `server/go.mod:6,10` — `MicahParks/keyfunc/v2 v2.1.0`, `golang-jwt/jwt/v5 v5.3.0`.
- `server/main.go:190-209` — JWKS setup (`SUPABASE_JWKS_URL` env-driven, hourly refresh).
- `server/main.go` `supabaseKeyfunc()` (new), used at the `/auth/exchange` handler and inside `dualAuth`/`tryAuth`.
- `keyfunc/v2@v2.1.0/ecdsa.go` — confirms P-256/EC JWK parsing support.
- `curl https://akxsdkerudurzcemurrb.supabase.co/auth/v1/.well-known/jwks.json` (2026-07-12) — live ES256 key already published, pre-rotation.
- `app/.env:6` — `VITE_AUTH_MODE=cookie`, confirming the Bearer-path gap was dormant.
- `storage-go@v0.8.1/client.go:56` — `Authorization: Bearer` set unconditionally in `NewClient`.
- `go build ./... && go vet ./... && go test ./...` (server/) — green after the refactor.
- `bash skills/scripts/verify.sh` — green except the pre-existing, unrelated gitleaks finding tracked separately (historical `server/.env` secret in git history, predates this session).
