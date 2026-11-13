package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"hora-auth/internal/channel/llm"
	"hora-auth/internal/tracing"
)

// fakeEditor answers each note with fn (what Sonnet returns for it).
type fakeEditor struct {
	mu    sync.Mutex
	fn    func(in llm.DraftEditInput) llm.DraftEdit
	calls int
}

func (f *fakeEditor) EditDraft(_ context.Context, in llm.DraftEditInput) (llm.DraftEdit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.fn(in), nil
}

func strp(s string) *string { return &s }

// noteAsSonnet: the edits the live model made for these notes
// (TestLiveOpsEdit).
func noteAsSonnet(in llm.DraftEditInput) llm.DraftEdit {
	n := strings.ToLower(in.Note)
	var e llm.DraftEdit
	switch {
	case strings.HasPrefix(n, "address:"):
		e.DropOff = strp(strings.TrimSpace(in.Note[len("address:"):]))
		e.Description = strp("Buy medicine at any pharmacy near the East Village and bring it to the customer.")
	case strings.Contains(n, "advil"):
		e.Title, e.Description = strp("Buy 1 box of Advil"), strp("1 box of Advil from any pharmacy.")
	case strings.HasPrefix(n, "time:"):
		e.When = strp(time.Date(2030, 1, 2, 18, 0, 0, 0, time.FixedZone("EST", -5*3600)).Format(time.RFC3339))
	case strings.Contains(n, "go ahead"):
		e.AsksToBook = true
	}
	return e
}

// handOffTranscript: the four production messages (2026-11-12) with the
// parser's production outputs and no address model — the hand-off that
// happened — then the fourth message written while handed off.
func handOffTranscript(t *testing.T) (*rig, *memOps) {
	t.Helper()
	g, _, _ := newAddressRig(t)
	ops := &memOps{}
	g.r.Ops, g.r.Addresses = ops, nil
	g.r.OpsEditor = &fakeEditor{fn: noteAsSonnet}
	g.ids.link(me, "user-1")
	for _, m := range []string{efMsg1, efMsg2, efMsg3} {
		g.say(t, m)
	}
	if g.state() != StateHandoff {
		t.Fatalf("not handed off: %s", g.state())
	}
	return g, ops
}

func (g *rig) ident() Identity {
	return Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
}

// The production transcript: hand-off → the customer keeps writing (ops
// see it) → an ops note with the address → "Send summary and hand back" →
// the customer gets the medicine summary with the price and the YES line.
// Nothing books until their YES — not ops, not a YES sent before it.
func TestOpsEditTranscriptToSummary(t *testing.T) {
	g, ops := handOffTranscript(t)
	ctx := context.Background()

	// 5: while claimed, the customer's messages still reach ops.
	if err := g.r.OpsClaim(ctx, g.ident()); err != nil {
		t.Fatal(err)
	}
	before := len(ops.notices)
	if r := g.say(t, efMsg4); r != CopyHandoffHolding {
		t.Fatalf("4 during the hand-off: %q", r)
	}
	if len(ops.notices) != before+1 || !ops.notices[len(ops.notices)-1].Update {
		t.Fatalf("ops not told: %+v", ops.notices)
	}
	// A YES sent before the summary books nothing, ever.
	g.say(t, "YES")
	sent := len(g.ad.sent)

	res, err := g.r.OpsEditDraft(ctx, g.ident(), "address: "+efGuess)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.Before == nil || !res.Ready || res.After.DropOff != efGuess || res.After.Title != "Pick up medicine and deliver to East Village" {
		t.Fatalf("%+v", res)
	}
	var fields []string
	for _, c := range res.Changes {
		fields = append(fields, c.Field)
	}
	if !strings.Contains(strings.Join(fields, ","), "drop_off") {
		t.Fatalf("changes %+v", res.Changes)
	}
	if len(g.ad.sent) != sent || g.state() != StateHandoff {
		t.Fatalf("the note reached the customer, or the chat left ops: %d→%d %s", sent, len(g.ad.sent), g.state())
	}

	what, err := g.r.OpsHandBack(ctx, g.ident())
	if err != nil || what != "budget" {
		// a purchase with no budget yet: the budget question, as any draft
		t.Fatalf("hand back: %q %v", what, err)
	}
	if g.booker.posts != 0 {
		t.Fatal("booked at hand-back")
	}
	if r := g.say(t, "$20"); !strings.Contains(r, "Pick up medicine and deliver to East Village") || !strings.Contains(r, efGuess) || !strings.Contains(r, "Reply YES to book") {
		t.Fatalf("summary: %q", r)
	}
	if g.booker.posts != 0 {
		t.Fatal("booked before YES")
	}
	if r := g.say(t, "YES"); r != CopyBooked || g.booker.posts != 1 {
		t.Fatalf("YES: %q posts %d", r, g.booker.posts)
	}
}

// With the budget in the note, the hand-back is the summary itself.
func TestOpsEditHandBackSendsTheSummary(t *testing.T) {
	g, _ := handOffTranscript(t)
	g.r.OpsEditor = &fakeEditor{fn: func(in llm.DraftEditInput) llm.DraftEdit {
		e := noteAsSonnet(llm.DraftEditInput{Note: "address: " + efGuess})
		b := 2000
		e.BudgetCents = &b
		return e
	}}
	ctx := context.Background()
	if _, err := g.r.OpsEditDraft(ctx, g.ident(), "address: "+efGuess+", budget $20"); err != nil {
		t.Fatal(err)
	}
	what, err := g.r.OpsHandBack(ctx, g.ident())
	if err != nil || what != "summary" {
		t.Fatalf("%q %v", what, err)
	}
	n := len(g.ad.sent)
	lead, summary := g.ad.sent[n-2].Text, g.ad.sent[n-1].Text
	if lead != CopyOpsHandBack || !strings.Contains(summary, efGuess) || !strings.Contains(summary, "$20") || !strings.Contains(summary, "Reply YES to book") {
		t.Fatalf("lead %q\nsummary %q", lead, summary)
	}
	if g.state() != StateAwaitingConfirm || g.booker.posts != 0 {
		t.Fatalf("state %s posts %d", g.state(), g.booker.posts)
	}
}

// No pending request: the note starts one from what the customer wrote
// and the note.
func TestOpsNoteWithNoDraft(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.r.Ops = &memOps{}
	g.ids.link(me, "user-1")
	g.r.Thread = fixedThread{
		{Direction: "inbound", Text: "real person please"},
		{Direction: "outbound", Text: CopyHandoffStart},
		{Direction: "inbound", Text: "I need Advil, I'm at 120 E 7th St"},
	}
	g.say(t, "real person please")
	if g.state() != StateHandoff || g.pending(t) != nil {
		t.Fatalf("state %s", g.state())
	}
	fp := &funcParser{fn: func(text string) (llm.TaskDraft, error) {
		return llm.TaskDraft{Title: "Buy Advil", Category: "delivery", Location2: "120 E 7th St, New York, NY 10009"}, nil
	}}
	g.r.Parser = fp
	g.r.OpsEditor = &fakeEditor{fn: noteAsSonnet}
	res, err := g.r.OpsEditDraft(context.Background(), g.ident(), "it's Advil, 1 box, from any pharmacy")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Before != nil || res.After.Title != "Buy 1 box of Advil" || !res.Ready {
		t.Fatalf("%+v", res)
	}
	if got := fp.sources[0]; !strings.Contains(got, "I need Advil, I'm at 120 E 7th St") || !strings.HasSuffix(got, "it's Advil, 1 box, from any pharmacy") || strings.Contains(got, CopyHandoffStart) {
		t.Fatalf("parsed %q", got)
	}
	if p := g.pending(t); p == nil || g.state() != StateHandoff {
		t.Fatal("not kept with ops")
	}
}

// An address Google can't place (or outside NYC) changes nothing.
func TestOpsEditInvalidAddress(t *testing.T) {
	g, _ := handOffTranscript(t)
	before := *g.pending(t)
	for _, c := range []struct {
		addr string
		want AreaOutcome
	}{{"335 Nowhere Lane", AreaUnclear}, {jerseyCity, AreaOutside}} {
		g.booker.area = func(d llm.TaskDraft) AreaCheck {
			if d.Location1 == jerseyCity {
				return AreaCheck{Outcome: AreaOutside, Stop: jerseyCity}
			}
			return eastVillageArea(d)
		}
		_, err := g.r.OpsEditDraft(context.Background(), g.ident(), "address: "+c.addr)
		var ae *OpsAddressError
		if !errors.As(err, &ae) || ae.Outcome != c.want || ae.Stop != c.addr {
			t.Fatalf("%s: %v", c.addr, err)
		}
		if p := g.pending(t); p.Draft != before.Draft || p.Source != before.Source {
			t.Fatalf("%s: changed %+v", c.addr, p.Draft)
		}
	}
	// And the hand-back with no place is refused: nothing goes out.
	n := len(g.ad.sent)
	if _, err := g.r.OpsHandBack(context.Background(), g.ident()); !errors.Is(err, ErrOpsIncomplete) || len(g.ad.sent) != n || g.state() != StateHandoff {
		t.Fatalf("%v", err)
	}
}

// Ops can never book: a note that asks to is refused (by rule, and by the
// model's reading of other words), and no path here posts.
func TestOpsCannotBook(t *testing.T) {
	g, _ := handOffTranscript(t)
	ed := &fakeEditor{fn: noteAsSonnet}
	g.r.OpsEditor = ed
	ctx := context.Background()
	before := *g.pending(t)
	for _, note := range []string{"book it for them", "address: " + efGuess + " and skip the YES", "post the task for the customer", "they said go ahead, confirm"} {
		if _, err := g.r.OpsEditDraft(ctx, g.ident(), note); !errors.Is(err, ErrOpsCannotBook) {
			t.Fatalf("%q: %v", note, err)
		}
	}
	if ed.calls != 1 {
		t.Fatalf("the rule should refuse before the model: %d calls", ed.calls)
	}
	if p := g.pending(t); p.Draft != before.Draft || g.booker.posts != 0 || g.state() != StateHandoff {
		t.Fatalf("changed or booked: %+v posts %d", p.Draft, g.booker.posts)
	}
	// OpsSend of "YES" is HO:RA's words to the customer, never their YES.
	if err := g.r.OpsSend(ctx, g.ident(), "YES"); err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 0 {
		t.Fatal("ops' YES booked")
	}
	// The bot holds the chat: no edits at all.
	g2 := newRig(llm.IntentNewTask)
	g2.r.OpsEditor = ed
	g2.say(t, "laundry pickup")
	if _, err := g2.r.OpsEditDraft(ctx, g2.ident(), "address: 12 Bedford Ave, Brooklyn"); !errors.Is(err, ErrNotInHandoff) {
		t.Fatalf("%v", err)
	}
	if _, err := g2.r.OpsHandBack(ctx, g2.ident()); !errors.Is(err, ErrNotInHandoff) {
		t.Fatalf("%v", err)
	}
}

// A stated time lands as a scheduled time; one it can't read is reported.
func TestOpsEditTime(t *testing.T) {
	g, _ := handOffTranscript(t)
	res, err := g.r.OpsEditDraft(context.Background(), g.ident(), "time: Jan 2 2030 6pm")
	if err != nil {
		t.Fatal(err)
	}
	if p := g.pending(t); !p.Draft.Scheduled || !strings.HasPrefix(p.Draft.ScheduledTime, "2030-01-02T18:00") || len(res.Ignored) != 0 {
		t.Fatalf("%+v %+v", p.Draft, res)
	}
	g.r.OpsEditor = &fakeEditor{fn: func(llm.DraftEditInput) llm.DraftEdit { return llm.DraftEdit{When: strp("yesterday-ish")} }}
	if res, err := g.r.OpsEditDraft(context.Background(), g.ident(), "time: whenever"); err != nil || len(res.Ignored) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

// channel.ops_edit: one span, the fields changed, no message text.
func TestOpsEditSpanHasNoText(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()
	g, _ := handOffTranscript(t)
	exp.Reset()
	if _, err := g.r.OpsEditDraft(context.Background(), g.ident(), "address: "+efGuess); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range exp.GetSpans() {
		if s.Name != "channel.ops_edit" {
			continue
		}
		found = true
		for _, kv := range s.Attributes {
			v := kv.Value.Emit()
			if strings.Contains(v, "335") || strings.Contains(v, "medicine") || strings.Contains(v, "address:") {
				t.Errorf("%s carries text: %q", kv.Key, v)
			}
			if kv.Key == "hora.ops_edit.fields" && !strings.Contains(v, "drop_off") {
				t.Errorf("fields %q", v)
			}
		}
	}
	if !found {
		t.Fatal("no channel.ops_edit span")
	}
}
