package channel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
)

// Ops fix a customer's pending request during a hand-off (D-30): an
// internal note, never sent to the customer, read by the model as a
// structured edit and applied by rule. Every address is checked like the
// customer's own. Ops can never book or skip the YES: "Send summary and
// hand back" releases the chat and the customer gets the normal next
// question — the summary with the price and "Reply YES to book" — and only
// their YES books.

var (
	// ErrOpsCannotBook: the note asks to book, post, confirm or charge for
	// the customer. Nothing changes; only the customer's YES books.
	ErrOpsCannotBook = errors.New("channel: ops cannot book or skip the customer's YES")
	// ErrOpsNoDraft: no request could be made from the conversation and the note.
	ErrOpsNoDraft = errors.New("channel: no request could be drafted")
	// ErrOpsSeveral: the pending request has several errands — fix it with
	// the customer, not with a note.
	ErrOpsSeveral = errors.New("channel: the pending request has several errands")
	// ErrOpsIncomplete: hand back with a summary needs a place (and nothing
	// still asked); ErrOpsNoPending: nothing to summarise.
	ErrOpsIncomplete = errors.New("channel: the request still lacks a place")
	ErrOpsNoPending  = errors.New("channel: no pending request")
)

// OpsAddressError: an address in the edit did not pass the area check.
// Nothing was saved.
type OpsAddressError struct {
	Stop    string
	Outcome AreaOutcome
}

func (e *OpsAddressError) Error() string {
	return fmt.Sprintf("channel: address %s (%s)", areaOutcomeName(e.Outcome), e.Stop)
}

// DraftFields is the draft as ops see it, before and after an edit.
type DraftFields struct {
	Title        string   `json:"title"`
	Category     string   `json:"category"`
	Description  string   `json:"description"`
	Pickup       string   `json:"pickup"`
	DropOff      string   `json:"drop_off"`
	When         string   `json:"when"`
	Minutes      int      `json:"duration_minutes"`
	BudgetCents  int      `json:"budget_cents"`
	NeedsBudget  bool     `json:"needs_budget"`
	Notes        []string `json:"notes"`
	QuoteCents   int      `json:"quote_cents"`
	QuoteMinutes int      `json:"quote_minutes"`
}

// FieldChange is one line of the audit: which field, from what, to what.
type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// OpsEdit is what an edit did. Ready: "Send summary and hand back" can
// go; otherwise Missing says what the request still lacks.
type OpsEdit struct {
	Created bool          `json:"created"`
	Before  *DraftFields  `json:"before"`
	After   DraftFields   `json:"after"`
	Changes []FieldChange `json:"changes"`
	Ready   bool          `json:"ready"`
	Missing string        `json:"missing,omitempty"`
	// Ignored: what the model returned that could not be used (a time it
	// could not read).
	Ignored []string `json:"ignored,omitempty"`
}

// opsBookRE: a note that asks ops' tool to book — refused by rule before
// any model reads it (the model's asks_to_book catches other words).
var opsBookRE = regexp.MustCompile(`(?i)\b(?:book|post|place|submit|charge|bill|pay for)\s+(?:it|this|that|the (?:task|order|errand|booking)|for (?:them|him|her|the customer))\b|\bskip (?:the )?(?:yes|confirmation|confirm)\b|\b(?:confirm|approve)\s+(?:it|the (?:task|booking|order))\s+for (?:them|him|her|the customer)\b|\bwithout (?:their|his|her|the customer'?s?) (?:yes|confirmation)\b`)

// OpsDraft is the pending request as ops see it (nil when none).
func (r *Router) OpsDraft(ctx context.Context, id Identity) (*DraftFields, string, error) {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return nil, "", fmt.Errorf("OpsDraft: %w", err)
	}
	if conv.Pending == nil {
		return nil, "", nil
	}
	f := r.draftFields(conv.Pending)
	return &f, missingOf(conv.Pending), nil
}

// OpsEditDraft applies an ops note to the pending request of a handed-off
// chat (or starts one from the conversation and the note). The chat stays
// with ops; the customer is sent nothing. ErrNotInHandoff when the bot
// holds the chat.
func (r *Router) OpsEditDraft(ctx context.Context, id Identity, note string) (res OpsEdit, err error) {
	ctx, sp := tracing.StartRootSpan(ctx, "channel.ops_edit", tracing.KindChain, string(id.Channel), id.UserID, id.ID)
	defer func() {
		// No message text, no note, no address: which fields, and how it went.
		fields := make([]string, 0, len(res.Changes))
		for _, c := range res.Changes {
			fields = append(fields, c.Field)
		}
		sp.Attr(attribute.StringSlice("hora.ops_edit.fields", fields), attribute.Bool("hora.ops_edit.created", res.Created),
			attribute.Bool("hora.ops_edit.ready", res.Ready), attribute.String("hora.ops_edit.outcome", opsEditOutcome(err)))
		if err != nil {
			sp.Attr(attribute.Bool("error", true))
		}
		sp.End()
	}()
	note = strings.TrimSpace(note)
	if note == "" {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: empty note")
	}
	if r.OpsEditor == nil || r.Booker == nil {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: not configured")
	}
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: %w", err)
	}
	if conv.State != StateHandoff {
		return OpsEdit{}, ErrNotInHandoff
	}
	if opsBookRE.MatchString(note) {
		return OpsEdit{}, ErrOpsCannotBook
	}

	var p PendingDraft
	if conv.Pending != nil {
		if len(conv.Pending.More) > 0 {
			return OpsEdit{}, ErrOpsSeveral
		}
		p = *conv.Pending
		before := r.draftFields(conv.Pending)
		res.Before = &before
	} else {
		d, source, err := r.opsNewDraft(ctx, id, note)
		if err != nil {
			return OpsEdit{}, err
		}
		p = PendingDraft{Draft: d, Source: source, CreatedAtUnix: r.now().Unix(), BudgetAskIndex: -1}
		res.Created = true
	}

	ectx, cancel := context.WithTimeout(ctx, 20*time.Second)
	e, err := r.OpsEditor.EditDraft(ectx, llm.DraftEditInput{Draft: p.Draft, BudgetCents: p.BudgetCents, Notes: p.Notes, Note: note, Now: r.now().In(r.loc())})
	cancel()
	if err != nil {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: %w", err)
	}
	if e.AsksToBook {
		return OpsEdit{}, ErrOpsCannotBook
	}

	d := p.Draft
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	if e.Title != nil && !PlaceholderTitle(*e.Title) {
		set(&d.Title, e.Title)
	}
	set(&d.Description, e.Description)
	if e.Category != nil && llm.Categories[strings.TrimSpace(*e.Category)] {
		set(&d.Category, e.Category)
	}
	var stops []*string
	if e.Pickup != nil {
		set(&d.Location1, e.Pickup)
		stops = append(stops, &d.Location1)
	}
	if e.DropOff != nil {
		set(&d.Location2, e.DropOff)
		stops = append(stops, &d.Location2)
	}
	if e.When != nil {
		switch w := strings.TrimSpace(*e.When); {
		case strings.EqualFold(w, "asap") || w == "":
			d.Scheduled, d.ScheduledTime = false, ""
		default:
			t, perr := time.Parse(time.RFC3339, w)
			if perr != nil || t.Before(r.now().Add(-5*time.Minute)) {
				res.Ignored = append(res.Ignored, "time")
			} else {
				d.Scheduled, d.ScheduledTime = true, t.Format(time.RFC3339)
			}
		}
	}
	if e.Minutes != nil && *e.Minutes > 0 && *e.Minutes <= 24*60 {
		d.DurationMinutes = *e.Minutes
	}
	if err := d.Validate(); err != nil {
		return OpsEdit{}, ErrOpsNoDraft
	}

	// Every address ops changed is checked like the customer's own: inside
	// takes the address as found; anything else changes nothing.
	areaStops := map[string]string{}
	for k, v := range p.AreaStops {
		areaStops[k] = v
	}
	for _, s := range stops {
		if strings.TrimSpace(*s) == "" {
			continue
		}
		got := r.Booker.CheckArea(ctx, llm.TaskDraft{Location1: *s})
		if got.Outcome != AreaInside {
			return OpsEdit{}, &OpsAddressError{Stop: *s, Outcome: got.Outcome}
		}
		if len(got.Resolved) == 1 && strings.TrimSpace(got.Resolved[0]) != "" {
			areaStops[stopKey(*s)] = got.Resolved[0]
			*s = got.Resolved[0]
		}
		areaStops[stopKey(*s)] = *s
	}
	if stopKey(d.Location1) != "" && stopKey(d.Location1) == stopKey(d.Location2) {
		d.Location1 = ""
	}
	// A new draft's stops were never checked: checked now, the same way.
	if res.Created {
		for _, s := range []*string{&d.Location1, &d.Location2} {
			if strings.TrimSpace(*s) == "" {
				continue
			}
			if _, ok := areaStops[stopKey(*s)]; ok {
				continue
			}
			got := r.Booker.CheckArea(ctx, llm.TaskDraft{Location1: *s})
			if got.Outcome != AreaInside {
				return OpsEdit{}, &OpsAddressError{Stop: *s, Outcome: got.Outcome}
			}
			if len(got.Resolved) == 1 && got.Resolved[0] != "" {
				*s = got.Resolved[0]
			}
			areaStops[stopKey(*s)] = *s
		}
	}

	it := DraftItem{Draft: d, BudgetCents: p.BudgetCents, NeedsBudget: p.NeedsBudget, BudgetAsked: p.BudgetAsked}
	if !it.NeedsBudget {
		it.NeedsBudget = r.needsPurchase(ctx, p.Source+"\n"+note, d)
	}
	if e.BudgetCents != nil && *e.BudgetCents >= 0 {
		it.BudgetCents, it.BudgetAsked, it.NeedsBudget = *e.BudgetCents, true, it.NeedsBudget || *e.BudgetCents > 0
	}
	if p.Area != "" {
		items := []DraftItem{it}
		buyInArea(items, p.Area)
		it = items[0]
	}
	cents, minutes, err := r.Booker.Quote(ctx, it.Draft, it.BudgetCents)
	if err != nil {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: quote: %w", err)
	}
	it.QuoteCents, it.QuoteMinutes = cents, minutes

	if e.SupportNote != nil && strings.TrimSpace(*e.SupportNote) != "" && len(p.Notes) < MaxNotes {
		p.Notes = append(p.Notes, strings.TrimSpace(*e.SupportNote))
	}
	// The ops words stay with the request: a later correction by the
	// customer is re-parsed with them, so the fix is not undone.
	p.Source = strings.TrimSpace(p.Source + "\n" + note)
	p.SetItems([]DraftItem{it})
	p.AreaStops = areaStops
	p.UnclearStop, p.WhichBorough, p.AddressGuess = "", false, ""
	p.PlaybookAsk, p.PlaybookAsked = nil, true // ops answered what the type would ask
	p.NameAsked, p.BudgetAskIndex = false, -1
	p.DetailsAsked = missingPlace([]DraftItem{it})
	p.DetailsTries = 0
	p.SummaryShown, p.SummaryShownAtUnix, p.BookingKey = "", 0, "" // a new summary, a new booking key

	// Saved with the chat still handed off; the clock restarts (ops are on it).
	fresh, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: %w", err)
	}
	if fresh.State != StateHandoff {
		return OpsEdit{}, ErrNotInHandoff // released meanwhile: the bot has it
	}
	fresh.Pending = &p
	if err := r.Conversations.SaveConversation(ctx, fresh); err != nil {
		return OpsEdit{}, fmt.Errorf("OpsEditDraft: %w", err)
	}
	if _, err := r.Conversations.TouchHandoff(ctx, id.ID); err != nil {
		log.Printf("[channel] ops edit identity=%s: touch: %v", id.ID, err)
	}

	res.After = r.draftFields(&p)
	res.Changes = fieldChanges(res.Before, res.After)
	res.Missing = missingOf(&p)
	res.Ready = res.Missing == ""
	log.Printf("[channel] ops edit identity=%s: %d fields changed, created=%v ready=%v", id.ID, len(res.Changes), res.Created, res.Ready)
	return res, nil
}

// opsNewDraft: no pending request — one is parsed from what the customer
// wrote (their messages, oldest first) and the note.
func (r *Router) opsNewDraft(ctx context.Context, id Identity, note string) (llm.TaskDraft, string, error) {
	if r.Parser == nil || r.Prompt == nil {
		return llm.TaskDraft{}, "", fmt.Errorf("opsNewDraft: not configured")
	}
	var lines []string
	if r.Thread != nil {
		msgs, err := r.Thread.RecentMessages(ctx, id.Channel, id.Address, 20)
		if err != nil {
			return llm.TaskDraft{}, "", fmt.Errorf("opsNewDraft: %w", err)
		}
		for _, m := range msgs {
			if m.Direction == "inbound" && strings.TrimSpace(m.Text) != "" {
				lines = append(lines, strings.TrimSpace(m.Text))
			}
		}
	}
	source := strings.Join(append(lines, note), "\n")
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	d, _, err := r.Parser.ParseTask(pctx, r.Prompt(r.now().In(r.loc())), source)
	cancel()
	if err != nil || PlaceholderTitle(d.Title) {
		log.Printf("[channel] ops edit identity=%s: no draft from the conversation: %v", id.ID, err)
		return llm.TaskDraft{}, "", ErrOpsNoDraft
	}
	if !namesDuration(source) {
		d.DurationMinutes = 0 // the default estimate, as any draft
	}
	return d, source, nil
}

// OpsHandBack: "Send summary and hand back". The chat goes back to the bot
// and the customer gets the request's next question as any draft would —
// the name (unlinked number), the budget, or the summary with its price
// and "Reply YES to book". Nothing is booked or charged here: only the
// customer's YES to that summary books. Returns which question went out.
func (r *Router) OpsHandBack(ctx context.Context, id Identity) (string, error) {
	conv, err := r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return "", fmt.Errorf("OpsHandBack: %w", err)
	}
	if conv.State != StateHandoff {
		return "", ErrNotInHandoff
	}
	if conv.Pending == nil {
		return "", ErrOpsNoPending
	}
	if missingOf(conv.Pending) != "" {
		return "", ErrOpsIncomplete
	}
	won, err := r.Conversations.ReleaseHandoff(ctx, id.ID, ReleaseOpts{Resume: StateAwaitingConfirm, AnyClock: true, PutAgain: true})
	if err != nil {
		return "", fmt.Errorf("OpsHandBack: %w", err)
	}
	if !won {
		return "", ErrNotInHandoff
	}
	conv, err = r.Conversations.GetConversation(ctx, id.ID)
	if err != nil {
		return "", fmt.Errorf("OpsHandBack: %w", err)
	}
	if conv.Pending == nil {
		return "", ErrOpsNoPending
	}
	in := Inbound{Channel: id.Channel, From: id.Address}
	if err := r.Reply(ctx, in, CopyOpsHandBack, ""); err != nil {
		return "", err
	}
	if !id.Linked() && conv.PendingName == "" {
		conv.State = StateAwaitingName
		conv.Pending.NameAsked = true
		if err := r.Conversations.SaveConversation(ctx, conv); err != nil {
			return "", fmt.Errorf("OpsHandBack: %w", err)
		}
		return "name", r.Reply(ctx, in, fmt.Sprintf(CopyAskName, conv.Pending.Headline()), "")
	}
	if nextBudgetAsk(conv.Pending) >= 0 {
		return "budget", r.continueDraft(ctx, in, id, conv)
	}
	return "summary", r.putSummary(ctx, in, conv, "")
}

// missingOf: what the request still lacks before a summary ("" none).
func missingOf(p *PendingDraft) string {
	switch {
	case p == nil:
		return "request"
	case missingPlace(p.Items()):
		return "address"
	case p.UnclearStop != "" || p.AddressGuess != "":
		return "address"
	}
	return ""
}

// missingPlace: an errand with no place at all, or a purchase-free errand
// with only a drop-off. A purchase with only a drop-off is fine when ops
// set it ("from any pharmacy"): bought near the drop-off.
func missingPlace(items []DraftItem) bool {
	for _, it := range items {
		l1, l2 := strings.TrimSpace(it.Draft.Location1), strings.TrimSpace(it.Draft.Location2)
		if l1 == "" && (l2 == "" || !it.NeedsBudget) {
			return true
		}
	}
	return false
}

func (r *Router) draftFields(p *PendingDraft) DraftFields {
	d := p.Draft
	when := "As soon as possible"
	if d.Scheduled {
		when = r.when(true, d.ScheduledTime)
	}
	return DraftFields{Title: d.Title, Category: d.Category, Description: d.Description, Pickup: d.Location1, DropOff: d.Location2,
		When: when, Minutes: p.QuoteMinutes, BudgetCents: p.BudgetCents, NeedsBudget: p.NeedsBudget,
		Notes: append([]string{}, p.Notes...), QuoteCents: p.QuoteCents, QuoteMinutes: p.QuoteMinutes}
}

// fieldChanges: the fields that differ (all of them, for a new draft).
func fieldChanges(before *DraftFields, after DraftFields) []FieldChange {
	var b DraftFields
	if before != nil {
		b = *before
	}
	var out []FieldChange
	add := func(f, x, y string) {
		if x != y {
			out = append(out, FieldChange{Field: f, Before: x, After: y})
		}
	}
	add("title", b.Title, after.Title)
	add("category", b.Category, after.Category)
	add("description", b.Description, after.Description)
	add("pickup", b.Pickup, after.Pickup)
	add("drop_off", b.DropOff, after.DropOff)
	add("when", b.When, after.When)
	add("budget", dollarsOrEmpty(b.BudgetCents), dollarsOrEmpty(after.BudgetCents))
	add("notes", strings.Join(b.Notes, " | "), strings.Join(after.Notes, " | "))
	add("quote", dollarsOrEmpty(b.QuoteCents), dollarsOrEmpty(after.QuoteCents))
	return out
}

func dollarsOrEmpty(c int) string {
	if c == 0 {
		return ""
	}
	return Dollars(c)
}

func opsEditOutcome(err error) string {
	var ae *OpsAddressError
	switch {
	case err == nil:
		return "applied"
	case errors.Is(err, ErrOpsCannotBook):
		return "refused_book"
	case errors.Is(err, ErrNotInHandoff):
		return "not_handed_off"
	case errors.As(err, &ae):
		return "address_" + areaOutcomeName(ae.Outcome)
	case errors.Is(err, ErrOpsNoDraft):
		return "no_draft"
	}
	return "error"
}
