# PLAN — Messaging entry point (v2)

Status: **active** · Branch: `feat/messaging-channel` · Written 2026-10-18 · Target: 2026-10-31

This file is the dashboard for the messaging work. Keep it current: when a
milestone lands, tick it and add the commit hash. It is checked in so it can't
go missing again (the previous PLAN.md never reached git).

---

## 1. Goal

Requesters post and manage tasks by texting HO:RA over **iMessage** (Sendblue)
or **WhatsApp** (Meta Cloud API). Supporters keep the native app, slimmed to a
supporter dashboard.

**Principle:** business rules do not change. Pricing, holds, settlement,
caps, cancellation policy, platform fee and payouts are reused exactly as they
are in production. What's new is the entry point and the conversation around it.

Architecture diagrams: https://claude.ai/artifact/SjaN4gT57zcfAnGm9HD1cV

## 2. App Store review freeze (in force until v1 review completes)

The reviewer tests against production. Until review is done:

- All work in this plan lives on `feat/messaging-channel` (or branches off it).
  **Nothing merges to `main`** — `main` auto-deploys `server/` to Render.
- Do not change existing routes, billing, `ai_task_parser.go`, or the review
  sandbox account. Do not touch `REVIEW_ACCOUNT_*` on Render.
- Additive migrations (new tables only) may be applied to prod if a dev test
  needs them; nothing that alters an existing table.

## 3. Scope

**In (v1, Oct 31):**
- Inbound + outbound text on iMessage (Sendblue) and WhatsApp (Meta test number first)
- Phone-number identity + one-page setup (name, email, terms, card with 3DS)
- AI draft → explicit YES → task posted with the existing pre-auth hold
- Status updates to the requester thread (accepted, arrived, completed, receipt)
- Budget / time approval by explicit YES/NO with the existing timeouts
- Chat relay between supporter app and requester thread, numbers hidden
- Multi-task disambiguation and human handoff to ops
- Funnel instrumentation

**Out (v1):** general assistant Q&A, voice, group chats, proactive suggestions,
editing a posted task by chat (handoff to ops instead), masked phone calls
(v1.1), supporter-app UI slimming (after review, separate PR).

## 4. Architecture

```
iMessage ─ Sendblue ─┐                         ┌─ notify.Create ─ email / push
WhatsApp ─ Meta API ─┤→ webhook → router → … → │  (existing fan-out)
                     │   (server/internal/channel)  └─ + channel.Send (new)
                     └─ adapters: sendblue.go, whatsapp.go
```

- New package `server/internal/channel`: one `Adapter` interface
  (`ParseInbound`, `Send`, `VerifySignature`), two implementations.
  Business code sees only `channel.Inbound` / `channel.Outbound` events.
- **Outbound hook:** `notify.Create` is already the single fan-out point
  (DB row → email → `SendPush`). Add `channel.Send` beside `SendPush`, same
  best-effort goroutine pattern (S-32). No caller changes.
- **LLM behind an interface** (`channel/llm`): `ParseTask`, `ClassifyIntent`.
  Implementations: current Anthropic call (reference) and Baseten
  `zai-org/GLM-5.3-Flash` (OpenAI-compatible). Chosen by env var, decided by
  the eval set (§9), never by feel.

## 5. Data model (new tables only)

| Table | Purpose |
|---|---|
| `channel_identities` | `user_id`, `channel` (imessage/whatsapp/sms), `address` (E.164), `verified_at`. Unique `(channel, address)`. |
| `channel_messages` | Every inbound/outbound message: provider message id (**unique → dedupe**), direction, `task_id` it was attributed to, body. Outbound id → task mapping powers reply-to routing. |
| `channel_conversations` | Per identity: state (`idle`, `drafting`, `awaiting_confirm`, `awaiting_setup`, `awaiting_approval`, `awaiting_task_choice`, `handoff`), pending draft JSON, pending approval id, `last_inbound_at` (WhatsApp 24h window). |
| `channel_setup_tokens` | One-time setup link: token hash, identity id, `expires_at` (30 min), `used_at`. |

Rule 1 of CLAUDE.md applies: all access through Go. RLS enabled, no policies,
no client grants on every new table — and run the four invariant queries after
the migration.

## 6. Inbound router (order is the logic)

1. Verify provider signature → reject if bad.
2. Insert into `channel_messages` on provider id; conflict → ACK and stop (idempotent).
3. Resolve identity by `(channel, address)`. Unknown → create pending identity,
   send setup link, stop.
4. Pending approval and reply is explicit YES/NO → resolve via existing
   extension endpoint logic. Not explicit → re-ask; the timeout default still runs.
5. Native reply/quote to an outbound message → that message's `task_id`.
6. Exactly one active task → relay to that task's chat.
7. Several active tasks → name/keyword match only if unambiguous; otherwise
   ask with a numbered list. Never guess.
8. No active task → LLM intent: `new_task` → draft flow; `status`/`cancel` →
   deterministic handlers; anything else → handoff to ops with the thread.

**Money rule:** any step that moves or reserves money requires an explicit
YES matched by the router, never an LLM interpretation.

## 7. Outbound format

- Relayed chat: `Maya · Laundry pickup: <text>`
- Status: short, one per event; include the live-tracking link on accept.
- WhatsApp outside the 24h window → approved template only. Templates needed:
  task reminder, approval request, receipt, balance due.

## 8. Identity & security

- `profiles.phone` is free text and unverified — **never** match accounts on
  it (account-takeover risk). Linking an existing account = email OTP on the
  setup page; new account = setup page creates it.
- Sendblue webhook secret and Meta `X-Hub-Signature-256` (app secret) verified
  on every request.
- No message bodies or phone numbers in logs; log message ids only.
- Setup tokens: single use, 30-minute expiry, stored hashed.
- **Existing code:** `server/whatsapp_webhook.go` is a live stub on prod — no
  signature check, and it logs sender number + message text. Replace it with
  the channel adapter on this branch; until then it receives nothing (no
  webhook is configured at Meta).

## 9. Eval set

`server/internal/channel/testdata/eval/`: ~50 real-shaped requester messages
from Traction 3 (anonymized) with expected task JSON + intent. Run against
each model; record accuracy, p50/p95 latency, JSON validity. GLM-5.3 Flash has
reasoning on by default — measure latency with it on and off.

## 10. Milestones

| # | Dates | Deliverable | Done |
|---|---|---|---|
| M0 | Oct 19 | PLAN.md; `channel` package skeleton; adapter interface; migration file (not applied) | ✅ see §15 |
| M1 | Oct 20–22 | Sendblue inbound/outbound in sandbox; identity + setup page; dedupe | ✅ help-first flow booked a real task from an iPhone, Oct 20 — §15 |
| M2 | Oct 23–25 | Draft → YES → hold → task; status updates via `notify.Create` hook | 🟡 first booking proven on a phone (Oct 20); status updates built, phone test pending |
| M3 | Oct 26–28 | Approvals YES/NO; chat relay; multi-task; ops handoff | |
| M4 | Oct 29–31 | WhatsApp adapter on Meta test number; eval run; real-card E2E on both channels; demo | |

## 11. External dependencies (owner: Aura)

- [ ] Sendblue: plan allows outbound to users who texted first + price; webhook carries reply-to / reacted-message id; dedicated line
- [x] Meta Business portfolio created, verification submitted (2026-10-18)
- [ ] Meta developer app + WhatsApp product → test number id + temp token (set on Render, never in chat or git)
- [ ] New US number for production WhatsApp (not the supporter number)
- [ ] Baseten API key
- [ ] TalkJS test-mode app id + secret on the staging Render service (M3 chat relay cannot be tested without it)
- [ ] Privacy Policy: add Sendblue, Meta (WhatsApp), Baseten to the provider list before launch

## 12. Funnel metrics (from day one)

first inbound → draft confirmed → setup completed (card) → first task completed.
The setup step is the one to watch.

## 13. Open questions

- Dani: confirm approval timeouts and copy for budget/time requests by text.
- Does Sendblue expose threaded-reply context? If not, step 5 falls back to 6–7.
- One number for both channels, or separate? (Default: separate.)
- ~~Staging~~ decided 2026-10-19, see §16.
- **Adjustment to make after the flow is complete (Aura, 2026-10-20):** someone
  who wants three tasks at once has to go through the whole round for each one,
  which is tedious. To design: one message listing several errands → several
  drafts → one summary → one YES. (From the second task on, a linked number
  already books with just YES — no link, no code — so the remaining friction is
  the one-task-per-round chat itself.) Fits M3 "multi-task".

## 15. Progress log

**M0 — 2026-10-19**
- `server/internal/channel`: `Adapter` interface, `Inbound`/`Outbound`,
  E.164 normalization + log redaction, `WebhookHandler` (verify → parse →
  dedupe → 200 → route async), `PGStore`, `LogRouter` (records + logs ids only).
- Sendblue adapter: secret-header check (fails closed), receive parsing incl.
  `reply_to`, drops outbound echoes and group threads, `send-message` with
  inline reply. Webhook auth **confirmed** 2026-10-19: raw signing secret in
  the `sb-signing-secret` header.
- WhatsApp adapter: `X-Hub-Signature-256` HMAC check, subscribe handshake,
  text / button / interactive replies with `context.id`, send with context.
- `channel/llm`: `Parser` interface, `TaskDraft` (same shape as
  `/tasks/ai-parse`), OpenAI-compatible client for Baseten. The system prompt
  is passed in; it moves out of `ai_task_parser.go` into a shared package
  after the review freeze so app and messaging use one prompt.
- Routes `/webhooks/channel/{sendblue,whatsapp}` in `server/channel_webhooks.go`,
  mounted **only** when `CHANNEL_WEBHOOKS_ENABLED=1` — inert on prod. The
  legacy `/webhooks/whatsapp` stub is untouched.
- Migration `20261019120000_messaging_channel.sql` (4 tables): applied to a
  throwaway local Postgres with stub FK tables — RLS on all four, zero client
  grants, E.164 CHECK rejects bare numbers, `(channel, provider_message_id)`
  dedupe holds. **Not applied to Supabase.**
- Tests: 23 in `internal/channel` + `llm` (signatures, fail-closed, parsing,
  dedupe on retries, 500-on-store-failure, unprocessed-on-route-failure).
  `go build`, `go vet`, `go test ./...` green.

**Env vars introduced** (none required until the flag is on):
`CHANNEL_WEBHOOKS_ENABLED`, `SENDBLUE_FROM_NUMBER`, `SENDBLUE_WEBHOOK_SECRET`,
`SENDBLUE_WEBHOOK_SECRET_HEADER`, `SENDBLUE_BASE_URL`, `WHATSAPP_APP_SECRET`,
`WHATSAPP_GRAPH_VERSION`. Already set on Render: `SENDBLUE_API_KEY`,
`SENDBLUE_API_SECRET`, `BASETEN_API_KEY`.

**First real message — 2026-10-19 22:02 UTC (staging):** iPhone → Sendblue
shared line → `POST /webhooks/channel/sendblue` 200 in 295 ms → verified →
parsed (service iMessage) → stored in `channel_messages` → routed →
`processed_at` set. Log line carries the message id and a redacted number only.
M0 is proven end to end on real infrastructure.

**M1 — 2026-10-20 (night, unattended; awaiting Aura's phone test)**
- `SetupRouter` replaces `LogRouter`: unknown number → one setup link (no
  second link within 2 min, "already sent" reply instead); linked number →
  acknowledgement (M2 replaces it with drafting via the `Linked` hook). Every
  reply is sent on the inbound channel and recorded as an outbound row.
- Identity store (`identity.go`): upsert by (channel, E.164), attach inbound
  rows, conversation `last_inbound_at` (monotonic), setup tokens = 32 random
  bytes, SHA-256 stored, 30 min, single use. `ConsumeSetupTokenAndLink` is one
  transaction: consume → link (refuses a number already linked to another
  account, rolls back so the token survives) → retire older links.
- Setup page `GET /channel/setup` (Go-served, `channel_setup.html/.js`
  embedded): link check → email → 6-digit Supabase OTP → name + terms
  (`PATCH /profile`) → `POST /channel/setup/link` (dualAuth, Bearer) → card via
  existing `POST /payments/setup-intent` + Stripe.js, skipped with a note when
  payments are off (staging today). Welcome iMessage on link. Token removed
  from the address bar on load; `Referrer-Policy: no-referrer`, strict CSP,
  supabase-js pinned with SRI. `profiles.phone` never used.
- Tests: 30 in `internal/channel` incl. 3 against real Postgres
  (`CHANNEL_TEST_DATABASE_URL`, throwaway DB only): lifecycle, expiry,
  takeover refused, 8 concurrent link attempts → exactly 1 wins. Page rendered
  at 390 px in headless Chromium against a mock backend: no console errors.
- **Not yet verified on a phone:** Sendblue outbound (`/api/send-message`),
  and whether the staging branch's Supabase Auth email carries a 6-digit code
  (if it arrives as a magic link only, the branch's email template needs
  `{{ .Token }}`).

**Flow change — 2026-10-20 00:23 (Aura's decision): help first, leave Messages once.**
A new number no longer gets a link on its first message. The chat greets,
drafts and prices the request, asks the first name in the chat, and states
the Terms in the summary. The person leaves Messages exactly once — at their
first YES — on a page that verifies email, saves a card and books. Every later
task stays in Messages. Diagram updated (artifact §03).

**M1 rework + M2 core — 2026-10-20 (night, unattended)**
- `Router` (state machine, `router.go`): idle → greeting/question/request
  (LLM intent, local fallback) → draft + quote → `awaiting_name` (first-timers
  only) → `awaiting_confirm` → YES: linked = post now, not linked = the one
  link (`awaiting_setup`) → page finishes → post. NO cancels; any other text
  re-drafts with everything said so far. YES/NO matched by rule, never the model.
- **Double-booking guard:** `ClaimForPosting` atomically moves the
  conversation to a posting state before anything is posted; parallel YESes
  or the page racing a YES book once. A claim abandoned by a crash is
  recoverable after 3 minutes (routing times out at 60 s).
- `chatBooker` (`server/channel_booker.go`): quote = `quoteTask` (same as
  `/tasks/estimate`); post = the real `createTask` handler run in-process
  with the linked user's id, so balance/card/decline/3DS/PAYMENTS_ENFORCED
  behave exactly as in the app. Outcomes map to chat replies; decline keeps
  the draft for a retry; 3DS hands off to a human (gap, see below).
- Chat name + Terms consent are copied onto `profiles` at link (blanks only).
- Page: email → code → link → card (or skipped when payments are off) →
  `POST /channel/setup/finish` books; shows what is being booked.
- Model: Baseten `zai-org/GLM-5.3-Flash` by default (`LLM_*` env to swap;
  `LLM_DISABLE_THINKING=1` to test latency). Uses the app's existing
  extraction prompt (`aiTaskParserSystemPrompt`, read-only).
- Migration file gained `awaiting_name`, `pending_name`, `terms_accepted_at`
  (unshipped file edited; staging branch altered to match).
- Tests: 43 in `internal/channel` + 2 in `server` (booker mapping), all green
  with `-race`, DB tests on a throwaway Postgres incl. 8-way claim race.

**First real booking — 2026-10-20 18:18 UTC (staging, Aura's iPhone):** new
number → help → draft → name → summary → YES → page (email code via the
staging Postmark server → link) → booked. Task `open` in the branch DB,
conversation back to `idle`, number linked and verified, 12 messages, none
left unprocessed. Fix on the way: the Supabase branch inherited main's custom
SMTP settings without the password (535 from Postmark); staging now uses its
own Postmark server and token (§16 key rule).

**M2 status updates — 2026-10-20**
- `notify.ChatHook` (one nil-by-default variable in `internal/notify`): every
  `notify.Create` also hands the event to the chat, alongside the in-app row,
  email and push — never instead of them. Set only by
  `RegisterChannelWebhooks`, so production is unchanged until the flag is on.
- `server/channel_status.go`: sends only when the recipient is the task's
  **requester** (supporter-side notifications for the same task never reach a
  chat), only for users with a linked number, on its own goroutine.
- `channel/status.go`: chat copy for accepted, arrived, clock in/out, done
  (time + total), cancelled, reassigned, removed, nearing the estimate. Goes to
  the thread the task was booked from (the "Booked" message carries the task
  id), else the most recent number; recorded against the task so M3 can route
  replies. WhatsApp outside 24 h is skipped (template needed, M4).
- **Deliberately not sent yet:** anything that asks for an action in the app —
  more time/budget approvals, time cap reached, balance due — because "do it in
  the app" is a dead end for a chat-only customer. Email and push still carry
  them; M3 adds YES/NO by text for them. Receipt stays email-only (done
  message states the total).
- Tests: 5 in `channel/status_test.go`, 1 DB test (booking thread wins over
  most recent), 1 in `server` (requester-only guard). All green with `-race`.

**Status updates verified on a phone — 2026-10-20 21:07 CEST (Aura):**
accepted ✅, clock in ✅, clock out ✅ arrived on the iPhone from the web app
(local, pointed at staging) acting as supporter. Found and fixed in the same
session:
- **Double-booking bug (real, not a flaky test):** a second YES that loaded
  the conversation before the first YES finished booking re-saved the old
  `awaiting_confirm` + draft over the finished `idle`, then claimed and booked
  again. The consent write on YES is now `AcceptTerms` (touches only
  `terms_accepted_at`), never a full save. Caught by `TestParallelYesBooksOnce`
  under `-race`; 5/5 green after.
- A bare YES/NO with nothing pending never starts a draft (it was being parsed
  as an errand).
- "On my way" now texts the chat customer (`ON_MY_WAY`, chat-only: app users
  get the live map at that moment, and that is unchanged).
- A linked customer saying "good"/"thanks"/"hi" after an update gets a short
  acknowledgement, not the welcome pitch (Aura's point 5).
- Name + Terms consent from the chat were not landing on `profiles`
  (FK: `profiles.id` must be the user id) — fixed.
- Staging gaps closed: the Supabase branch had **no storage buckets**
  (branches don't copy storage) so completion photos 404'd — `avatars` and
  `task-completions` created on the branch; `CORS_ALLOW_ORIGINS` and
  `COOKIE_SECURE=true` set on the Render service so a local web app can log
  in against it; `AvatarUploader` now reads `VITE_API_BASE_URL` too.
- Not verifiable on staging yet: chat relay (TalkJS keys are prod-only — a
  TalkJS test app is an Aura item, §11) and `COMPLETED` (photo retest pending).

**Known gaps (next):** shopping budget is not asked in chat (grocery tasks
post with no budget); 3DS in chat goes to a human; returning customer who
needs a new card re-verifies email on the page; status updates to the chat
(accepted / arrived / done) are M2's next piece; `created_via` uses
`ai_parse` until a `message` value can be added after the freeze.

## 16. Staging

- **Render:** `hora-staging-messaging` (srv-dbb8hrom7kps73flstl0), Starter,
  auto-deploys `feat/messaging-channel`. https://hora-staging-messaging.onrender.com
  No root directory (the connector can't set one): build `cd server && go build -o app .`,
  start `cd server && ./app`, so every commit to the branch redeploys it.
- **Supabase:** dev branch `staging-messaging` (ref `qxohqvkmzxokdyzrysgr`) of
  the main project — S-03 holds, it is the same project. No production data.
  All 33 migrations + `20261019120000_messaging_channel` applied; the four
  CLAUDE.md Rule 3 queries return 3 / 0 / 0 / 0.
- **Env set by Claude:** `CHANNEL_WEBHOOKS_ENABLED=1`, `APP_ENV=staging`,
  `SUPABASE_PROJECT_URL`, `PAYMENTS_ENFORCED=false`, and placeholder
  `GOOGLE_CLIENT_ID/SECRET` (Google sign-in is not used on staging).
- **Env set by Aura (secrets):** `SUPABASE_DB_URL` (branch), `SENDBLUE_API_KEY`,
  `SENDBLUE_API_SECRET`, `SENDBLUE_FROM_NUMBER`, `SENDBLUE_WEBHOOK_SECRET`,
  `BASETEN_API_KEY`. Stripe **test** keys added at M2.
- Sendblue receive webhook → `https://hora-staging-messaging.onrender.com/webhooks/channel/sendblue`.
- **Keys (rule, 2026-10-20): staging never reuses a production secret.**
  Must be separate (money / real data): DB password and Supabase secret key
  (branch has its own ✅), Stripe (**test mode only**). Should be separate (a
  staging accident would hit prod): Postmark — own server "HO:RA Staging",
  its Server API Token as both SMTP username and password on the branch ✅;
  Baseten — own `hora-staging` key (to do, swap `BASETEN_API_KEY` on Render).
  Shared for now: Sendblue sandbox line (prod doesn't use it; prod gets its own
  keys with the paid line). Note: a Supabase branch inherits main's custom SMTP
  host/user but not the password — set it per branch.
- Tear down after Oct 31 if unused: delete the Render service and the branch.

## 14. Paused

App Store public release of the requester app; requester-side app features;
October Williamsburg market test (replaced by messaging pilot after Oct 31).
