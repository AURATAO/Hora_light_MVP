# D-28: A warm voice for customer replies on the text channel

**Date:** 2026-11-11
**Status:** Proposed. Built on `feat/warm-voice` (off main at f2384bc). Off unless `CHANNEL_VOICE=1`. Not merged, not deployed. No migration.
**Trigger:** Aura, 2026-11-11: every customer-facing reply should sound warm, friendly and professional, like a thoughtful concierge, not a form. Sonnet writes it.

## Decision
After the router decides WHAT to say, `Router.Voice` (the small step's primary, `LLM_SMALL_MODEL`, Sonnet 5.5) rewrites the free words of the reply in HO:RA's voice. It decides nothing. The layer runs in one place, `sendStamped` (`internal/channel/voice.go`), before the outbound stamp, so every reply passes through it exactly once.

1. **Whole messages are never sent to the model** (`voiceFixedReason`): supporter relays; ops' own words (`OpsSend`, `Outbound.Verbatim`); a supporter's request a YES or a 1/2 answers, and their item notice; photos; setup links; the booking summary; HELP and the opt-in reply (STOP gets no reply); scope refusals and the scope holding line; anything citing Google Maps or a web page; the supporter application link; and the copies other code reads back from the thread by their exact text (`CopyParseFailed`, `CopySupporterAlready`, `CopyLookingUp`, `CopyDiscoveryFooter`).
2. **Fixed spans are masked as ⟦n⟧ before the call** (`MaskForVoice`), so the model never sees them: any sentence with YES/NO/STOP/START/HELP/INFO/SKIP/LAST, "reply", "cancel task", the Terms, an estimate, a charge, fee, payment, hold, card or `$`; links, domains and emails; numbered list lines, summary lines (`·`, `→`) and parenthesised footers; "Want …?" offers; quoted titles; and every value filled into a `Copy*` format (`voiceFormats`, 107 formats; a test fails when a new one is not listed).
3. **The rule check** (`CheckVoice`, on the full text with the blocks put back). Every block is present unchanged and in order. Times are verbatim. Every number is kept and none is added (digits, and number words in English). Prices are kept. No link, email or phone number is added or dropped. There is no extra `?`, no new YES/NO-type keyword and no new `$`. Every capitalised name (place, person, borough, HO:RA) survives. There is no "As an AI", no `!!`, at most one `!` and at most one emoji. In English, no new commitment word (book, confirm, cancel, refund, free, guarantee, promise, charge, minutes, hours, asap). Length is at most the original plus 10% (minimum 10 characters) in English, or ×1.6 in another language.
4. **Fallback**: a model error, the model's tokens not coming back exactly once each, a failed check, or no answer within `DefaultVoiceTimeout` (**2.5 s**) sends the original copy unchanged. The log line carries the reason and latency only.
5. **Language**: nothing in the channel matched the customer's language before this. Every copy was English, and only the classifier read other languages. The model gets the customer's latest message (for tone and language only) and writes the reply in that language. Fixed blocks stay English, including the YES lines, which are what the rules read. Replies with no inbound message (status updates) are written in English.
6. **Tone guide** in one file: `internal/channel/llm/voice.md` (prompt v0.3, next to `playbooks.json`).
7. **Tracing**: a `channel.voice` span with outcome (`rewritten` / `fallback` / `fixed`), reason, latency, language, block count and character counts. It carries no text. The model's own LLM span runs under `tracing.WithoutText`: model, parameters and tokens only. The reply is already on `channel.send`, and a rejected rewrite is never kept.
8. Flags: `CHANNEL_VOICE=1` turns it on; `CHANNEL_VOICE_TIMEOUT` (Go duration, ≤ 10 s) overrides the timeout. The model is the small step's primary only, with no GLM fallback inside a 2.5 s budget.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. No table, grant or policy.

## Context and alternatives
- **Rewrite at each call site** instead of at the send: about 200 call sites, and new ones would bypass it. The one send path sees everything; the masker and the whole-message list decide what is fixed.
- **Check only, no masking**: the check catches changed numbers and dropped lines, but masking means the YES lines, links and prices never reach the model at all, and the check is a second lock.
- **Haiku** would be faster, but the brief names Sonnet. Not measured.
- **Timeout 2 s vs 2.5 s**: see Evidence. 2.5 s rarely falls back. 2 s lost a third of the replies on a slow hour.

## Evidence
- `go test ./internal/channel -run Voice` and `./internal/channel/llm -run Voice`: the rule check (dropped price, changed price, changed time, reformatted time, dropped and added numbers, number words, added URL and domain, dropped email, added question, missing and reworded YES line, blocks out of order, added keyword, dropped name and borough, too long, emoji, `!!`, "As an AI", new promise, phone), the masker, whole-message exemptions with no model call, every fallback (error, timeout, token dropped / doubled / invented, question added), the send path (sent and recorded text, customer words reach the model, span attributes with no text), and ops / approval text never reworded.
- `go run ./cmd/agenttest -voice -runs 2` (Sonnet 5.5, prompt v0.3, 41 replies built from the channel's own copy, ×2 = 82): 10 fixed (never sent), 72 to the model. **Rule check passed 63/72 (88%)**. Fallback 9/72 (12%): 8 too long, 1 a false name drop ("About" → "Roughly"; now a known starter). There were 0 model errors and 0 over the timeout. **Added latency: median 1.34 s, p90 1.74 s, p95 1.90 s, max 2.48 s.** An earlier run the same hour on prompt v0.1 was slower (median 1.92 s, p95 3.31 s, 24 of 70 over 2 s).
- 27 of the 63 passing rewrites were word for word: Sonnet leaves copy that already reads well. Every fallback seen was a sound rewrite that was too long or tripped a strict rule. None was unsafe.
- Under the length rule, acknowledging a stressed customer ("Sorry for the wait —") makes the reply too long, so it falls back every time.
- Spanish and Chinese customers got translated replies with tokens, names and numbers intact. Chinese kept the word "supporter" in English.
- `REQUIRE_DB_TESTS=1 go test -p 1 -race -count=1 ./...` with Postgres (`horatest` + `channeltest`): green. `go vet ./...` clean.
