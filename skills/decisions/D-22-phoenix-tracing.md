# D-22: Redacted chat traces go to our self-hosted Phoenix, kept 90 days

**Date:** 2026-11-03
**Status:** Accepted 2026-11-03 (Arbiter: Aura), with one change: retention is 90 days, not the proposed 30. Built on `feat/phoenix-tracing`, off unless configured; not merged, not deployed.
**Trigger:** We could not tell where the requester agent misreads messages, or why one reply took 28 s. S-12 and D-18 A-7 keep message text out of every log, so nothing showed which step was slow or what the model was shown and said.

## Decision
The server may send **redacted** conversation text to **our self-hosted Arize Phoenix**, and nowhere else, for quality and latency review. Phoenix keeps it **90 days**. Everywhere else S-12 and A-7 stand unchanged: stdout and Render logs, error strings and analytics carry no message text.

What is sent: one OpenTelemetry trace per inbound chat message, using OpenInference attributes. It contains:
- the message, the replies sent, the agent's rendered context (`AgentUserMessage`), the model's raw output, and the decoded `Proposal` after `DecodeProposal`, with `made_safe` set when decoding changed it;
- the prompt version, the model, token counts and errors;
- a timed span for each model call, each service-area check (per errand and per stop), each Google Geocoding request, each reply sent, and each relay to the task chat.

Conditions. The code must not weaken any of them:
1. **Off unless configured.** `PHOENIX_COLLECTOR_ENDPOINT` and `PHOENIX_API_KEY` must both be set. Unset, no exporter, sweeper, signal handler or deletion queueing exists.
2. **Redacted before export.** Phone numbers, email addresses and card-like digit runs (13+ digits) are redacted (`tracing.Redact`, unit-tested) in customer text, rendered context and model output. Only the `tracing.Span` text methods write text, and they always redact. Coordinates and API keys are never attributes.
3. **Identity is ids only.** `user.id` is `users.id`; `session.id` is the conversation (`channel_identities.id`); the trace is tagged with `hora.inbound_message_id` and `hora.channel` for joining to `channel_messages`. Never a phone number.
4. **Deletable.** Text is recorded only inside a message trace whose conversation is known. A trace with text therefore always names a session that account deletion can delete.
5. **Never on the critical path.** A batch processor that drops when full, a 3 s export timeout and no retries. A Phoenix outage costs traces, never a reply (tested: a hanging and a failing collector leave `Propose`'s result and latency unchanged). On SIGTERM the server drains HTTP for up to 10 s, then flushes for up to 5 s.
6. **Deleted with the account.** In its transaction, account deletion queues the account's conversation ids in `public.trace_deletions` (RLS on, no grants, no policies; `20261103120000`). A sweeper then calls Phoenix `DELETE /v1/sessions/{session_id}`, which cascades through traces and spans and needs Phoenix 13.13 or later. It retries with backoff (2 min doubling to 6 h), logs status codes only, and raises an `[ERROR]` line after 12 failures.
7. **Retention.** The Phoenix server runs with `PHOENIX_DEFAULT_RETENTION_POLICY_DAYS=90`.

## Constitution impact
- Standards added: none
- Standards modified: S-12 (amended: Phoenix is the single permitted destination for redacted message text); D-18 A-7 (amended likewise)
- Invariants added/changed: none. The new table follows CLAUDE.md Rule 3, and the four queries still return 3 / 0 / 0 / 0.

## Context and alternatives
- **Logging text to Render.** Rejected. That is exactly what S-12 forbids, and Render's retention is not ours to delete from.
- **A hosted tracing SaaS.** Rejected. It means a third processor of conversation text and a Privacy Policy change. Self-hosting keeps the data in infrastructure we run and delete from.
- **Recording trace ids per user in our DB and deleting by trace.** Rejected as more state for the same result. Phoenix deletes by session.
- **Deleting directly in Phoenix's Postgres.** Kept as the fallback if the REST route ever fails us. The REST route is versioned and cascades correctly.

## Residuals (declared)
- **Pre-13.13 Phoenix.** The route does not exist there, and Phoenix's 404 is also our "already gone" signal. So an old server would look like successful deletions. Run 13.13 or later. Phoenix documents no version endpoint, so we cannot probe for this.
- **Phoenix switched off.** An account deleted while the `PHOENIX_*` variables are unset queues nothing. Any traces from an earlier enabled period then go only by the 90-day retention.
- **Late spans.** A trace still being exported after its session was deleted would recreate it. The first attempt therefore waits 3 minutes, against a 60 s routing timeout and a 5 s batch delay.
- **Over-redaction.** A list of ten or more numbers written with spaces between them reads as a phone number. This errs toward privacy.
- **Not redacted.** Names and addresses are kept on purpose, because review needs them. They go with the account (condition 6) and with retention (condition 7).

## Evidence
- `go test ./internal/tracing` covers redaction tables, off by default, identity gating, and the delete client.
- `go test ./internal/channel/llm -run Phoenix` covers a hanging and a failing collector, with `Propose` unchanged and shutdown bounded.
- `go test ./internal/channel -run 'OneTrace|TraceDeletions'` and `go test . -run 'TraceDeletion|SweepTrace'` cover one trace per message, the queue in the deletion transaction, and sweeper retry.
- The full suite runs with `REQUIRE_DB_TESTS=1 -race -p 1`.
- Phoenix API: `DELETE /v1/sessions/{session_identifier}` takes "either a GlobalID or user-provided session_id string" and returns 204 or 404 (Phoenix REST reference, sessions).
