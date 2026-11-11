# D-27: Supporter interest in the text channel goes to the application form

**Date:** 2026-11-10
**Status:** Proposed. Built on `feat/supporter-interest` (off main at 284a6ba). Not merged, not deployed. No migration.
**Trigger:** Aura, 2026-11-10: people text HO:RA's number because they want to WORK as a supporter ("are you hiring?", "how do I become a supporter?"). Send them to the application form, and keep recruiting out of the text channel.

## Decision
`internal/channel/supporter.go` recognises supporter interest by rule (`SupporterInterest`, in `routeMessage` after HELP and before the scope gate). The classifier's new `supporter_interest` intent (Sonnet) covers words no rule knows. It is never drafted.

- **First answer:** `CopySupporterApply` (under 250 characters): supporters run errands around NYC on their own schedule, plus the link. No earnings, rate or number of tasks.
- **The link:** `SUPPORTER_APPLY_URL`, default `https://mvp.horaapp.co/become-supporter`; a value that isn't https falls back to the default.
- **Follow-ups:** any later supporter question (pay, requirements, "did you get my application?") gets `CopySupporterFollowUp` and the link again. A vague question ("how much does it pay?") counts as one only within 24 h of the link. Never a hand-off.
- **Approved supporters:** the number's account gets `CopySupporterAlready` (use the supporter app). The status is derived as GET /profile derives it.
- **Mixed messages:** "book … and are you hiring?" → the interest clause is cut and the errand words are routed as usual. `CopySupporterLine` is sent once after the errand's reply. The classifier's `also_supporter_interest` does the same for words no rule splits.
- **Ops:** one non-urgent notice per number per New York day (`HandoffSupporterInterest`), by email only, with the last four digits and no message text.
- **No new state:** whether and when the link went out is read from the thread (ThreadReader), so there is no new column.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none

## Context and alternatives
- A column for "link sent at" would need a migration. The thread already has the answer.
- Ahead of the scope gate on purpose: "can I drive for you?" asks for a job, not a ride. A message that also matches a scope refusal rule ("are you hiring escorts?") is left to the scope gate. The scope prompt also says that asking to work for HO:RA is "allow".
- The model path still runs the latency prefetch: the words are parsed alongside the classifier, but no draft is made.

## Evidence
- `go run ./cmd/agenttest -supporter -runs 3` (Sonnet 5.5): 132/132, 81 by rule, 51 by the model, p50 1.2 s.
- `go test`: every phrasing through the router; near-misses are drafted; follow-ups, ops once per day, approved supporter, mixed messages; no routing paraphrase matches the rule.
- `REQUIRE_DB_TESTS=1 go test -p 1 ./... -race`: green.

## Amendment 2026-11-11 — approved supporters and referrals
Production 2026-11-10 15:20–15:26: an approved supporter's number asked "Can I be a supporter?" and then "Can you share me the apply link that I can send to my friends". Both got "You're already a HO:RA supporter!…", and the second asked for the link.
- A request for the link, or to share, refer or invite someone (`SupporterReferral`: "send me the apply link", "can my friend join?", "how can my friend apply?"; otherwise the classifier's `supporter_referral`, Sonnet) counts as supporter interest. An approved supporter gets `CopySupporterShare` ("Here's the link to share: <link>") with no "already" line and no ops notice. Anyone else gets the usual application answer.
- Only questions about their own work get `CopySupporterAlready`, and never twice in a row: asked again right after it, the reply is the link to share.
- "share the link with my friends" does not say which link. It counts as a referral only for an approved supporter, or right after HO:RA's last line was the application link or the "already" line. A link named as something else ("the restaurant link") is never ours.
- Evidence: `agenttest -supporter -runs 2` 128/128 (12 referral phrasings, 2 by the model; 8 referral errands); `agenttest -routing` 64/64 + 10/10; `REQUIRE_DB_TESTS=1 go test -p 1 -race ./...` green.
