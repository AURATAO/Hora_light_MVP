# D-26: Scope rules for people and homes in the text channel

**Date:** 2026-11-10
**Status:** Proposed. Built on `feat/scope-rules` (off main with D-25 merged). Not merged, not deployed. No migration.
**Trigger:** Aura, 2026-11-10: put the rules about people and homes into the errand playbooks. Public companionship, home errands and items with a friend are allowed. Private companionship, accompanying someone else, romantic or sexual requests, personal or medical care, minors, alcohol/tobacco/cannabis, ID or logins, driving the customer's car, and cash beyond the budget are refused. Romantic and sexual requests also go to ops as urgent.

## Decision
`playbooks.json` → `scope_rules` (the slot D-25 left empty) now holds the allowed sentences (`people`, `homes`), nine refusal categories, each with a fixed reply that offers what HO:RA can do instead, the allow shapes, the sensitive words, the home-entry question, and the evals. The router checks every message the person can still act on (`routeMessage`, right after HELP and before inline replies, withdrawals, discovery, drafting and relaying). A refused request gets its reply and nothing else: no parse, no draft, no price, no search, and nothing relayed to a supporter.

1. **Order (rules are a fast path, Sonnet decides otherwise):** refusal rules in file order; then allow shapes (company in a named public place, an item from or to an adult, a home errand with access instructions); then "no sensitive word at all" = an everyday errand. Only what is left goes to `LLM_SMALL_MODEL` (`llm.CheckScope`, JSON enum `allow` | category id). A model error allows and is logged: the clear refusals are the rules', and a draft still needs the person's YES.
2. **Draft words are checked again by rule** in `startDraftOpts` on everything said for the draft. This catches two messages that are fine alone but not together, with no extra model call.
3. **`intimate` alerts ops** (`HandoffScopeAlert`, urgent, with the open tasks, by email and text). It is a notice, not a hand-off. An open draft is dropped. Any other refusal keeps an open draft and says "Your earlier booking is still waiting."
4. **Companionship playbook** (first in file order): asks where to meet (a public place), when and how long, with a summary line and a supporter note for public-only. **Home entry** (any type): the access question ("will you or an adult you name be home, or how does the supporter get in?") comes before the type's own questions unless the words already answer it. The task gets a note, the summary gets a line. Both are behind `CHANNEL_PLAYBOOKS`; the scope gate is not.
5. Discovery's own `out_of_scope` path (D-23) stays for what only its model sees (reservations, price promises).

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none

## Context and alternatives
- A scope field on the classifier would be free for the messages it reads, but the fast paths (clear searches, clear errands, in-context relays) never reach the classifier. A separate check ahead of all of them is the one place nothing can bypass.
- Asking the model on every message would cost about 1.3 s each. The sensitive-word gate keeps that cost to roughly 1 message in 50 in the repo's corpora.
- `ai_task_parser.go` is frozen for review (D-16) and still teaches "walk my mom to her doctor appointment" as a companionship example. In the chat the gate refuses that message before the parse. The app's own AI prefill and posting are not gated.

## Evidence
- `go run ./cmd/agenttest -scope -runs 3` (Sonnet 5.5, no fallback): 306/306. 264 of those were decided by rule, 42 by the model, with a median model latency of 1.3 s. Allow/refuse was never wrong and the alert was never wrong.
- `-scope -scope-all` (the model alone on every case): 102/102 after one prompt fix. Before the fix, the model refused "pick up Tylenol from Duane Reade"; the personal-care sentence now says over-the-counter items are fine.
- `go test`: every eval through the router (`TestScopeEvalsThroughRouter`). Refusals: the category's reply only, no parse, no draft, idle, no quote; ops told only for `intimate`. The existing corpora (381 messages) are refused only where they ask for alcohol, prescriptions or a login (`TestScopeRulesSpareExistingErrands`).
- `REQUIRE_DB_TESTS=1 go test -p 1 ./... -race -count=1` with Postgres: green.

## Amendment before merge (2026-11-10)
Aura, same day, before merge:
1. **main merged in** (supporter-only dispatch, item problem, new pricing). It merged with no conflicts. Public companionship is priced as companionship whatever the parser filed it under: a playbook may name a `category`, and the companionship entry names `companionship`.
2. **Fail closed.** The order is unchanged: refusal rules, then allowed patterns, then no sensitive word = everyday errand, then the model. When the words contain a sensitive word and the model fails, times out (`ScopeTimeout`, 8 s) or returns an unknown verdict, nothing is drafted. The person gets "A HO:RA teammate will look at this and get back to you shortly." and the thread is handed to ops (`HandoffScopeUnchecked`). It is urgent only when an intimate pattern appears in the words, even one its unless exempts. The log carries the failure's kind (timeout, status_NNN, error), never the text. Messages with no sensitive word never ask the model.
3. **Every task creation path** (`server/task_scope.go`) runs the same check:
   - `POST /tasks` (web and app);
   - `PATCH /tasks/:id` when the title or description changes;
   - `POST /ai/parse-task`, via gin middleware in front of the frozen parser, which is not touched.
   A refusal is 422 `{error: "out_of_scope", category, message: <the category's reply>}`. A model failure on sensitive words is 503 `scope_unchecked`. The web post form shows the reply above the submit button, and the AI box shows it in place of "couldn't understand". Title and description are joined with ". " so they are not read as one phrase. HO:RA's own fixed supporter notes are removed first ("…no alcohol" must not refuse a companionship task). A chat booking was read by the chat's gate with the model, so its post re-checks by rule only.
4. **No driving people.** The `driving` category now covers rides in anyone's car: "drive me to JFK", "give me a ride", "pick me up at … and drive me", "drive my mom to the airport", "be my driver". It is ordered before `third_person` so it gets the driving copy. Carrying items by car is allowed, and so is "pick me up a coffee".
