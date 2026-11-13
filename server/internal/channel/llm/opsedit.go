package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DraftEditor reads an ops teammate's correction to a customer's pending
// request ("address: 335 E 6th St, New York, NY 10003", "it's Advil, 1 box,
// from any pharmacy", "time: today 6pm") as a structured edit. It never
// books, approves or charges: the router applies the edit by rule, checks
// every address with the area check, and the customer still has to reply
// YES to the summary (D-30).
type DraftEditor interface {
	EditDraft(ctx context.Context, in DraftEditInput) (DraftEdit, error)
}

// DraftEditInput: the draft as it stands and the teammate's words.
type DraftEditInput struct {
	Draft       TaskDraft
	BudgetCents int
	Notes       []string
	Note        string
	Now         time.Time // New York time
}

// DraftEdit: nil = unchanged. When is "asap" or an RFC 3339 time.
type DraftEdit struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Category    *string `json:"category"`
	Pickup      *string `json:"pickup"`
	DropOff     *string `json:"drop_off"`
	When        *string `json:"when"`
	Minutes     *int    `json:"duration_minutes"`
	BudgetCents *int    `json:"budget_cents"`
	SupportNote *string `json:"supporter_note"`
	// AsksToBook: the words ask to book, post, confirm or charge on the
	// customer's behalf — refused; only the customer's YES books.
	AsksToBook bool `json:"asks_to_book"`
}

const draftEditPrompt = `You help an operations teammate of HO:RA, an errand service in New York City, correct a customer's pending errand request. The teammate writes a short internal correction; you turn it into a structured edit of the draft. The customer will see the corrected summary and must reply YES themselves before anything is booked.

Return only the fields the correction changes; every other field is null.
- "title": a short imperative summary in English (max 60 characters), only when the correction changes what the errand is ("it's Advil, 1 box" → "Buy 1 box of Advil").
- "description": the full new description for the supporter, keeping what still holds from the current one and adding the correction's details (item, quantity, where to buy, apartment, buzzer). Drop statements the correction makes wrong or settles (for example "confirm the address" once the address is given).
- "category": one of delivery, grocery, laundry, companionship, queue, anything_else — only if it changes.
- "pickup": where the supporter goes first (a store, a pickup address). "from any pharmacy" means no fixed pickup: leave null and say it in the description.
- "drop_off": where it is brought (the customer's address). A correction that only gives "address: …" for a delivery is the drop-off; for an errand done at one place it is the pickup.
- Write addresses as the teammate wrote them; never invent or complete a building number.
- "when": "asap", or an RFC 3339 time with the New York offset for a stated time ("today 6pm"), using the current time given below.
- "duration_minutes": only if a length is stated.
- "budget_cents": a shopping budget stated in dollars, as cents ("budget $20" → 2000).
- "supporter_note": a short instruction for the supporter that is not part of the errand itself (a contact, "call on arrival").
- "asks_to_book": true if the teammate asks you to book, post, confirm, approve or charge the task for the customer, or to skip the customer's YES. You never do that; set it and change nothing else.

Return {"title","description","category","pickup","drop_off","when","duration_minutes","budget_cents","supporter_note","asks_to_book"}.`

func draftEditSchema() map[string]any {
	ns := map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}}
	ni := map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "null"}}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": ns, "description": ns, "category": ns, "pickup": ns, "drop_off": ns, "when": ns,
			"duration_minutes": ni, "budget_cents": ni, "supporter_note": ns,
			"asks_to_book": map[string]any{"type": "boolean"},
		},
		"required":             []string{"title", "description", "category", "pickup", "drop_off", "when", "duration_minutes", "budget_cents", "supporter_note", "asks_to_book"},
		"additionalProperties": false,
	}
}

func editDraft(ctx context.Context, m Provider, in DraftEditInput) (DraftEdit, error) {
	cur, err := json.Marshal(map[string]any{
		"title": in.Draft.Title, "category": in.Draft.Category, "description": in.Draft.Description,
		"pickup": in.Draft.Location1, "drop_off": in.Draft.Location2,
		"scheduled": in.Draft.Scheduled, "scheduled_time": in.Draft.ScheduledTime,
		"duration_minutes": in.Draft.DurationMinutes, "budget_cents": in.BudgetCents, "supporter_notes": in.Notes,
	})
	if err != nil {
		return DraftEdit{}, fmt.Errorf("edit draft: %w", err)
	}
	user := fmt.Sprintf("Current time in New York: %s (%s)\nCurrent draft: %s\nTeammate's correction: %q",
		in.Now.Format(time.RFC3339), in.Now.Format("Monday"), cur, strings.TrimSpace(in.Note))
	out, _, err := m.CompleteJSON(ctx, draftEditPrompt, user, "draft_edit", draftEditSchema(), 1024)
	if err != nil {
		return DraftEdit{}, fmt.Errorf("edit draft: %w", err)
	}
	var e DraftEdit
	if err := json.Unmarshal([]byte(extractJSONObject(out)), &e); err != nil {
		return DraftEdit{}, fmt.Errorf("edit draft decode: %w", err)
	}
	return e, nil
}

func (m *Model) EditDraft(ctx context.Context, in DraftEditInput) (DraftEdit, error) {
	return editDraft(ctx, m.provider(), in)
}
