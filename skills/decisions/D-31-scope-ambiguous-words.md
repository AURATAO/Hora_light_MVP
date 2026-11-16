# D-31: Scope rules refuse only on unambiguous words; a refusal can be clarified; the welcome only at the start

**Date:** 2026-11-14
**Status:** Proposed. Built on `fix/scope-ambiguous-words` (off main at 0183019). Not merged, not deployed. No migration.
**Trigger:** Two production failures. WhatsApp 2026-11-14 16:56 UTC: "Hi I need someone to grab a drink from Hay Tea Noho nyc" was refused by the rule `refuse:alcohol` (its `(?:have|grab) a drink` pattern; Sonnet never saw it), and the correction "Hay tea is a bubble tea" got the welcome. iMessage 2026-11-13 17:40 UTC: "Hi — I need help with finding a perfect man" had no sensitive word, went to the classifier, and got the welcome.

## Decision
Amends D-26.
1. **Rules are a fast path for unambiguous words only.** A refusal rule matches only words with no everyday meaning (beer, wine, vodka, liquor store, cigarettes, vape, weed…). Words with an everyday meaning (drink, drinks, bar, club, shot, pot, joint, pills, high, party, spirits, cocktail, alcohol, naked, massage, romantic, caregiver, refill, bathing…) never refuse on their own. They are sensitive words, so Sonnet decides. Every refusal category was audited (playbooks.json `scope_rules` sr0.2).
2. **Sonnet reads with the conversation.** An ambiguous message goes to `LLM_SMALL_MODEL` with up to 6 thread lines. The prompt tells it to judge a named place by what it sells (HeyTea, Starbucks, Joe & The Juice…). Sonnet knew "Hay Tea" without a lookup, so the Google place search is not used.
3. **A refusal can be clarified.** When our last line is a refusal reply (within `ScopeFollowupWindow`, 30 min), the next message is read with the refused one (`ScopeRules.DecideIn`):
   - The rules on the new message alone run first. A new request refused by rule is refused as itself.
   - Then Sonnet says whether the message clarifies the refused one.
   - If it does, the rules run on the two together (an unambiguous word stays refused: "buy wine" + "it's for a gift"), then Sonnet's verdict on both.
   - Allowed together: the two messages go on as one request, as if the first had been allowed.
   - Not a clarification: the message is judged alone.

   It is read from `channel_messages`, so there is no new column.
4. **The welcome only for a greeting alone, at the start.** The start is no reply of ours in `WelcomeGap` (12 h). A greeting that carries a request ("Hi — I need…"), a mid-conversation greeting, a clarification or anything else gets a short line: `CopyHelloAgain`, `CopyAck` for a thanks, `CopyNotSure` otherwise. A name given with nothing to book gets "Thanks, NAME!".
5. **New category `dating`** (no ops alert): matchmaking, "find me a husband", dating apps. Asking a supporter to BE a date stays `intimate`.
6. **Refusal copy offers everyday errands only.** Companionship is being paused on legal advice. No reply offers a coffee, a walk or meeting someone (alcohol, private_company, third_person rewritten).

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none

## Context and alternatives
- More `unless` exemptions on the old patterns was the alternative. It is endless ("Hay Tea", "boba", every shop name), and the miss is a refused customer. A model call on an ambiguous word costs ~1.6 s.
- Storing the refused message on the conversation would need a migration. The thread already holds it.
- Open with Aura: public companionship is still allowed by the `public_company` rule and the companionship playbook (behind CHANNEL_PLAYBOOKS). Only the copy changed here.

## Evidence
- `go run ./cmd/agenttest -scope` (Sonnet 5.5, no fallback): 181/181 (106 by rule, 75 by the model, p50 1.6 s). `-scope -runs 3`: 543/543, allow/refuse never wrong, alert never wrong. `-scope -scope-all` (the model alone, never used in production): 180/181. The miss was "pick up my prescription from CVS" → `id_login` instead of `personal_care`. It is still refused, and in production a rule decides that message.
- `-routing` 66/66 + 10/10; `-playbooks` 29/29.
- `go test -tags liveeval -run TestLiveScopeTranscripts`: the production messages on WhatsApp and iMessage with real models. HeyTea is drafted; "perfect man" gets the dating decline.
- `REQUIRE_DB_TESTS=1 go test -p 1 -race -count=1 ./...` with Postgres: green.
