# D-30: Ops fix a customer's pending request during a hand-off; the customer still says YES

**Date:** 2026-11-13
**Status:** Proposed. Built on `feat/ops-draft-edit` (off main at 0183019). Not merged, not deployed. No migration.
**Trigger:** Aura, 2026-11-13, after production 2026-11-12 (identity 4bf78a05…): during a hand-off ops can only read, reply as HO:RA, claim and release. Fixing the customer's request meant asking them to repeat it.

## Decision
- **Internal note, structured edit.** In Ops Chats, a held chat has a "Fix the request" field. It is never sent to the customer. `POST /admin/channel/:identity/draft-edit {note}` → `Router.OpsEditDraft`. The small step's model (Sonnet, `llm.DraftEditor`) reads the note as an edit: title, description, category, pickup, drop-off, when, length, budget, supporter note. Nil fields are unchanged. The router applies it by rule.
- **Addresses are checked.** Every address the edit sets goes through the same area check as the customer's own (Google via `Booker.CheckArea`). Inside → the address as found. Unclear, outside, which-borough or check down → 422/503 naming the address, and **nothing is saved**.
- **No pending request:** the note starts one. The customer's inbound messages (oldest first) plus the note are parsed with the production parse prompt. Placeholder titles are refused (D-29).
- **Before / after.** The response carries both drafts and a per-field change list. Ops see a Field / Before / After table and the request as it will be summarised.
- **"Send summary and hand back"** (`POST /admin/channel/:identity/hand-back-summary` → `Router.OpsHandBack`) works only when the request has a place. It releases the chat (`ReleaseHandoff`, PutAgain) and sends `CopyOpsHandBack` ("…Nothing is booked until you reply YES to it."), then the request's next normal question: the name (unlinked number), the budget (a purchase with none set), or the summary with price, "Reply YES to book" and Terms. R-1 still applies, so a YES sent before the summary books nothing.
- **Ops can never book.** No route books, posts or charges, and a test asserts that no ops route is named for it. A note that asks to book, post, confirm or charge for the customer, or to skip the YES, is refused (403 `ops_cannot_book`): by rule first, by the model's `asks_to_book` for other words. An ops message "YES" is HO:RA's words to the customer, never their YES.
- **Audit:** `CHANNEL_OPS_DRAFT_EDIT` records the ops user id, identity and each changed field with before and after. The note text is not stored. Refusals are logged as `CHANNEL_OPS_DRAFT_EDIT_REFUSED` with their code, and hand-backs as `CHANNEL_OPS_HAND_BACK_SUMMARY` with what was sent.
- **Trace:** a `channel.ops_edit` root span (session = conversation) records the field names, created, ready and outcome. It carries no note, message text or address.
- **Unchanged:** while a chat is handed off (claimed or not), the customer's messages reach ops as before. An edit keeps the chat with ops and restarts the 10-minute clock.
- **Rule:** a purchase with only a drop-off counts as complete once ops set it ("from any pharmacy"); the supporter buys near the drop-off.

## Constitution impact
- Standards added: none
- Standards modified/retired: none
- Invariants added/changed: none. The money rule stands: only the customer's own YES to a summary shown after any change books (S-11/5, D-17 R-1).

## Context and alternatives
- Ops typing the summary themselves was rejected: the price and the YES line must be the bot's own summary, which the YES is checked against.
- A form with one box per field was rejected: ops write corrections in words while reading the thread, and the model fills the fields, with addresses checked by rule.
- Several-errand requests are refused (409 `several_errands`): a note cannot say which errand it means.

## Evidence
- `go test ./internal/channel -run Ops`: the production transcript (handoff → message 4 reaches ops while claimed → YES before the summary books nothing → note with the address → hand back → budget → summary → YES books once). Also: note with no draft, invalid and outside addresses (nothing saved, hand-back refused), booking attempts refused, time edits, and a span with no text. `TestPGOpsEditKeepsHandoff` covers the real store. `TestOpsEditErrorResponses` covers the HTTP codes and checks that no route books.
- Live (liveeval, Sonnet 5.5): `TestLiveOpsEditNotes` 5/5 notes read correctly (address → drop-off; "Advil, 1 box, from any pharmacy" → title, no fixed pickup; "today 6pm" → 18:00 NY; "$20" → 2000; "just confirm the booking for her" → asks_to_book). `TestLiveOpsTranscript`: the four messages hand off, two notes, then the hand-back sends the summary with the YES line, and nothing is posted.
- `REQUIRE_DB_TESTS=1 go test -p 1 -race ./...`: green. `vite build`: green.
