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
| M0 | Oct 19 | PLAN.md; `channel` package skeleton; adapter interface; migration file (not applied) | |
| M1 | Oct 20–22 | Sendblue inbound/outbound in sandbox; identity + setup page; dedupe | |
| M2 | Oct 23–25 | Draft → YES → hold → task; status updates via `notify.Create` hook | |
| M3 | Oct 26–28 | Approvals YES/NO; chat relay; multi-task; ops handoff | |
| M4 | Oct 29–31 | WhatsApp adapter on Meta test number; eval run; real-card E2E on both channels; demo | |

## 11. External dependencies (owner: Aura)

- [ ] Sendblue: plan allows outbound to users who texted first + price; webhook carries reply-to / reacted-message id; dedicated line
- [x] Meta Business portfolio created, verification submitted (2026-10-18)
- [ ] Meta developer app + WhatsApp product → test number id + temp token (set on Render, never in chat or git)
- [ ] New US number for production WhatsApp (not the supporter number)
- [ ] Baseten API key
- [ ] Privacy Policy: add Sendblue, Meta (WhatsApp), Baseten to the provider list before launch

## 12. Funnel metrics (from day one)

first inbound → draft confirmed → setup completed (card) → first task completed.
The setup step is the one to watch.

## 13. Open questions

- Dani: confirm approval timeouts and copy for budget/time requests by text.
- Does Sendblue expose threaded-reply context? If not, step 5 falls back to 6–7.
- One number for both channels, or separate? (Default: separate.)

## 14. Paused

App Store public release of the requester app; requester-side app features;
October Williamsburg market test (replaced by messaging pilot after Oct 31).
