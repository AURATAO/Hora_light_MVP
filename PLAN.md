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

## 2. App Store review freeze (LIFTED 2026-10-24 — v1 approved; kept for the record)

**2026-10-24 23:54 (Aura): the app passed review.** C-3 of the merge
conditions is met. The rules below no longer bind; `main` is still not
merged into until C-1/C-2 are green and staging is signed off (M5).



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
| M2 | Oct 23–25 | Draft → YES → hold → task; status updates via `notify.Create` hook | ✅ Oct 20 — booking + accepted/clock in/out/done all seen on an iPhone (staging, payments off) |
| M3 | Oct 26–28 | Approvals YES/NO; chat relay; multi-task; ops handoff | 🟡 approvals ✅, multi-task ✅, handoff ✅, budget + auto-extend consent ✅, chat relay (M3.2) ✅ PASS r7 + **staging 3.2a/3.2b seen (Oct 21 evening)**; M3.2b conversation model (D-17) ✅ **PASS round 3** — staging bagel flow owed; M3.2c Arbiter amendments (I-O defer-don't-drop, I-P version B) ✅ **PASS round 5** (6e8af2a); M3.6 completion photo + receipt ✅ **PASS round 4**; M3.2d Arbiter rulings R-1…R-3 (sent-before guard, no-with-reason, widened yes + closed "yes + message" shapes) ✅ **PASS round 5** (e25acef, minors fixed after) — staging evidence owed for M3.2b/c/d and M3.6 (Oct 22); **D-18 requester agent built behind `CHANNEL_AGENT` (off) ✅ PASS round 2** — model baseline owed (Aura, local key) |
| M4 | Oct 29–31 | WhatsApp adapter on Meta test number; eval run; real-card E2E on both channels; demo | |
| M5 | after the review freeze | Go-live: D-16 merge conditions C-1 (I-05 green on a Docker machine) / C-2 (gitleaks green on a full clone) / C-3 (freeze over); setup page moved to `app/` (D-16 R-4); Privacy Policy (Sendblue, Meta, Baseten, LLM routing of chat text, photos via iMessage/WhatsApp); production numbers (Sendblue dedicated line, WhatsApp production number + permanent token); TalkJS identity verification on production; rotate `WHATSAPP_VERIFY_TOKEN`; merge `feat/messaging-channel` to `main` (Arbiter 2026-10-22: added) | |

## 11. External dependencies (owner: Aura)

- [ ] Sendblue: plan allows outbound to users who texted first + price; webhook carries reply-to / reacted-message id; dedicated line
- [x] Meta Business portfolio created, verification submitted (2026-10-18)
- [x] Meta developer app + WhatsApp test number on staging (2026-10-20)
- [ ] WhatsApp System User permanent token (temp token dies every 24 h)
- [ ] Rotate `WHATSAPP_VERIFY_TOKEN` before prod (the GIN request log printed the staging one; prod route must not log query strings)
- [ ] New US number for production WhatsApp (not the supporter number)
- [ ] Baseten API key
- [ ] TalkJS: a **separate app** "HO:RA Staging" (its Test env) on the staging Render service — not a second key on the production app: TalkJS webhooks are per app, so a shared app would deliver production chat events to staging and vice versa (key rule, §16). Then its webhook → staging `/webhooks/talkjs` for M3 relay.
- [ ] TalkJS production: confirm whether prod runs on the app's Test or Live environment; switch to Live before launch if needed
- [x] **C-2 · gitleaks GREEN (2026-10-26).** gitleaks v8.21.2 on a FULL clone
  (unshallowed, 425 commits): **no leaks found**. The inherited
  `payments_phase3_test.go:1109` placeholder is resolved by the fingerprint
  already in `.gitleaksignore`, not by skipping the scan — proved narrow by
  putting an `sk_live_`-shaped literal in a copy of that same file, which the
  scanner still flags. Nothing to decide; the earlier red runs predate the
  allowlist entry.
- [ ] **D-16 merge conditions (deadline 2026-10-30, before any merge to main):** C-1 I-05 green on a Docker machine with CLI v2.118.0 against the staging branch; ~~C-2~~ done; C-3 ~~review freeze~~ over (app approved 2026-10-24); C-4 delete the two `server/channel_setup.*` files (above).
- [ ] **C-1 note (2026-10-26):** production's migration ledger CANNOT be
  compared to the repo by filename. 33 versions are recorded there against 41
  files here, and several differ in timestamp for the same content (e.g. repo
  `20260818140000` vs production `20260818225732`) because they were applied
  through the MCP `apply_migration`, which stamps at apply time. So I-05 has to
  be the schema-based `supabase db diff`, and a "missing version" in the ledger
  is not by itself evidence of drift.
- [ ] **Env ticket (D-16 R-5):** a verification machine with Docker + supabase CLI v2.118.0 and a `VERIFY_DB_URL` for the staging branch, so I-05 can actually run (today: INCOMPLETE everywhere, base and branch alike). Optional: stop committing `supabase/.temp/`.
- [x] **Go-live change (D-16 R-4):** setup page moved from `server/` to `app/`
  (`app/src/pages/ChannelSetup.jsx` + `app/src/api/channelSetup.js`, Tailwind,
  this app's Supabase and Stripe clients; 2026-10-26). Same endpoints, same
  proof model: a fresh email code is typed every time, and a signed-in session
  only prefills the email field — a draft that offered to connect the number to
  the signed-in account instead was a one-tap takeover and was removed in
  review (D-16 addendum 2026-10-26). Switch it
  on per environment with **`CHANNEL_SETUP_WEB_URL`** (full page URL, e.g.
  `https://core.horaapp.co/channel/setup`): new texted links point there, and
  `GET /channel/setup` on the backend 302s to it so links already sent still
  work. Unset, the embedded page is served exactly as before. The webapp
  origin must be in `CORS_ALLOW_ORIGINS` — if it is not, every texted link
  opens and every visitor sees "we couldn't reach HO:RA" with nothing in the
  server log, so check it with one real link per environment.
- [x] **Web app response headers (setup-page review r2/r3, 2026-10-26):**
  `app/vercel.json` now sends `X-Frame-Options: DENY`,
  `Content-Security-Policy: frame-ancestors 'none'`, `Referrer-Policy:
  no-referrer`, `X-Content-Type-Options: nosniff` and a `Permissions-Policy`
  that closes camera/microphone/USB/motion. Deliberately NOT a full CSP: that
  has to enumerate Stripe, TalkJS, Google Maps, Google Fonts and Supabase, and
  one omission is a blank page — its own task after go-live, with every page
  clicked through afterwards. `geolocation` and `payment` are left permitted
  (task tracking; Apple/Google Pay inside Stripe's iframe). **`vercel.json`
  takes no comments:** Vercel validates it against a strict schema and an
  explanatory `"//"` key fails the deploy ("should NOT have additional
  property"), which is how the first attempt broke. The reasoning for each
  header lives here instead.
- [ ] **Merge condition C-4 (from the setup-page review, 2026-10-26):** the
  S-02/S-30 exception for `server/channel_setup.html` + `.js` expires at
  go-live. They are kept now ONLY because staging has no webapp build pointed
  at the staging API, so the embedded page is the only way to exercise the
  chat flow there. At the merge to `main`: set `CHANNEL_SETUP_WEB_URL` on
  production, confirm a real link lands on the webapp page, then DELETE both
  files, the `/channel/setup.js` route and the redirect branch. Declared, not
  assumed — Aura to confirm.
- [ ] **Post-freeze refactor #1 (D-16 R-1):** `createTask` takes the principal as an argument; drop the synthetic context in `chatBooker.Post`.
- [ ] **TalkJS identity verification (M3.2, R4 MINOR-5):** confirm "Identity verification" is enforced for the HO:RA Staging and production TalkJS apps — the webhook's sender id is a client-chosen email otherwise, and since M3.2 that id decides whose words reach a requester's phone.
- [ ] Privacy Policy: add Sendblue, Meta (WhatsApp), Baseten to the provider list before launch — and state that chat text (the person's messages and the supporter's relayed lines) is processed by the LLM provider for routing (D-17).
- [x] **Inherited test race — FIXED 2026-10-26.** `gin.SetMode` moved to a
  `TestMain` (`server/main_testmain_test.go`) and removed from all 36 call
  sites plus one `init()`, so no test writes gin's package state while 8
  goroutines are in `postGpsPing`. **`go test -race` is now green on the whole
  of `server/` (79 s) and `internal/channel/` (4 s)** — it had been red since
  before this branch. Nothing in the product changed; the racy writes were all
  in test helpers.

## 12. Funnel metrics (from day one)

first inbound → draft confirmed → setup completed (card) → first task completed.
The setup step is the one to watch.

## 13. Open questions

- Dani: confirm approval timeouts and copy for budget/time requests by text.
- **Post-freeze mobile change:** supporter task screen shows "Ask for a budget" at budget 0 (today gated on `hasBudget`), so a misread chat task is recoverable (see incident 2026-10-21). **Laundry first (Aura 2026-10-24):** a laundry task can ask for money at $0 (laundromat / dry-cleaning fees paid up front) — done in the webapp (`TaskDetail.jsx` `canAskBudget`); **mobile `mobile/src/app/task/[id].tsx` to do after the review freeze**. The chat side (asking a budget for laundry fees when the task is drafted) is not ruled yet.
- ~~Does Sendblue expose threaded-reply context?~~ **Closed 2026-10-21 (D-17, I-N):
  the field exists and is wrong** — `reply_to.message_handle` named our most
  recent outbound message in both controlled trials, whatever bubble was
  long-pressed. Ignored on iMessage; WhatsApp `context.id` is reliable.
- **D-17 conversation model (M3.2b, 2026-10-21):** with a supporter on the
  job the person's words default to the supporter; a rule relays short
  answers to a fresh supporter question; the context classifier decides the
  rest; "for Maya" / "new task:" are the ways out; an approval request
  interrupts our own questions (budget/name too) and is not sent when it
  cannot be armed. See the decision record for principles and routing.
- **M3.2 accepted limitations (review rounds 1–3, 2026-10-21):** no dedupe of
  TalkJS webhook retries on the supporter→requester side (same as the push
  path); a held message has no TTL (purged with the account); supporter
  messages outside the WhatsApp 24h window are logged, not sent (template,
  M4); `SaveConversation` is a full-row write, so two messages typed within
  a TalkJS round-trip can overwrite each other's hold; relayed text is
  verbatim (prefixed with the profile name) — a link policy is a standard
  proposal, not built.
- **M3.2 follow-up (R6 m-1):** a YES swiped on an *old* approval prompt
  approves whatever is pending now; to pin it to that prompt's request, record
  the extension id on the outbound row and match it in `routeReply`.
- **M3.2 follow-ups (R7 minors):** (1) the approval arm is a single slot —
  when request A resolves, carry its id on `EXTENSION_RESOLVED` and clear the
  arm only if it matches (today a later YES to request B gets "Got it");
  (2) a blocked supporter's approval note still reaches the phone, as the
  app's push does — decide in safety; (3) an approval question can be sent
  while the conversation is in a state that cannot arm it (name/setup/
  budget/handoff) — check armability before sending.
- **M3.2 staging evidence still owed (R3-4):** one real requester→supporter
  message (chat-only requester, supporter who never opened the chat) and one
  supporter→requester message with a photo, on the staging number.
- **Web app has no new-chat-message notification (Aura, 2026-10-21 staging):**
  the TalkJS webhook is push-only by design (mobile gets the push; no
  `notifications` row, so the web app's list and bell never show a chat
  message; TalkJS's own offline email still goes). To decide: (a) write a
  `NEW_MESSAGE` notifications row too (the web list then shows "Sam sent you
  a message" with a link to the chat; the mobile list would show it as
  well), or (b) a web-only unread badge on the chat button polled from
  TalkJS. (a) is a server change (small, but notifications are frozen-area
  code shared with mobile); (b) is `app/` only, post-freeze.
- **M3.2c — D-17 rulings to build (Aura, 2026-10-21 23:30):** (1) I-O: an
  approval question the thread cannot take yet is deferred and sent+armed
  when it can, if still pending; (2) I-P version B: with a supporter
  message in the last 10 minutes only an explicit YES/NO answers us, casual
  affirmatives relay, with the footer and the "(Passed 'ok' to Sam…)" line.
  (3) Report the Sendblue `reply_to` defect with the Test A/B handles.
  **Built and reviewed to PASS (5 rounds, 2026-10-22, 6e8af2a; D-17 review
  log).** Staging branch has the two new columns (`approval_armed_at`,
  `approval_claimed_at`). Owed: staging evidence — a request arriving
  during a handoff / mid-booking is asked afterwards; two requests asked
  one at a time; "ok" with a supporter line in the last 10 minutes goes to
  the supporter. Arbiter ruling requested in D-17 (double-tap YES after an
  approval re-shows the draft; guard `onConfirm` the same way?).
- **M3.6 — completion photo + receipt to the requester's chat (Aura, 2026-10-21):**
  after COMPLETED, send the completion photo with the done message; if the task
  had purchases, a second message "Receipt: $XX" with the receipt photo.
  Groundwork is in M3.2 (`Outbound.MediaURL` → Sendblue `media_url`, WhatsApp
  image+caption). To settle: (a) the photo URLs today are storage-bucket URLs —
  check whether `task-completions` is public or needs a signed URL with a
  short expiry before sending to a third-party provider (PII: the photo may
  show a home); (b) WhatsApp outside the 24h window needs a template.
  **Built and reviewed to PASS (4 rounds, 2026-10-22; D-16 I-R + review
  log).** Settled: (a) signed 1-hour links to this task's own objects only,
  never stored or logged; (b) outside the window nothing is sent, as for
  every status. Owed: staging evidence — completion photo on the done
  message (iMessage), receipt photo as its own message, `.heic` on WhatsApp
  dropped with honest copy.
- **M3.6 follow-up (2026-10-22):** the completion email carries the
  completion photo only; add the receipt photo (7-day signed link, like the
  completion photo) so a chat that could not carry it can say where it is.
  Until then the chat copy promises nothing about where an unsent photo is
  (D-16 I-R(7)).
- One number for both channels, or separate? (Default: separate.)
- ~~Staging~~ decided 2026-10-19, see §16.
- **Adjustment to make after the flow is complete (Aura, 2026-10-20):** someone
  who wants three tasks at once has to go through the whole round for each one,
  which is tedious. To design: one message listing several errands → several
  drafts → one summary → one YES. (From the second task on, a linked number
  already books with just YES — no link, no code — so the remaining friction is
  the one-task-per-round chat itself.) Fits M3 "multi-task".
- **Tone (Aura, 2026-10-20):** replies are canned. "Thank you" after a
  completed task gets "Got it. Text me anytime…" — wrong register. To do, M3:
  (a) context-aware acknowledgements — after COMPLETED → "You're welcome, hope
  it went well"; after a booking → something that references the task;
  (b) let the model write the small-talk replies (greeting / thanks / other)
  from a short persona prompt with the conversation's last few turns, while
  everything that touches money or state stays rule-based copy (YES/NO,
  summary, price, link). Eval the persona on the §9 set before switching.

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

**COMPLETED verified on a phone — 2026-10-20 21:34 CEST:** the Done message
arrived after the web-app completion (photo upload worked once the branch had
its buckets). M2 is fully verified on real infrastructure.

**Staging deploys were failing (fixed 2026-10-20):** the branch's session-mode
pooler allows 15 clients; one process holds up to 16 (two pools × 8) and a
zero-downtime deploy runs two. `DB_MAX_CONNS` (new, default 8 = production
unchanged) is 3 on staging; stuck idle sessions were terminated once. See §16.

**WhatsApp test number live on staging — 2026-10-20 22:41 CEST:** Meta app +
test number (+1 555 630 2033), webhook verified, `messages` subscribed, a real
WhatsApp message from Aura's phone answered. Pitfalls hit (for the prod
setup): Render "Save only" does not redeploy; env names must match exactly
(`WHATSAPP_APP_SECRET` missing → every webhook 401 "adapter not configured",
fail-closed as designed); recipients go in API Setup → To → "Manage phone
number list", NOT "Add phone number" (that registers a sender). The temporary
access token expires every 24 h — a System User permanent token is needed
before any demo.

**Stripe test mode on staging — 2026-10-20:** own Stripe sandbox keys (not the
App Review sandbox); startup logs "Stripe initialized in test mode";
`PAYMENTS_ENFORCED=true` on staging. `APP_ENV=staging` now refuses a live key
outright (`payments.go`). Stripe webhook for staging not set yet — needed only
for the settle-after-completion path.

**M3.1 — approvals by text — 2026-10-20 (night)**
- A supporter's request for more time or budget reaches the requester's
  thread as a question ("…Reply YES to approve or NO to decline. No answer
  within 5 minutes counts as a no."); the next YES/NO answers it. YES/NO by
  rule only (money rule); anything else re-asks and decides nothing.
- **The decision is the app's own code.** `resolveExtension` (HTTP) was
  refactored into `decideExtension(ctx, taskID, extID, requesterUID,
  decision)` — a pure function taking the principal — and the handler now
  wraps it. The chat calls the same function. No synthetic request context,
  so S-11 needed no further amendment for this; all existing DB-backed
  extension tests (approved / denied / expired / authorization / double
  pending / audit trail) pass unchanged against the refactor.
- Priority: the approval clock beats a draft being confirmed — the question
  interrupts, the draft is kept and re-shown afterwards ("Back to your new
  request:"). Not armed mid-booking, mid-setup or in handoff.
- Resolved elsewhere (app tap, timeout) clears the question; only the timeout
  is told in the chat (the person's own answer was confirmed when given).
- `pending_extension_id` (existing column) now round-trips.
- Tests: 9 in `approval_test.go` (incl. ambiguous replies decide nothing, a
  stale YES resolves nothing, late YES on an expired request), 1 DB test.
- Not yet verified on a phone: needs a supporter to request time/budget on
  the web app against staging.

**M3.3 — several errands, one YES — 2026-10-21 (night, unattended)**
- A message asking for several errands is split by the model into the
  person's own words per errand (max 5; model down → one errand), each
  parsed and priced on its own; one numbered summary with a total; one
  rule-matched YES books them in order. The first that does not book stops
  the run; it and the rest stay pending ("Booked 1 of 3 so far. …"); a later
  YES retries exactly the unbooked ones. A 3DS item (task exists, parked) is
  spent, never re-offered. A part no draft could be made from is said in the
  summary, not dropped. First-timers are asked their name once for all.
- Stored drafts stay backward compatible (`more` is additive).

**M3.4 — ops handoff — 2026-10-21 (night, unattended)**
- Triggers, rule-based only: an explicit ask for a person (en + zh), two
  messages in a row not understood, 3DS. Ops get an email with the recent
  thread, channel, last-four hint, linked account email and identity id —
  never the number. While held: every message forwarded, holding line back,
  nothing booked/priced/approved. If ops cannot be told, the person is not
  promised a teammate and the message is retried.
- Ops routes (dualAuth + requireOpsAdmin): list handoffs, read a thread,
  send (queued), release (resumes a kept draft). Audit rows carry identity
  id + actor, never text or number. No ops UI yet: curl / the ops panel can
  call them; a panel page is a follow-up.
- Review: round 1 FAIL (3DS item re-offered after release — a second charge;
  ops send/release blocked on the provider) → `f7f665f`; round 2 FAIL (3DS
  ops notice on the request path) → `058b39e`; **round 3 PASS.**

**Incident, 2026-10-21 (Aura's test): "bring me a Russ & Daughters bagel"
parsed as `delivery`, budget 0 → the supporter app shows no receipt step and
no "Ask for more budget" (both gated on budget > 0), so the supporter would
pay out of pocket with no way to claim.** Root cause is not the category:
money follows the *budget*, and the chat never asks for one (M2 known gap).
Decision (CTO view): NO universal reimbursement — the requester must agree
to a ceiling before money moves (money rule); the budget IS that consent.
Fix in two layers:
1. **Chat (M3.5, done 2026-10-21, review PASS round 2):** after parsing, a second rule+model check "will the
   supporter have to pay for something?" (purchase words, food/items, shops;
   model as tiebreak). If yes and no amount was given → ask in chat "Will
   your supporter be buying this? About how much should I set aside?" →
   amount parsed by rule (`$20`, `20 dollars`, `twenty`) → shopping budget on
   the task, shown in the summary, covered by the hold. An amount in the
   original message ("budget 60") is taken directly.
   Also in M3.5 (Aura, 2026-10-21): **auto-extend consent.** The app has a
   checkbox (default on); the chat sent nothing and the server defaulted to
   on — consent the person never saw. Now the summary states it ("If it runs
   a little over, up to 15 extra minutes are OK without asking") and the one
   YES covers it; "no extra time" / "ask me first" (rule-matched) turns it
   off and re-shows the summary; the value is sent explicitly at posting.
   No extra question: every added question costs completions; the line is
   the chat's checkbox.
2. **Supporter app (post-freeze):** show "Ask for a budget" even at 0 — the
   server already accepts it, and the requester now approves by YES in the
   chat (M3.1). This is the safety net when the chat misreads.

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
- `DB_MAX_CONNS=3` on the Render service: the branch pooler caps at 15 sessions
  and a deploy runs two processes. If a deploy fails with `EMAXCONNSESSION`,
  terminate idle `Supavisor` sessions (`pg_terminate_backend`) and redeploy.
- Storage buckets are NOT copied to a branch: `avatars` (public) and
  `task-completions` (private) were created by hand on this one.
- Tear down after Oct 31 if unused: delete the Render service and the branch.

## 13b. Dispatch automation (raised 2026-10-25 00:00, Aura) — not yet ruled

Today a new task tells only ops ("New task posted" email); supporters see
it only by opening Earn, so Aura dispatches by hand and "immediate" rests
on her reacting. Proposed, each stage shippable alone:

1. **Broadcast** (~1 day): on post, push + an iMessage/SMS from our line
   to every verified supporter ("New task near X, N min, $Y. Open HO:RA
   to accept."); first accept wins (D-10 handles the race); ops are
   notified only if nobody accepts within N minutes.
2. **Availability** (~1 day, mobile): an "I'm available" toggle with an
   auto-off; the broadcast goes only to supporters who have it on.
3. **Tiers** (~½ day): trusted supporters first, everyone after M minutes.

Open for the Arbiter: SMS needs supporters' numbers linked (a setup
link each) or push only; N (minutes before ops are pinged). Sequenced
after the agent work.

## 14. Paused

App Store public release of the requester app; requester-side app features;
October Williamsburg market test (replaced by messaging pilot after Oct 31).
