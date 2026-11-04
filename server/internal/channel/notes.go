package channel

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"hora-auth/internal/channel/llm"
)

// Notes for the supporter (production 2026-11-03 15:57 and 16:46 NY). With
// the summary open, "This is my number … if supporter arrive can call my
// friend Lily" was re-drafted as a correction: the parser had nothing to
// change, the same summary came back, and the instruction was gone. Now the
// agent proposes "note" for words meant for whoever will run the pending
// booking — a contact, an instruction, a door code — and they are kept on
// the draft, shown in the summary ("Note for your supporter: …") and sent
// with the booking as part of the task description the supporter reads.
// A note never changes the time, the price or the addresses.

// intentNote: the agent's "note" while the booking summary is open; the
// caller adds the words to the draft.
const intentNote llm.Intent = "note_for_booking"

// MaxNotes / maxNoteRunes bound what one draft carries.
const (
	MaxNotes     = 5
	maxNoteRunes = 500
)

// noteChangeRE: a note that also changes the booking ("call Lily, and make
// it 5pm") is a correction, re-drafted as before — a time or length must
// never be kept only as words in a note. Not digits in general: a phone
// number or a door code is exactly what a note carries.
var noteChangeRE = regexp.MustCompile(`(?i)\b(?:make it|instead|change|switch|move it|reschedule|tomorrow|tonight|today|this (?:morning|afternoon|evening)|o'?clock|\d{1,2}(?::\d{2})?\s*(?:am|pm|a\.m\.|p\.m\.))\b`)

func noteCarriesChange(text string) bool {
	return noteChangeRE.MatchString(text) || namesDuration(text)
}

// addNote keeps text as a note on the pending booking and shows the summary
// again with it, at today's price (the note changes no figure, but the
// summary's clock restarts, so the figures it states are fresh ones).
func (r *Router) addNote(ctx context.Context, in Inbound, conv Conversation, text string) error {
	p := conv.Pending
	note := strings.TrimSpace(text)
	if utf8.RuneCountInString(note) > maxNoteRunes {
		note = string([]rune(note)[:maxNoteRunes]) + "…"
	}
	if note != "" {
		p.Notes = append(p.Notes, note)
		if len(p.Notes) > MaxNotes {
			p.Notes = p.Notes[len(p.Notes)-MaxNotes:]
		}
	}
	items := p.Items()
	for i := range items {
		cents, minutes, err := r.Booker.Quote(ctx, items[i].Draft, items[i].BudgetCents)
		if err != nil {
			return fmt.Errorf("addNote: quote: %w", err)
		}
		items[i].QuoteCents, items[i].QuoteMinutes = cents, minutes
	}
	p.SetItems(items)
	return r.showSummary(ctx, in, conv)
}

// NotesDescription appends the requester's notes to a task description,
// as the supporter will read it.
func NotesDescription(desc string, notes []string) string {
	if len(notes) == 0 {
		return desc
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(desc))
	for _, n := range notes {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Note from the requester: ")
		b.WriteString(n)
	}
	return b.String()
}
