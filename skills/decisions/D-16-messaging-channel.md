# D-16: Requesters book over iMessage and WhatsApp through the Go backend

**Date:** 2026-10-20
**Status:** Accepted with amendments; review CONDITIONALLY APPROVED at round 5 (Arbiter: Aura, 2026-10-20) — conditions C-1..C-3, deadline 2026-10-30, no merge to main before. Tier 3.
**Trigger:** Product pivot (Aura, 2026-10-16): requesters order through an AI agent over iMessage (Sendblue) and WhatsApp (Meta Cloud API); the mobile app becomes a supporter-only dashboard. Business rules unchanged. Built on `feat/messaging-channel`; nothing merges to `main` during the App Store review freeze. Plan and progress: `PLAN.md`.

## Decision

1. **The chat is a new entry point into the existing backend, not a new service** (S-02). Package `server/internal/channel` (adapters, webhook handler, router, stores, LLM client); wiring in `server/channel_*.go`. Mounted only when `CHANNEL_WEBHOOKS_ENABLED=1` — inert in production until switched on.
2. **Four new tables** (`channel_identities`, `channel_messages`, `channel_conversations`, `channel_setup_tokens`), one migration (`20261019120000_messaging_channel.sql`): RLS enabled, zero policies, explicit REVOKE from PUBLIC/anon/authenticated (S-10, S-13). Go is the only reader and writer (S-01). Identity columns are `user_id` → `users.id` (S-20).
3. **A phone number becomes an account only through proof of both halves:** a single-use, 30-minute, SHA-256-hashed setup token delivered to that number, plus Supabase email OTP on the setup page, exchanged through `dualAuth` (S-04, S-11). `ConsumeSetupTokenAndLink` is one transaction and refuses a number already linked elsewhere. `profiles.phone` is never used to match accounts.
4. **Money moves only on an explicit YES matched by rule** (`IsYes`), never by the model. `ClaimForPosting` + `AcceptTerms` make a booking exactly-once across parallel YESes and the setup page's finish step. A booking runs the real `createTask` handler in-process for the linked user, so every payment gate (`PAYMENTS_ENFORCED`, card, balance, 3DS, decline) is production's own. Quotes use `quoteTask` (S-05).
5. **Model:** Baseten `zai-org/GLM-5.3-Flash` through an OpenAI-compatible client, swappable by env (`LLM_*`). It extracts a task draft with the app's own `aiTaskParserSystemPrompt` (read-only; `ai_task_parser.go` is frozen) and classifies intent. It decides nothing about money or state.
6. **Status updates reach the chat through `notify.ChatHook`**, a nil-by-default hook set once at startup when the flag is on. Recipient: the task's requester only, on the thread the task was booked from (S-32). Events that ask for an action in the app (approvals, time cap reached, balance due) are not sent until the chat can act on them (M3). `ON_MY_WAY` is chat-only.
7. **S-12:** phone numbers are redacted in every log line (`Redact`); message bodies are never logged; the setup token travels in the URL fragment; the request logger skips the WhatsApp webhook paths (verify token in the query). Webhooks fail closed: no secret configured → 401.
8. **Staging** is a Render service on the branch plus a Supabase dev branch of the same project (S-03), with its own keys (Postmark server, Stripe sandbox, DB password). `APP_ENV=staging` refuses a live Stripe key outright.

## Declared interpretations (Builder) — each needs the Arbiter's ruling

- **I-A. Message bodies and phone numbers are stored at rest** in `channel_messages` / `channel_identities`. S-12 governs logs, errors and analytics, not storage; storage is needed for dedupe, threading and ops handoff. *Open:* retention period (proposal: bodies purged after 90 days).
- **I-B. Third-party processors.** Message text goes to Sendblue / Meta (transport) and Baseten (model). Privacy Policy must list them before launch (PLAN §11).
- **I-C. `ON_MY_WAY` is a new notification, chat-only.** Interpreted as not changing business rules: app users get the live map at that moment; a chat customer has no map. Aura asked for it 2026-10-20.
- **I-D. `notify.ChatHook` is a package-level variable** assigned once before the server serves. Chosen over threading a dependency through ~20 `notify.Create` call sites in frozen code.
- **I-E. `created_via = 'ai_parse'` for chat bookings** until a `message` value can be added after the freeze. Analytics cannot yet tell app-AI posts from chat posts (the `channel_messages.task_id` link can).
- **I-F. Production-affecting edits outside the new package** (all behaviour-equivalent in production): `main.go` — `gin.Default()` rebuilt as `gin.New()` + Logger(SkipPaths) + Recovery; `DB_MAX_CONNS` (default 8, unchanged). `payments.go` — staging-only live-key refusal. `notify.go` — the hook (nil in prod). `live_tracking.go` — `ON_MY_WAY` call (no-op while the hook is nil). `AvatarUploader.jsx` — reads `VITE_API_BASE_URL` as a fallback.
- **I-G. The migration file was edited after its first apply to the staging branch** (added `awaiting_name`, `pending_name`, `terms_accepted_at`). It has never shipped; the branch was altered to match. S-21's forward-only rule is read as applying from first production apply.
- **I-H. Legacy `/webhooks/whatsapp` stub (S-60.6)** stays registered during the freeze; removed in the go-live change.

## Constitution impact
- Standards added: none proposed yet. Candidate: "a credential never travels in a URL query string" (generalising the S-12 fixes here).
- Standards modified/retired: none. CLAUDE.md Rule 1's table list gains the four `channel_*` tables at merge.
- Invariants: Rule 3's four queries on the staging branch: 3 / 0 / 0 / 0 (re-run in the review evidence bundle). Regression tests added: `TestParallelYesBooksOnce` (double-booking found 2026-10-20), `TestSetupLinkKeepsTokenOutOfTheQuery`, `TestChatStatusHookOnlyTellsTheRequester`.

## Context and alternatives

**Why inside `server/` and not a separate bot service:** S-02, and booking must run the exact production payment path. A separate service would call the API as a client and re-implement auth.

**Why a web page at the first YES at all:** a card cannot be collected in a chat, and email OTP is the only account proof we have. The person leaves Messages once; every later booking stays in the chat.

**Why a hook and not a second notification system:** one event source keeps email, push, in-app and chat saying the same thing at the same moment.

## Evidence
- Real-device runs on staging, 2026-10-20: iMessage first booking; accepted / clock in / clock out / completed received; WhatsApp test number round-trip. Logged in `PLAN.md` §15.
- Double-booking defect reproduced under `go test -race` and fixed (`AcceptTerms`).
- Verify and review evidence: appended below by the Verifier and Reviewer, not by the Builder.

## Review log

### Round 1 — 2026-10-20 (Builder: Claude Opus/Fable sessions; Reviewer: Sonnet, isolated; Verifier: Haiku, isolated)

**Verifier:** `verify.sh` EXIT=1. Only red step: `schema drift (I-05)` — `supabase: command not found` (CLI absent in the build environment). Go build/vet/test green (incl. `-race ./internal/channel/...`), web build green, web guards 131/131, mobile typecheck + export green, I-01b/I-02/I-07 ok, gitleaks SKIP (not installed). Rule 3 queries on the staging branch (Builder evidence): 3 / 0 / 0 / 0.

**Reviewer:** FAIL. Fixed by the Builder in `0028025` (no argument offered):
S-31 discarded errors and unwrapped returns (code and tests); S-12 `?t=` still accepted by the page; S-32 chat send on the setup page's request path and on-my-way lookups on the enroute request path.

**Reviewer findings that need an Arbiter ruling** (the Builder may not overrule them; each either becomes a code change or a constitution amendment recorded here):

- **R-1 · S-11** — `chatBooker.Post` sets `uid` on a synthetic gin context and calls `createTask`. Identity there comes from a provider-signed webhook → `channel_identities.user_id`, which was itself set only through `dualAuth` on `/channel/setup/link`. *Options:* (a) amend S-11: "or a server-verified channel identity linked through dualAuth (D-16)"; (b) refactor `createTask` so its core takes a uid argument and both callers pass it (touches frozen code).
- **R-2 · S-13** — `/webhooks/channel/*`, `/channel/setup/config`, `/channel/setup/check` have no auth middleware. Webhooks are authenticated inside the handler (Sendblue shared secret, Meta HMAC, fail-closed) — the same pattern as the existing Stripe and TalkJS webhooks; `/check` is authorised by the setup token itself; `/config` serves only public values (Supabase URL + publishable key). *Options:* (a) amend S-13: "or a provider-signature / single-use-capability check in the handler, declared in a decision record"; (b) wrap each in a named middleware so the control is visible at the route.
- **R-3 · S-20** — `channel_identities.address` / `channel_messages.address` hold phone numbers as text. The HO:RA identity is `user_id` (FK to `users.id`); the address is the external channel endpoint, like an email address for Postmark. *Options:* (a) amend S-20 to say channel endpoints are not HO:RA identity; (b) none practical — a phone number must be stored to send to it.
- **R-4 · S-02 / S-30** — the setup page is HTML + hand-written CSS served by Go (`server/`), not a page in `app/` with Tailwind. Reason it was built this way: `app/` deploys from `main` (Vercel) and cannot ship during the review freeze; the page must be reachable from staging now. *Options:* (a) accept as a declared exception until go-live, then move it to `app/` (Tailwind) in the go-live change; (b) move it now to `app/` and test via a Vercel preview of the branch.
- **R-5 · I-05 red** — the only red in `verify.sh` is the missing Supabase CLI. Per INVARIANTS, a pre-existing claim needs the same check on the base branch side by side; the Builder has not shown that. *Options:* (a) install the CLI + link credentials in the build environment and re-run; (b) decision record scoping I-05 to environments that have the CLI (it was "dormant" by design until the baseline existed).

**Arbiter (Aura), 2026-10-20 — rulings:**

- **R-1 → amend S-11, no code change.** The chat principal's trust chain is written into S-11 as five conditions (provider signature per message; binding only through `dualAuth`; binding needs the single-use setup token; one number one account; money only on a rule-matched YES). **Post-freeze refactor #1:** `createTask` takes the principal as an argument; the synthetic request context goes away.
- **R-2 → named middleware.** Done in `30b9e19`: `providerSignatureAuth`, `whatsappSubscribeAuth`, `setupTokenAuth`, `publicByDesign` (`server/channel_auth.go`), each named on its route. No standard amended.
- **R-3 → amend S-20, with three conditions:** E.164 only (CHECK on every such column — `channel_messages.address` gained it in `20261020120000`), unique per active binding, treated as personal data (S-12; purged with the account — `channel.PurgeUserTx` in account deletion, `4cb3e60`; Privacy Policy before launch).
- **R-4 → declared exception until go-live.** The setup page stays Go-served with hand-written CSS on the branch; the go-live change moves it to `app/` with Tailwind. Tracked in PLAN §11.
- **R-5 → report honestly; make the failure mode impossible to misread.**
  - Evidence (same environment, same step, side by side): base `541f5e9` → `supabase: command not found` / `RED: schema drift detected`; branch `30b9e19` → identical. **Base also red → environment issue**, ticket opened (PLAN §11), not a regression.
  - The old step ran `supabase db diff --linked`, and the committed `supabase/.temp/project-ref` is **production** (`akxsdkerudurzcemurrb`). With the CLI installed it would have diffed against production. It was not run that way.
  - No CI exists, so there is no CI CLI version to match; the version recorded in the repo is **v2.118.0** and that is what was installed for this check. `db diff` also needs a Docker daemon, which this environment does not have.
  - `verify.sh` now: preflight lists missing tools/settings as `缺少相依套件 / MISSING DEPENDENCY`; a check that cannot run reports `INCOMPLETE` and the run exits **2** (not green, not a test failure); I-05 uses `--db-url "$VERIFY_DB_URL"` (local/branch only) and **refuses a URL containing the production ref** (exit 1). Exercised: no CLI → exit 2; CLI 2.118.0 without Docker → exit 2; production URL → exit 1. INVARIANTS I-05 updated to match.

### Round 2 — 2026-10-20 (Reviewer: new Sonnet session; Verifier: Haiku)
**Verifier:** EXIT=2 INCOMPLETE — no red, no failing test; only I-05 not run (no Docker daemon; env ticket). Working tree clean after the run (the CLI version check no longer rewrites `supabase/.temp/cli-latest` — fixed in the same round).
**Reviewer:** FAIL, S-31 only (PendingFor dropped an error; HMAC `Write` unchecked; `rows.Close` / body `Close` unchecked). Explicitly checked and passing: S-01, S-10, S-11 conditions 1–5, S-12, S-13, S-20 conditions 1–3, S-21, S-32, S-33, S-60, Rule 3. Fixed in `c6f0876`.

### Round 3 — 2026-10-20 (Reviewer: new Sonnet session; Verifier: Haiku)
**Verifier:** EXIT=2 INCOMPLETE — identical to round 2 (no red, no failing test, tree clean).
**Reviewer:** FAIL. Fixed by the Builder, no argument offered:
- **S-11 condition 3 — a real defect missed by rounds 1 and 2:** the setup-link message was recorded in `channel_messages.body` with the raw token. Now the stored copy carries `[setup link]` (`Outbound.StoredText`); only the hash exists anywhere. The three existing staging rows were scrubbed (0 raw tokens left; 0 live tokens at the time). Regression tests: `TestSetupLinkTokenIsNeverStored`, `TestPGRecordOutboundStoresTheRedactedBody`.
- **S-31:** `FinishSetup` returned success when the claim itself failed, so the page could say "all set" for a booking never attempted — now an error (500). Regression: `TestFinishSetupClaimFailureIsAnError`. The two race tests now fail on any error other than the expected loss.
- **I-05 wording:** INVARIANTS now names both preflight labels (`MISSING DEPENDENCY` for tools, `MISSING SETTING` for `VERIFY_DB_URL`).

### Round 4 — 2026-10-20 (Reviewer: new Sonnet session; Verifier: Haiku)
**Verifier:** EXIT=2 INCOMPLETE — no red, no failing test, tree clean (unchanged from rounds 2–3).
**Reviewer:** FAIL.
- S-31 ×3, fixed: a WhatsApp message with an unreadable sender was dropped silently (now logged by message id, never the number); `SENDBLUE_FROM_NUMBER` invalid was silently unset (now logged without the value); `DB_MAX_CONNS` invalid silently became 8 (now logged). The Builder also fixed the same pattern where the Reviewer had not pointed (our own number in both parsers, both timestamp fallbacks, a raw `return err` in `NotifyStatus`).
- S-02 (page in `server/`): already ruled by the Arbiter as R-4 (time-boxed exception), but the ruling lived only in D-16, which the Reviewer cannot see — so the Reviewer correctly failed it. Per the workflow the Arbiter cannot overrule a FAIL; the ruling is now written into STANDARDS S-02 and S-30 as a named, time-boxed exception (two files, this branch, expires at go-live).

### Round 5 — 2026-10-20 (Reviewer: new Sonnet session; Verifier: Haiku) — at `0e75971`
**Reviewer:** **PASS.**
**Verifier:** EXIT=2 **INCOMPLETE** — no red line, no failing test (Go incl. `-race` and DB tests, web build, 131/131 web guards, mobile typecheck + export, I-01b, I-02, I-07, design checks all ok); working tree clean. Not run: I-05 (no Docker daemon, no `VERIFY_DB_URL` — env ticket, identical on base per R-5) and gitleaks (not installed — pre-existing SKIP on base). I-01a: Builder evidence on the staging branch, 3 / 0 / 0 / 0.

**Arbiter ruling for the round-5 state (Aura, 2026-10-20): CONDITIONALLY APPROVED.**
PASS + INCOMPLETE may be conditionally approved only if (a) every check that did not run is shown to be environmental, with a base comparison; (b) the conditions and their deadline are written here; (c) nothing merges to `main` until every condition is met. M3 may start on the branch.

(a) Evidence, base vs branch, same environment:
- **I-05:** base `541f5e9` and branch both `supabase: command not found` (R-5). With CLI v2.118.0 installed, both stop at the missing Docker daemon. Environmental.
- **I-02 secret scan:** gitleaks v8.21.2 (release binary) was installed and actually run, `--redact`. **Branch-only commits (`origin/main..HEAD`): 0 findings.** Base `origin/main`: 1 finding, also present on the branch because it is inherited: `stripe-access-token` at `server/payments_phase3_test.go:1109` (commit `0bf9d39`, on `main`). The literal is an `sk_test_` value with an 11-character suffix; real Stripe secret keys are far longer, so it is almost certainly a test placeholder. It is still pre-existing, not introduced here. Caveat: this clone is shallow (27 commits scanned), so the full history has not been scanned.

(b) **Conditions — deadline 2026-10-30, and in any case before any merge to `main`:**
- **C-1 · I-05 green for real:** run on a machine with a Docker daemon and supabase CLI **v2.118.0** (record the version actually used), `VERIFY_DB_URL` = the staging branch (`qxohqvkmzxokdyzrysgr`), never production. Expected: empty diff. A red here is a regression and blocks.
- **C-2 · secret scan green for real:** gitleaks run on a **full** (non-shallow) clone: zero findings in this branch's commits, and the inherited `payments_phase3_test.go:1109` finding resolved by a separate decision (replace the literal with an obviously fake value, or allowlist its fingerprint), not by skipping the scan.
- **C-3 · merge gate:** no merge to `main` until C-1 and C-2 are green **and** the App Store review freeze (PLAN §2) has ended. The go-live change also carries the R-4 page move (S-02 exception expiry).

`verify.sh` now treats a missing gitleaks like a missing CLI: `缺少相依套件 / MISSING DEPENDENCY: gitleaks` in the preflight and `INCOMPLETE` for the step (previously `SKIP`, which did not affect the exit code).

### M3.1 (approvals by text) — review round 1 — 2026-10-21 (Reviewer: Sonnet; Verifier: Haiku) at `6f33779`
**Reviewer:** FAIL ×4. Fixed, no argument offered, except where noted:
- **S-11 condition 5 — real defect:** the hook armed "the newest pending request on the task"; time and budget requests can both be pending, so a YES to one question could have approved the other. Now the lookup is by `(task, kind)`, the kind taken from the notification type; the app's partial unique index guarantees one pending per kind, so the armed request is exactly the one the question describes. Regression test `TestChatStatusHookArmsTheRequestOfTheRightKind`.
- S-31 ×2: `decideExtension` keeps the sentinel for the status code and the cause for the log (`errors.Join`); a non-`ErrNoRows` read failure is no longer reported as "not found"; the handler logs 404 causes.
- **S-21 (no migration in the diff) — Builder note for the Arbiter:** `awaiting_approval` and `pending_extension_id` already exist in `20261019120000_messaging_channel.sql` on this branch (unshipped; I-G). The Reviewer saw only this change's diff, so the finding is correct for its inputs and needs no code change.

### M3.1 — review round 2 — 2026-10-21 at `c79cc62` (Reviewer: new Sonnet session, given the branch migration file as input; Verifier: Haiku at `6f33779` + round-1 fixes)
**Reviewer:** **PASS.** Noted without failing (no standard covers it): the refactored handler now answers 500 instead of 404 when the "already resolved" read fails for a reason other than no-row. Builder's declared interpretation: a database failure IS a server error; the old 404 hid it unlogged (an S-31 defect), so this is the correction, not a regression.
**Verifier:** EXIT=1 RED — the only red is the secret scan, now actually running: the one finding is the inherited `payments_phase3_test.go:1109` on `main` (C-2); branch-only commits: 0. Everything else green; I-05 INCOMPLETE (Docker, C-1). The `git status` entries in that run were the round-1 fixes being written at the same time; committed in `c79cc62`.
**Arbiter (Aura):** _pending_ — M3.1 is under the same conditional approval as D-16 (C-1..C-3); no merge.

### M3.3 + M3.4 (several errands / ops handoff) — review round 1 — 2026-10-21 at `9575493`
**Reviewer:** FAIL ×3, all fixed in `f7f665f`, no argument offered:
- **S-11 condition 5 — real defect:** a 3DS outcome creates the task (parked until the bank confirms) but the item stayed pending; the new `Release` re-showed it with "Reply YES to book" — a second booking and charge. The pre-existing single-draft code had the same latent flaw, masked only because handoff had no release. Now a 3DS item is spent (removed from the pending set); regression test `TestThreeDSItemIsSpentNotRetried`.
- S-32 ×2: ops send and release waited on Sendblue/Meta inside the admin request; now the state change is synchronous and the message is queued (send answers 202).
**Verifier:** EXIT=1 — the only red is gitleaks (the inherited `main` finding, C-2); branch-only commits 0 (re-checked). Everything else green incl. full server suite with DB; I-05 INCOMPLETE (C-1).
**Declared interpretations (Builder) for the Arbiter:**
- I-I. The ops handoff email contains the recent message text and the linked account's email, with a last-four-digit phone hint. S-12 names phone numbers, credentials, GPS, identity data and payment details; message text and account email are what ops act on, as in every existing ops email. The full number never appears anywhere.
- I-J. Ops routes exist before an ops UI does; they are reachable with the existing ops session (curl or the ops panel). A panel page is a follow-up, not a condition.

### M3.3 + M3.4 — review rounds 2–3 — 2026-10-21
**Round 2 (at `f7f665f`):** Reviewer FAIL ×1 — S-32: the 3DS ops notice ran on the setup page's request path; fixed in `058b39e` (inside `dispatch`). Reviewer explicitly traced all six posting outcomes: no double booking, nothing beyond the summary.
**Round 3 (at `058b39e`):** Reviewer **PASS** (S-11/5, S-12, S-13, S-14, S-21, S-31, S-32, S-33, S-60 each traced). Verifier (at `9575493` + fixes): EXIT=1 — only red is the inherited gitleaks finding (C-2); branch-only 0; full server suite with DB green; I-05 INCOMPLETE (C-1).
**Arbiter (Aura):** _pending_ — same conditional approval as D-16 (C-1..C-3); I-I and I-J to confirm; no merge.
