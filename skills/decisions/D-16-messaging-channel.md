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
- **I-R. Task photos reach the messaging providers (M3.6, 2026-10-22).** A
  new data class beyond I-B's message text: after COMPLETED, the completion
  photo and the receipt photo — which can show someone's home — are sent to
  the requester's own thread through Sendblue (`media_url`, fetched by
  Sendblue server-side and stored as an attachment on the iMessage) and
  Meta (image message with caption; Meta fetches and caches the media).
  Rules: (1) only a signed link to one of OUR `task-completions` objects,
  under THIS task's own key prefix (`completions/<task>/…`), ever leaves —
  `signedTaskPhotoURL(raw, taskID, ttl)` fails closed, so a reference that
  is not ours, is another task's, or cannot be signed, is left out (the
  text still goes). "Ours" is the path shape
  plus the task prefix; `completeTask` itself does not bind the key to the
  task, so this is where another task's photo is stopped. (2) The link
  lives `taskPhotoLinkTTL` (1 hour), enough for the provider's fetch; for
  that hour it is a bearer link, and it will sit in the provider's request
  logs and fetch cache as any media link does. (3) The link is never
  stored in `channel_messages` (the body is the text) and not logged by
  us — provider error bodies are masked for URLs (JSON-escaped slashes
  included) and `token=` values before they reach an error string; a
  percent-encoded echo is not masked (judged unlikely). (4) The channel is
  asked BEFORE the copy is composed whether it can carry the media
  (`MediaCarrier`): WhatsApp carries JPEG/PNG with a caption ≤1024; a
  `.heic` or an over-long caption means the photo is not sent and the
  words promise nothing ("Purchases: $X", no second message) — the link is
  never pasted, because it would outlive its token; a PUBLIC relay
  attachment the channel cannot carry (M3.2: a PDF, a photo with a long
  note) is still pasted as a link, as before (`Outbound.MediaExpires`
  tells the two apart; the cap is counted in bytes, which errs on the
  safe side; `.webp` is no longer treated as an image anywhere, so a
  relayed `.webp` is now pasted as a link and labelled "(attachment)" —
  Meta would have refused it as an image). Sendblue has no carrier check
  and relies on the API's refusal. (5) A media send the provider REFUSES (a 4xx other than
  401/403/429 — `ProviderStatusError`) falls back to the words once (a
  receipt photo → "Receipt: $X. (The photo couldn't be sent here.)"); a
  plan-limit 400 is indistinguishable from a media 400 and costs one more
  refused call; a transport failure, 408/409, 401/403, 429 or a 5xx (the
  message may have gone through, or a retry would not help) is NOT
  retried; an asynchronous media failure after a 200 (a status webhook) is
  not seen. So a "(receipt photo follows)" already sent can be false
  whenever the receipt send fails in any of those ways — the person then
  has the amount in the first message and no photo; nothing corrects it. (6) Outside
  WhatsApp's 24h window nothing is sent (template, M4), as for every
  status. (7) The copy never says where a photo that was not sent can be
  found: the completion email carries the completion photo only, not the
  receipt (a follow-up, PLAN §13 "Open questions"); the "Your payment
  receipt is on its way by email" line is the RECEIPT email, not a photo. The "Purchases: $X" line states the
  receipt amount, not a share of the total: reimbursement is clamped to
  the approved budget plus tolerance (billing.go). Privacy Policy: §1 already says completion and receipt
  photos are shown to the requester through short-lived signed links; the
  iMessage/WhatsApp line in §1 and §5(b) must name the photos as sent via
  those providers (Aura's edit list, PLAN §11). Delivery is at-most-once
  per notification row, like the email; a repeated COMPLETED notification
  repeats the messages.

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

### M3.5 (shopping budget + auto-extend consent) — 2026-10-21
**Trigger:** Aura's test: "bring me a Russ & Daughters bagel" booked with budget 0; the supporter app gates both the receipt step and "Ask for more budget" on budget > 0, so the supporter would pay out of pocket with no way to claim. Decision: **no universal reimbursement** — the requester consents to a ceiling before money moves; the budget is that consent. The chat now asks (rule first, model as tiebreak for *whether* to ask, amount by rule only); the summary states the auto-extend window the app collects with a checkbox, and the one YES covers it ("no extra time" opts out by rule). `awaiting_budget` added to the unshipped migration's CHECK (I-G); staging branch aligned.
**Round 1 (at `3eabf34`):** Reviewer FAIL ×1 — S-11/5: the stated "15 extra minutes" was a hardcoded copy of the billing config and could drift from the booked task. Fixed `9d3e105`: the router takes `Billing.AutoExtendMinutes` from the server; zero refuses to show the summary; test ties the figure to the billing value.
**Round 2 (at `9d3e105`):** Reviewer **PASS** (budget amount from typed words only; model never sets an amount; consent stated before every YES; S-05 via `quoteTask`; S-31/S-12/S-21/S-32/S-33/S-60 traced). Verifier (at `3eabf34`): EXIT=1 — only the inherited gitleaks finding (C-2); full server suite with DB green; I-05 INCOMPLETE (C-1).
**Post-freeze follow-up (PLAN §13):** supporter app shows "Ask for a budget" at budget 0 — the safety net when the chat misreads.
**Arbiter (Aura):** _pending_ — same conditional approval as D-16.

### M3.2 (chat relay: task chat ↔ iMessage/WhatsApp) — 2026-10-21
**What:** a linked requester's non-errand message goes into the task's TalkJS room as the requester (one active task → that one; several → numbered list, message held; inline reply → that message's task); the assigned supporter's messages go to the requester's number ("Maya · Laundry pickup: …", photo attached via the new `Outbound.MediaURL`). Relay moves no money and changes no task state. `pending_relay_text` added to the unshipped migration (I-G); staging branch aligned.
**Round 1 (at `455d647`):** Reviewer **FAIL** ×5 MAJOR. Fixed in the next commit, no argument offered, except where a declared interpretation is recorded:
- **M-1 (S-11/5) — real defect:** a supporter's relayed question ("want the large one too?") could capture a bare YES meant for a pending booking/approval, or steer one ("reply YES to approve"). Fix: (a) an inline reply to a task-tagged message relays in *every* open state, never books; (b) a supporter message relayed while a YES/NO is pending carries a footer stating what a bare YES/NO still answers (the booking, with title and amount; or the supporter's own extension request). Tests `TestRelayDuringPendingYesIsDisambiguated`, `…PendingApproval…`.
- **M-3 — real defect:** the hold was cleared before the pick was delivered: a bad number lost the message, a failed post lost it, a second message overwrote the first. Fix: the hold survives a bad pick and a failed post (person told to reply the number again), a second message joins the hold, a new errand drafts (the hold is dropped — the person moved on). Test `TestHoldSurvivesBadPickAndFailedPost`.
- **M-4 — tests missing on the authorization path:** added `channel_relay_test.go` (DB-backed): `ActiveTasks` lists only open+assigned+unblocked tasks of the requester; `PostAsRequester` refuses a non-owner, closed task, no supporter, blocked pair — all *before* the TalkJS config check, proven by the error order; `relayAllowedToRequester` passes only the assigned supporter on an open task with no block, fails closed on an unknown task; the webhook entry drops the requester's own messages and a forged `custom.taskId` from another room. Adapter media payloads tested for both providers.
- **M-5 — likely real:** chat-only requesters have no TalkJS user; now `PUT /users/{email}` (idempotent) before the conversation PUT. *Still to confirm on staging with a real message* (I-05-style: evidence, not a guess).
- **M-2 (S-21) — Builder note for the Arbiter, no code change:** the Reviewer assumed the migration was already applied somewhere. It is unshipped (not on `main`, not in production; I-G). The staging *branch* carries a matching `ALTER TABLE … ADD COLUMN IF NOT EXISTS` applied through the Supabase MCP (as for `awaiting_budget` in M3.5), and the branch is reset from the migration files before any merge. **Declared interpretation I-K:** until the migration ships, the file in the repo is the source of truth and the staging branch is patched to match it; after it ships, every change is a new forward migration (S-21 as written).
- Minor, fixed: empty supporter bodies (location/sticker) not sent; non-image attachments and >1024-char captions go as text + link on WhatsApp instead of an image Meta would reject; task status checked on both directions (`open` is the only live status: enum is open/completed/cancelled); the room id must equal `task_<id>` (custom.taskId is client-settable); blocked pairs are not listed at all; TalkJS error bodies (which echo emails) no longer go into errors/logs (S-12); the relay goroutine recovers; `Router.Chat` is wired only when TalkJS REST credentials exist, otherwise the plain acknowledgement as before.
- Minor, **accepted as known limitations** (PLAN §13): no dedupe of TalkJS webhook retries (same as the existing push path; TalkJS `idempotencyKey` on the requester→supporter side is a follow-up); no TTL on a held message (purged with the account, S-20); a supporter message outside the WhatsApp 24h window is logged, not sent (template work, M4); a question about HO:RA itself with a supporter on the job goes to the supporter.
**Verifier (round 1, at `17aef29`):** EXIT=1 — reds: the inherited gitleaks finding (C-2); I-05 INCOMPLETE (docker, C-1). `go test -race` (not part of verify.sh) fails two tests **on base `853bed5` too** — `TestLiveArrivalIsExactlyOnceUnderConcurrency` and `TestPhase2bBudgetIncreaseDenied`, both a test-harness race on `gin.SetMode` (`gps_ping_source_test.go:139`, `admin_task_ops_test.go:216`); environment ticket in PLAN §11, not a regression. Server suite without `-race` green incl. DB tests.
**Round 2 (at `e0bc480`; Reviewer: new Sonnet session, given the Builder's round-1 notes as claims to verify):** **FAIL** ×3 MAJOR + 1 conditional. M-1 money capture confirmed closed; M-3/M-4 confirmed closed. Fixed, no argument offered:
- **N-1 — real defect, created by the M-1 fix:** our own prompts (approval, summary) are task-tagged outbound messages, so an inline YES swiped on "reply YES to approve" was relayed to the supporter as the word "YES" and approved nothing. **Declared interpretation I-L:** while a question is pending, a *bare* YES/NO answers our question whichever bubble it was swiped on (the relayed footer now says exactly that: "To answer Maya instead, write a sentence"); a sentence in reply to the supporter's message relays. Test `TestInlineYesOnApprovalPromptApproves`; the M-1 test updated to this rule.
- **N-2 — real defect:** an inline reply to a message about a finished task (a "thanks" on "Maya finished!") hit a permanent "try again". `inlineTarget` now requires the task to be active, otherwise the message routes normally; `PostAsRequester` wraps `channel.ErrTaskNotActive` for finished/unassigned/blocked/not-the-sender's, and the router answers "That task has finished…" and drops the hold instead of looping. Test `TestInlineReplyToFinishedTaskRoutesNormally`.
- **N-3:** the supporter's TalkJS user is now ensured too (PUT /users is a merge; only name + email sent, so nothing a client set is overwritten — N-5).
- **N-4 (conditional) — real:** an extension request arriving under a hold was not armed. `askApproval` now arms from `awaiting_task_choice`; `ResumeState` brings the hold back after the answer, with its list. Test `TestApprovalArrivingUnderAHoldIsArmedAndHoldResumes`.
- Minor, fixed: TalkJS `idempotencyKey` = inbound provider id (N-6); nil-`Chat` guard, "never mind" drops the hold, relayed text capped at 1500 chars (N-7); supporter-initiated block covered in the `ActiveTasks` test (N-8 — `usersBlockedSQL` is one `exists(...)`, so `NOT` binds to the whole predicate); sender name passed instead of the push title (N-9); the migration file now also carries idempotent `ALTER TABLE … ADD COLUMN IF NOT EXISTS` for the two columns, so a database that already ran the earlier file gets them (N-10); transport errors scrubbed of the URL, which carries the email (N-12), with a test.
- N-9 (hook wired unconditionally): `RegisterChannelWebhooks` returns at the top unless `CHANNEL_WEBHOOKS_ENABLED=1`, so production is untouched — no change.
- N-11 (relayed text is verbatim in HO:RA's voice; link policy): noted for a standard proposal, not changed — the prefix names the supporter, and ops handoff exists for abuse.
**Verifier (round 2, at `e0bc480`):** EXIT=1 — reds: inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green incl. full server suite with DB.
**Round 3 (at `0635322`; Reviewer: new Sonnet session, given the full router/approval/conversation/status/talkjs_admin/safety files as well):** **FAIL** ×2 MAJOR. N-1/N-2/N-3/N-4 confirmed closed. Fixed, no argument offered:
- **R3-1 — real defect:** the supporter's name in HO:RA's own voice ("Maya · Laundry: …") came from the TalkJS payload, a client-set field: a supporter named "HO:RA Support" could impersonate us on a trusted number. Now the name is the profile's first name, loaded server-side in the same query as the authorization check; the payload's name is not read. Test updated to assert the profile name.
- **R3-2 — real defect, and I-L withdrawn:** "a bare YES/NO answers us whichever bubble" could not tell our prompts from the supporter's relayed words (both task-tagged), so (a) the footer promised "write a sentence" and a typed sentence was re-asked; (b) "yes, approve it" swiped on our prompt was relayed and silently approved nothing; (c) a bare "yes" swiped on an old supporter bubble booked a pending draft — money on a deliberately mis-addressed YES. **Declared interpretation I-L (replaced):** *the bubble decides.* `channel_messages.relay` (new column, unshipped migration + staging ALTER) marks a supporter's relayed message; an inline reply to a relay bubble is for the supporter whatever it says (a "yes" there never books); a reply to one of our prompts or status lines goes through the state machine (a YES on "reply YES to approve" approves, a sentence on it is re-asked, as before M3.2); with nothing pending, a reply to any message about an active task names that task. The footer now says: "To answer Maya, reply to this message. A YES or NO sent on its own answers my question above." Tests `TestBubbleKindDecidesWhoGetsTheAnswer`, `TestRelayDuringPendingYesIsDisambiguated` (rewritten), PG round-trip of the flag.
- R3-3: when the list has shrunk to one, the hold is delivered before it is cleared (a failed post keeps it); no supporter left / relay switched off says so plainly ("Your supporter has finished, so I couldn't pass that on") instead of "Got it". Tests.
- R3-5: `talkjsUpsertUser` now GETs first and creates only a missing user — an existing TalkJS user is left exactly as the clients set it.
- R3-6: the migration's trailing block also re-asserts the `state` CHECK (drop-if-exists + add), so a table created from an earlier revision accepts `awaiting_task_choice`; staging already had it (verified by `pg_constraint` on 2026-10-21).
- R3-7: a handoff drops the hold (ops read the thread; the words are in it), so release never resurrects it. Test. The concurrency window (full-row `SaveConversation` across a TalkJS call) is **accepted** for the beta: one person, one thread, typing speed; recorded in PLAN §13 with the TalkJS-retry dedupe.
- R3-8: non-image attachments are labelled "(attachment)"; Sendblue gets any media type by design (iMessage renders it).
- R3-4: the TalkJS wire sequence (GET/PUT user ×2, PUT conversation, POST message with `idempotencyKey`) cannot be pointed at a test server (`talkjsAPIBase` const) — **staging evidence required before M3.2 is called done** (PLAN §15 test 3.2a/b). Open.
**Verifier (round 3, at `0635322`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Round 4 (at `1632646`; Reviewer: new Sonnet session, full channel/router files + handoff + talkjs_webhook):** **FAIL** ×2 MAJOR. R3-3/5/6/7/8 confirmed closed. Fixed, no argument offered:
- **MAJOR-1 — real, R3-1 only half closed:** the profile name is the supporter's own field too; "HO:RA" as a first name would still impersonate us. Now `DisplayName` vets what may appear in HO:RA's voice: one line, letters/numbers/`'`/`-`/`.`, ≤24 chars, nothing reading as "hora"; anything else falls back to "your supporter". Applied to the relay prefix and the task list. Test `TestDisplayNamePolicy` (newline, markup, "HO:RA Support", over-long). *Proposed standard (S-xx): a user-set name shown in HO:RA's own voice on a trusted channel is vetted at the point of display.*
- **MAJOR-2 — real, I-L failed open:** `inlineTarget` dropped the bubble kind whenever the task was inactive, the lookup errored, or the outbound row was missing, so a "yes" swiped on Maya's bubble fell through to `onConfirm` and booked. Rewritten as `bubble` + `routeReply`: a reply to a relay bubble is relayed if active, else "that task has finished" — it **never** reaches a pending YES/NO; a lookup error fails closed (the webhook retries); a bare YES/NO on an *unknown* bubble while a question is pending is asked again ("send YES or NO on its own…"), never acted on. Test `TestYesOnARelayBubbleNeverBooks` covers all three paths. **I-L stands as "the bubble decides", now with these fail-closed branches.**
- MINOR-1: held words are delivered only to a task that was on the list they were held for; a new supporter gets the new message only, and the person is told. Test.
- MINOR-2: a failed list send is logged, not returned, so the sweep cannot append the same words to the hold twice. Test.
- MINOR-3: a late YES swiped on an old prompt with nothing pending is an acknowledgement, not relayed. Test.
- MINOR-4: the hold is released on a freshly loaded row (`releaseHold`), so an approval armed during the TalkJS call is kept. Test with an arming fake.
- MINOR-5: the TalkJS webhook now also rejects signatures older than 5 minutes (replay window) — since M3.2 that endpoint can put a message on a phone. Test. **Aura to confirm in the TalkJS dashboard that identity verification is enforced** (the sender id is a client-chosen email otherwise) — added to PLAN §11.
- NIT: footer for `awaiting_budget`; `TaskForProviderMessage` scoped to the thread's address; client strings logged with `%q`.
**Verifier (round 4, at `1632646`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Round 5 (at `41bbeb5`):** **FAIL** ×3 MAJOR. R4 MAJOR-2 confirmed closed (every relay-bubble branch traced; the relay never calls `Booker`). Fixed, no argument offered, except where noted:
- **MAJOR-A — real, R4 MAJOR-1 half closed:** the name check was a one-token denylist: `H.O.R.A`, `H0RA`, fullwidth `ＨＯＲＡ`, Cyrillic look-alikes, `Admin`/`Support`/`Billing` all passed. `DisplayName` rewritten: NFKC-normalised, letters only plus space/`'`/`-` (no digits, no `.`), one script (Latin/Cyrillic mixing refused), ≤24 chars, and a confusable-folded reserved-word check (hora, admin, support, staff, official, system, billing, security, team, help). Also applied to the status lines ("X accepted…", `status.go`). Tests cover every evasion the Reviewer listed.
- **MAJOR-B — real:** a NO swiped on the "which task?" list bubble (ours, no task) was answered "Got it" while the hold stayed. `routeReply` now hands a prompt bubble to the state machine when a hold is open or the bubble names no task; `onAwaitingTaskChoice` drops the hold on NO. Test.
- **MAJOR-C — Builder note for the Arbiter, no change:** the E.164 CHECK on `channel_messages.address` exists in `20261020120000_channel_messages_address_e164.sql` (D-16 R-3, applied to the staging branch 2026-10-20); the Reviewer was given only the first migration file. Added to the round-6 inputs.
- MINOR-1: the regenerated list and the one-task delivery both consider only tasks the words were held for; a task assigned since gets the new message alone. Test extended. MINOR-2: both relay directions are wired together (TalkJS REST configured) or neither — no one-way "Got it". MINOR-3 (S-31): a sender lookup failure in the TalkJS webhook is logged and the relay to the phone skipped (fail closed); pushes unchanged. MINOR-4: a bare YES/NO with nothing pending is an acknowledgement on both paths (idle no longer relays it). MINOR-5: tests for a bubble-lookup error, NO on relay/unknown bubbles, the list bubble. NIT-1: the kept-hold-after-failure path writes a fresh row (`updateHold`). NIT-2: migration header corrected.
- Flagged, outside this diff: `approvalText` uses the app notification body — it is server-built copy (`extensions.go`), not supporter-typed text.
**Verifier (round 5, at `41bbeb5`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Round 6 (at `1acd1c0`; inputs now include the E.164 migration, extensions.go, channel_status.go):** **FAIL** ×1 MAJOR. R5 A/B/C confirmed closed (every listed evasion traced through `DisplayName`; the list bubble reaches the state machine; the CHECK exists). Fixed:
- **M-1 — real, and my round-5 note was wrong:** the approval question relayed the app's notification body, which inlines the supporter's free-text `reason` (`other: …`, up to 120 chars, newlines included) and the assignee name unvetted — supporter-typed words in HO:RA's voice on the one message a YES answers. Now the chat composes its own question from the structured request (`PendingApproval{ID, Kind, Minutes, Cents, Reason}` looked up by the status hook): vetted name, server-recorded minutes/amount, task title, and the reason shown as `Maya's note: “…”` — one line, printable, ≤120 chars, quoted. Without the structured request nothing is sent. Test `TestApprovalQuestionQuotesTheSupportersNote`. The other relayed bodies: CANCELLED carries the requester's own reason; TASK_REASSIGNED/TASK_REMOVED/TIME_CAP_WARNING are server copy.
- m-1 (prompt bubbles not matched to the pending question's own prompt): **declared, accepted for the beta** — equal to pre-M3.2 behaviour, where an unswiped "ok" with a draft pending books; a YES swiped on a stale prompt approves the currently pending request, which is what the thread says is pending. Tightening needs the prompt's extension id on the row; recorded as a follow-up in PLAN §13.
- m-2 (residual look-alikes: diacritics, small caps, more reserved words): accepted — a denylist cannot close the class; the display name is one token, the body is arbitrary supporter text anyway (N-11). Fails safe to "your supporter".
- m-3 (list shrunk to one → delivered without a confirmation): accepted — the words were held for that task among others; the alternative (asking again with a one-item list) costs a round for every finished errand. Recorded.
- m-4 (idempotency key is the pick message's id; TalkJS key length): R3-4 staging evidence, open. Key hashing deferred to that evidence.
- m-5 (replay check on the live push path): the TalkJS timestamp header is milliseconds per the TalkJS webhook docs and the repo fixtures; verified on staging when the next real message lands (3.2a). m-6: `%q` for the client-set task id in the remaining lines; the `talkjs_id` log line mirrors three pre-existing ones (unchanged). m-7 (handoff disconnects the requester→supporter direction; "talk to someone at the door" triggers a handoff): accepted for the beta — ops are in the thread and can relay; recorded. m-8: the E.164 migration is a separate forward migration (S-21), non-idempotent by design (NOT VALID avoided so it re-checks every row); inbound addresses are normalised by the adapters before `RecordInbound`. m-9: I-K/I-L live in this record; `PurgeUserTx` deletes whole rows of both tables (M3.4 test `TestPGPurgeUserRemovesOnlyThatPersonsChatData`).
**Verifier (round 6, at `1acd1c0`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green.
**Round 7 (at `d8a9f42`):** **PASS.** R6 M-1 traced closed end to end (hook → structured request → `approvalText`; the body is never read on the approval path; `quotedNote` strips controls and quotes). No BLOCKER/MAJOR. Conditions the Reviewer asked the Arbiter to attach, and the Builder's answers:
- MINOR-4 evidence (TalkJS timestamp unit; identity verification): the freshness check is now **enforced only when the relay sink exists** (`chatRelayHook != nil`) and log-only on a push-only deploy, so production pushes cannot be cut off by it; test. Identity verification stays with Aura (PLAN §11). The idempotency key is now a 32-hex SHA-256 of the provider id (no TalkJS length assumption).
- MINOR-5 evidence (relayed bodies): grep of the `notify.Create` call sites — `CANCELLED` to a requester carries the requester's own `in.Reason` (`main.go` cancel endpoint, actor = requester) or the ops team's text (`admin_task_ops.go`); `TASK_REASSIGNED`/`TASK_REMOVED` bodies are server copy (`admin_reassign.go`, `admin_tasks.go`); `TIME_CAP_WARNING` is server copy. No supporter-typed text reaches the chat outside the quoted approval note.
- MINOR-7: I-K and I-L are declared in this record (above); S-40(3) satisfied by this file.
- Fixed in the same pass (no review-relevant behaviour change): footer names the casual affirmatives the regex accepts ("A YES, OK or NO sent on its own…", MINOR-2); hook test asserts the structured request is carried (MINOR-3); status lines use the first name like the relay (NIT); `%q` on the remaining client-string log lines (NIT).
- Recorded as follow-ups (PLAN §13): MINOR-1 single approval slot — a resolution of request A clears the arm for request B (fails closed: a later YES gets "Got it"; fix: carry the extension id on `EXTENSION_RESOLVED` and clear only on match); MINOR-6 a blocked supporter's approval note still reaches the phone (same as the app's push; one line in safety); MINOR-8 an approval question can be sent while unarmable (pre-existing; check armability before sending).
**Verifier (round 7, at `d8a9f42`):** EXIT=1 — inherited gitleaks (C-2); I-05 INCOMPLETE (docker, C-1); everything else green incl. full server suite with DB.
**Arbiter (Aura):** _pending_ — M3.2 under the same conditional approval as D-16 (C-1..C-3) plus the staging evidence (R3-4) before it is called done; interpretations I-K, I-L to confirm.

### M3.6 (completion photo + receipt to the requester's chat) — review round 1 — 2026-10-22 at `03a97bc` (Reviewer: Sonnet, isolated; Verifier: with the M3.2c run)

**FAIL ×5 MAJOR.** (1) `photosOf` used `signTaskPhotoURL`, which returns
its input unchanged when the reference is not ours, storage is not
configured or signing fails — so a public-form or supporter-controlled URL
could reach Sendblue/Meta in HO:RA's voice. (2) A provider error body that
echoes the signed link would put its token in a log line
(`ProviderErrorText` masked only digit runs). (3) A media send the provider
refused lost the "done" message entirely. (4) "Purchases: $X (receipt
below)" could be false — the second send can fail, and Sendblue does not
guarantee order. (5) No decision record for a new data class going to
third parties. MINOR: tests stubbed `photosOf`; no WhatsApp/`.heic`/
"see photo"/send-failure/repeat cases; purchases without a receipt photo
were silently dropped; copy "Receipt for the purchases"; `formatCentsUSD`
untested; idempotency and photo-before-notification ordering unproven.

Fixed (all): `signedTaskPhotoURL` (fail-closed; reasons never carry the
URL) and `photosOf` uses only it, logging task id + reason; the amount is
stated even without a receipt photo ("Purchases: $X, included in the total
(receipt in your email)"), and with one, "(receipt photo follows)" — no
order promised; `ProviderErrorText` masks `https?://…` and `token=…`;
`sendWithMediaFallback` resends the words once when the media is refused
(receipt → "Receipt: $X. The photo is in your email."); WhatsApp drops a
non-image media reference instead of pasting the link; copy "Receipt: $X.";
I-R above. Tests: real `photosOf` against the DB (ours-but-unsigned /
not-ours / nothing), WhatsApp image payload with a signed query string and
the `.heic` drop, error-text masking, media refusal fallback for both
messages, purchases without photo, photo without amount, outside the 24h
window, `formatCentsUSD`, and that no stored body carries `token=`.
Ordering: the photo columns are written in the same statement as
`status='completed'` (main.go) before `notify.Create` runs — the lookup
cannot race the upload. Idempotency: at-most-once per notification row,
same as the email (declared in I-R).

### M3.6 — review round 2 — 2026-10-22 at `6239b6e` (Reviewer: new Sonnet session; Verifier: no RED, I-05 INCOMPLETE)

**FAIL ×1 MAJOR.** The WhatsApp adapter dropped a non-image media
reference at send time, after the copy had already promised "receipt
photo follows" / "see photo" — the same class as round-1 #4. MINOR: no
no-amount fallback copy; `urlRun` missed Meta's JSON-escaped `https:\/\/`;
the fallback retried on any error (not-configured, plan-limit, a lost
response — a possible duplicate); I-R overstated ("never lost", "never
logged"); `.webp` in the image list (Meta takes JPEG/PNG); "ours" was path
shape only — `completeTask` does not bind the key to the task, so another
task's key would be signed and sent; "included in the total" not always
true (reimbursement clamp); `TestPhotosOfFailsClosed` ran DDL and assumed
no storage env; test gaps.

Fixed (all): `MediaCarrier` capability — the router asks the adapter
before composing, clears what it cannot carry, and recomposes the words
("receipt in your email"), no photo-only message; `CopyReceiptPhotoOnly`
("The receipt:") / `CopyReceiptPhotoInEmail`; `urlRun` matches escaped
slashes; no retry on `ErrNotConfigured`, duplicate-on-lost-response and
async-failure cases declared in I-R(5); `.webp` removed; the task-prefix
check in `signedTaskPhotoURL` (reason "not this task's photo"); copy
"Purchases: $X (receipt photo follows / receipt in your email)"; the
photo columns moved into the live test fixture, env cleared and the
cached-client case skipped. Tests: WhatsApp `.heic` done+receipt (one
message, no promise) and the carried case; no-amount fallback; text-only
retry failing; not-configured not retried; escaped-slash/uppercase masks;
`CanCarryMedia` table; other task's key. Still untested: the positive
signing path (the signer is a package `sync.Once`; injecting it is a
refactor of frozen code — deferred) and a negative amount in
`formatCentsUSD` (unreachable: `ReceiptTotal` is set only for cents > 0).

### M3.6 — review round 3 — 2026-10-22 at `bf31d06` (Reviewer: new Sonnet session)

**FAIL ×2 MAJOR.** (1) The copy said the receipt photo was "in your
email" (three strings, a comment, and I-R) and the completion email
carries only the completion photo — a promise without the thing, the
same class as rounds 1–2. (2) The WhatsApp non-image drop was unconditional
and so broke the M3.2 relay: a relayed PDF or a photo with a long note
lost its link. MINOR: the no-retry test could not catch a regression;
nothing proved `*WhatsAppAdapter` satisfies `MediaCarrier`; the own-key
case could not tell "prefix passed" from "prefix rejected", and the test
consumed the storage `sync.Once` with empty env; the fallback still
retried on any error; I-R inaccuracies; a magic caption bound; test gaps
(caption-length drop, mixed, `.JPG`, extensionless, double-escaped echo).

Fixed (all): the copy promises nothing about where an unsent photo is —
"Purchases: $X" / "Receipt: $X. (The photo couldn't be sent here.)" /
"The receipt photo couldn't be sent here." — and I-R(7) records that the
email has the completion photo only (receipt-in-email is a follow-up).
`Outbound.MediaExpires` marks signed task photos: WhatsApp drops only
those; a public relay attachment is pasted as a link as before.
`ProviderStatusError` carries the provider's status, and the fallback
retries only on a 4xx other than 401/403/429 — not on transport errors.
`var _ MediaCarrier = (*WhatsAppAdapter)(nil)`. The receipt bound is the
measured receipt text. `urlRun` also matches `\\/` and `\u002f`. Tests:
call counts for timeout / not-configured / 401 / 429 / 500 (one call) and
400 (two); own key → "storage not configured"; `sync.Once` reset in
cleanup; relay PDF link kept vs expiring `.heic` dropped; `.JPG`,
extensionless, 1024 boundary; mixed carry; caption-length drop;
double-escaped and `\u002f` echoes.

### M3.6 — review round 4 — 2026-10-22 at `6ad1734` (Reviewer: new Sonnet session) — **PASS**

Both round-3 MAJORs closed in code. MINORs, all applied in the same
commit as the M3.2c round-4 fixes: "Your payment receipt is on its way by
email" (the RECEIPT email, not a photo); I-R(1) stale "email carries the
photos" removed; I-R(5) now says any failed receipt send leaves the
"follows" promise unmet; 408/409 excluded from the retry set; the
`looksLikeImageURL` comment moved; `pickyAdapter` delegates to the real
`WhatsAppAdapter.CanCarryMedia` (`WhatsAppCaptionMax`); the cap declared
in bytes; `urlRun` also matches `\\u002f`; `a.Send` errors asserted;
`preview_url: true` asserted for a relay link; `RelayToChat` pinned to
`MediaExpires == false`; the `.webp` relay change declared in I-R(4); the
PLAN section named.

## Addendum 2026-10-26 — the setup page moves to `app/` (R-4 done), review r1

Built: `app/src/pages/ChannelSetup.jsx` + `app/src/api/channelSetup.js`.
Same four endpoints, same proof model. `CHANNEL_SETUP_WEB_URL` (full page URL)
switches it on per environment; `GET /channel/setup` 302s there so links
already texted still work; unset, the embedded page is served as before.

**Review r1 (FAIL: 1 BLOCKER, 4 MAJOR) → fixes.**

1. **BLOCKER, and it was mine to begin with.** The first draft added a
   convenience the Go page never had: a signed-in visitor was offered "you're
   signed in as X — connect and continue" instead of typing an email and a
   code. That is a one-tap account takeover. Anyone can text HO:RA, reply YES
   and get a valid setup link of their own; send that URL to someone who
   happens to be signed in to the webapp and one tap binds the SENDER's phone
   number to the RECIPIENT's account — after which every YES the sender texts
   books a task against the recipient's card (S-11 condition 5), their task
   updates go to the sender's phone, and the binding cannot be undone short of
   deleting the account (condition 4). **Removed.** A fresh code is always
   typed; a session now only prefills the email field. S-11 condition 2 says
   the number is bound through an email-OTP session, and "fresh" is what makes
   the person holding the phone and the person holding the mailbox the same
   person. Recorded here because the lesson is general: a UX shortcut that
   skips a proof is a change to the proof model, and belongs in a decision
   record before it is written, not after a reviewer finds it.
2. (MAJOR) "Use a different email" called `supabase.auth.signOut()`, whose
   default scope is global — on a shared browser that revokes the other
   person's sessions everywhere, including their mobile app. Gone with the
   shortcut; nothing on this page signs anyone out.
3. (MAJOR) A booking that failed after the number was linked left the person
   on a view whose button re-spent the already-spent token (410 → false
   "expired") or re-verified a used OTP. Now: once linked, the token is
   discarded and the only retry is the BOOKING, on its own screen that says
   the number is connected.
4. (MAJOR) Any `/check` failure — network, CORS, 5xx — was shown as "this link
   has expired", so a misconfigured origin would send every booker to text
   again for a link that would fail identically. Now separate: 400/410 is
   "expired", unreachable is "we couldn't reach HO:RA" with a Try again that
   keeps the token (the old page had this distinction; the port had lost it).
5. (MAJOR) S-02/S-30: the exception for the two `server/` files expires at
   go-live. Kept for now because staging has no webapp build pointed at the
   staging API; deleting them is **merge condition C-4** (PLAN §11).
6. (MINOR) Stripe.js was imported at module scope, so it loaded while the
   token could still be in the URL → `@stripe/stripe-js/pure`, loaded at the
   card step. The token is now read in the first render rather than an effect,
   so the address bar is clean before paint.
7. (MINOR) 2xx was trusted without reading the body (`valid`, `linked`,
   `outcome` are now checked): an SPA rewrite or captive portal answering 200
   walked the whole flow to "you're all set" with nothing linked.
8. (MINOR) Only a 503 from `/payments/setup-intent` now skips the card step; a
   401, a 500 or a network failure used to skip it silently.
9. (MINOR) The card form's buttons stayed live while the booking ran (double
   book, or confirming an already-confirmed SetupIntent).
10. (MINOR) `CHANNEL_SETUP_WEB_URL` is validated: no scheme, or a fragment of
    its own, is logged and ignored rather than producing unclickable links or
    a redirect that eats the token.
11. (NIT) Title and `noindex` restored for the SPA route; progress bar shows 3
    of 3 on the done screen; unused parameter dropped.

Also corrected from the same review: the live-switch migration
(`20261026093000`) said "apply BEFORE the deploy"; it must be AFTER, because a
card added while the test key is still live mints a test customer the one-shot
migration has already passed. What happened on the day: deploy first, migration
nine minutes later, no traffic in between.
