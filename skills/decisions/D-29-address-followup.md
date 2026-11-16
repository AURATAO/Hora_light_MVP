# D-29: An answer to HO:RA's address question fills the open request

**Date:** 2026-11-13
**Status:** Proposed. Built on `fix/address-followup` (off main at f2384bc). Not merged, not deployed. No migration (new PendingDraft JSON fields only).
**Trigger:** Production 2026-11-12 ~21:56 UTC. The customer wrote "I'm in east village I need medicine", "335 e 3 ave", "6 street", "335 6 st 3 ave". HO:RA replied "I couldn't find "East Village" exactly…", then "Sorry, I couldn't work that out. Could you say what you need, where, and when?", then "Where should the supporter go for "x"?", then handed off ("details still missing after 2 answers"). No one answered in 10 minutes, and the timeout message re-asked the address question that had just failed.

## What actually failed
- The request was kept at step 1. Each answer was re-parsed together with it, which is the existing design.
- The parse model (Sonnet) returned degenerate drafts on two of the re-parses: an empty title ("decodeDraft: draft: empty title") and the title "x". Token counts match production (3131/3135 in), so the input was the same. This is rare model variance: 0 of 15 tries on 2026-11-13, then 1 of 12 each way in `agenttest -address`.
- On the empty title, the router sent `CopyParseFailed` and never saved the answer. "335 e 3 ave" was lost from the request, and the reply read as starting over. On "x", the router accepted the title and asked about "x".
- "East Village" is a neighbourhood, so Google can't place it as a door. It was counted as an unclear address, which is a failed answer.

## Decision
- **A — the answer fills the request.** When the re-parse fails (or the title is a placeholder and no pending title exists), `fillPending` puts the answer where the question pointed. It replaces the stop that could not be placed, or fills the missing place: the drop-off when the area is known, otherwise the place to go. A correction to a summary whose parse fails keeps the request and names it (`CopyRedraftFailed`). `CopyParseFailed` is only sent when nothing is pending.
- **B — no placeholder titles.** `PlaceholderTitle` covers "", "x", "-", "task", "n/a", "untitled", fewer than 3 characters, and the like. If one comes back, the pending request's title stands. With nothing pending, it counts as no parse (what they need is asked).
- **C — a guess to confirm.** If the map can't place an address, the small step's model (Sonnet, `llm.AddressGuesser`) reads everything said and proposes one full address, or null (it never invents a building number). The guess goes through the same area check (Google). Only a placed guess is asked: `CopyConfirmAddressGuess` "Did you mean …? Reply YES or send the right address." A YES uses it (no re-parse); other stops with the same building number are the same place. A NO asks for the right address, and that guess is never proposed again. Neither the confirm question nor its answer counts toward `MaxDetailsTries`. The guess is bounded at 8 s.
- **D — a neighbourhood is the area.** `helpers.ServiceArea` names a NYC neighbourhood (`Neighborhood`, result type `neighborhood` inside the county rule). The draft keeps it as `Area`, removes it from the stops, and asks `CopyAskAddressIn` ("Got it, the East Village. What's the exact address?"); this is not a failed answer. A purchase with only a drop-off is bought anywhere in the area: no "where should they buy it?" question, and a "Where to buy" line goes in the description. A one-stop drop-off posts as one stop (`draftToCreateInput`).
- **E — medicine.** New `pharmacy` playbook (matched by rule; the classifier also knows the id). Its `otc_items` ask is `first`: it is asked before the address when no over-the-counter item is named. "What do you need from the pharmacy? Over-the-counter things like Tylenol, cold medicine or bandages are fine — supporters can't pick up prescriptions." Prescriptions stay refused by the existing scope rule. A SKIP goes on to the address question, never to a summary.
- **F — after a timed-out hand-off.** The release marks the draft `SimplerAsk` with one try left, in the release's own UPDATE. The question comes back as `CopyAskAddressSimpler` (building number and street, with a cross street or the ZIP), never the one that failed.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. The area check still decides; the guess is only a question, and createTask checks again at the post.

## Context and alternatives
- Cross streets or a ZIP alone are not placeable by the existing bar (`placeable`), and that bar is not lowered. The simpler question asks for the building number with them, and the guess combines what was said.
- A rule-only fill would lose corrections the parse handles well. The fill runs only when the parse fails.

## Evidence
- `go test ./internal/channel -run 'Transcript|Address|Placeholder|Neighbourhood|Medicine|Handoff|SameBuilding'`: the production transcript with the parser's production outputs (empty title, "x"), with and without playbooks. `TestPGReleaseSimplerAsk` covers the release SQL. `helpers` covers neighbourhood detection.
- `agenttest -address -runs 3` (Sonnet 5.5): guesses 18/18, p50 1.7 s. Transcript parses: 1/12 placeholder or empty title, held by B.
- Live replay `TestLiveAddressFollowup` (liveeval; all chat steps on Sonnet, map stubbed), 5 runs: no lost request, no "x", no hand-off. Message 4 is always "Did you mean 335 E 6th St, New York, NY 10003?"; the YES goes on to the name question.
- `agenttest -routing` 66/66 + 10/10; `-playbooks` 29/29; `-discovery` 53/53. Agent eval: 171/176 + 1 SAME on the first run (4 fails), and #14, #103, #105 failing on the second. The agent prompt and cases are unchanged; #14 and #105 fail on main too, and #103 fails 1 in 4 runs.
