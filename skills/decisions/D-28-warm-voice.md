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

## Amendment 2026-11-11 (same day) — warm copy, and the layer only when needed
Aura, after the first eval: the English rewrites were light and cost ~1.3 s on every reply.
1. **Warm English belongs in the copy.** `go run ./cmd/agenttest -voice-copy` asks Sonnet once, offline, under voice.md, for warmer wording of each customer-facing `Copy*` template in copy.go. Each template is masked as at runtime (`MaskTemplateForVoice`: format verbs too) and checked by the same `CheckVoice`. Results go to `D-28-copy-proposals.md` (+ `.json`), grouped by situation. **They are not applied** until reviewed. Not proposed: `CopyParseFailed` (matched by exact text), `CopyOptInWelcome`, `CopyCancelHint`, summary lines, supporter-facing lines, and fragments glued into other sentences. The other three read-back copies live outside copy.go. `-copy-rerender` rebuilds the file from the JSON with no model call.
2. **The runtime layer runs only when triggered** (`voiceTriggerFor`). Otherwise the reply is sent as written, with no call and no added latency (`skipped`).
   - **language**: the customer's latest message is not English, by rule (`DetectLanguage`: script, then stop-words for es/fr/it/pt/de; a lone "gracias" counts; YES, numbers and addresses say nothing). A status update answers no message, so it takes the language (never the mood) of the newest inbound in the thread that says anything (`withThreadLanguage`, one thread read, only with the voice on).
   - **stressed**: by rule (`StressedBy`: "??"/"?!", still waiting, anger and complaint words, shouting, and a few Chinese/Spanish phrases). Otherwise from the agent's own escalation of this message (`dispute`, `repeat_confusion`) when the agent was asked anyway. The intent classifier has no sentiment output, and none was added: that would change its prompt and its routing evals.
   - Logged as `voice … trigger= stress_by= lang= outcome= reason=`; on the `channel.voice` span as `hora.voice.trigger`, `hora.voice.stress_by`, `hora.voice.language`.
3. **Stressed**: one opening acknowledgement of up to 25 characters (`voiceAckChars`) beyond the length budget. It holds no number, no promise and no question, the rest still fits the budget, and the reply has at most one "sorry".
4. **Promise words**: "as soon as" added ("right away" and "immediately" were already there).
5. **Chinese script**: Traditional is told apart from Simplified by a table of differing characters (`tradSimp`; characters shared by both scripts are left out). The model is told the script, and the check refuses the other script (`script_mismatch`). 你 is required unless the customer wrote 您 (`formality`). Script unclear ("你好" alone): the model is told to mirror the customer, and the script is not checked.
6. **New checks** found by reading the pairs, not by the pass rate: `garbled` (mojibake: one Spanish reply came back as "ÂżEn quĂ©…"), `untranslated` (English words left outside the fixed blocks: "va en camino for"), `name_added` ("de Nueva York"), `gendered` (a supporter's gender is never known: 他/她, él/ella, he/she). Two masking bugs were fixed. A value at the end of a format line swallowed a copy glued on after it. Where two formats matched the same words, the shorter one claimed a function word as a value; the format with more literal text now wins.

### Evidence (amendment)
- `agenttest -voice -runs 2` (prompt v0.5, 57 replies × 2 = 114): **46 through the layer** (28 language, 14 stressed, 4 both), 68 sent as written (English, calm). **Rule check 46/46**, fallback 0/46, 0 over 2.5 s. **Added latency, on those 46 only: median 1.41 s, p90 1.85 s, p95 1.92 s, max 2.23 s.** Every other reply: 0. The run before (same rules, before the last masking fix) had 4 of 46 over 2.5 s, with a max of 9.99 s.
- Known and accepted: fixed English blocks (money sentences, YES/reply lines) stay English inside a translated reply ("¿Tu supporter va a comprar algo para esto? About how much should I set aside, e.g. $20? Reply "nothing"…"). Spanish keeps the word "supporter".
- `-voice-copy`: 167 templates. 42 proposed, 53 kept by the model, 20 refused by the check (mostly too long; two added a question), 34 with nothing to reword, 18 excluded.
- `go test`: triggers (language, script, 您, stress, the agent's dispute, thread language without mood), calm English skipped with no model call, the status path, every new check, the masking fixes.

## Amendment 2 2026-11-11 — copy applied, photo copy, glossary
1. **Copy applied** to copy.go after Aura's review of `D-28-copy-proposals.md`: 41 proposals (all but `CopyWhichTaskBadPick`) and 17 of the refused ones, as refused. `CopyDeclined`, `CopyCancelFailed` and `CopyCancelWhyReask` were not applied. Placeholders, YES/NO lines and quoted titles were checked to be identical before and after for all 58. The test assertions that quoted the old wording (11 test files) were updated.
2. **"Text only" copy.** `CopyTextOnly` is sent when a message has no words and no photo was kept. With photos off (`CHANNEL_PHOTOS_ENABLED` unset), that is every attachment, so "I can only read text" stays true there. With photos on, attachments are answered earlier, by the photo path. A voice note, a video or a file is not an image, so it got `CopyPhotoNotImage` ("…send it as a photo or a screenshot"). What remains is a location, a contact card and the like. Now:
   - photos on: `CopyTextAndPhotos` ("I can read text and photos, but not that kind of message…");
   - `CopyPhotoNotImage` says text and photos work, and voice notes, videos and other files do not.
3. **Glossary** (voice.md v0.6): "supporter" and "HO:RA" are never translated, in any language. `CheckVoice` refuses a rewrite with fewer of either than the original, or with a translation of supporter (跑腿, repartidor, ayudante, …): reason `glossary`. The eval also counts glossary breaks in the text that would be sent.

### Evidence (amendment 2)
- `agenttest -voice -runs 2` (v0.6, the new copy): 46/114 through the layer, **46/46 passed**, fallback 0, 0 over 2.5 s. **Median 1.32 s, p90 1.69 s, p95 1.80 s, max 2.35 s** on those 46. Glossary: 18 of the 46 contain a term, with 0 broken and 0 refused. Seen: Spanish now leaves "borough" in English ("¿En qué borough está…?"). The check allows it.
- `REQUIRE_DB_TESTS=1 go test -p 1 -race -count=1 ./...` with Postgres: green. Without `-race`, one run hit an existing flake on main, `cancellation_policy_test.go:411`: the deadline was 11 ms past the 2m30s bound, likely DB `now()` against the Go clock. That file is untouched; it passed 3 reruns and the race run.
