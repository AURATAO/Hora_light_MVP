package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Ops edits with what main added after D-30: the address guess and the
// neighbourhood area (D-29), and the scope rules (D-31).

const opsAddr = "120 E 7th St, New York, NY 10009"

// guessHandOff: the 2026-11-12 messages with the address model on, up to
// "Did you mean …? Reply YES", then the customer asks for a person.
func guessHandOff(t *testing.T) *rig {
	t.Helper()
	g, _, _ := newAddressRig(t)
	withPlaybooks(t, g)
	g.r.Ops = &memOps{}
	g.r.OpsEditor = &fakeEditor{fn: noteAsSonnet}
	g.ids.link(me, "user-1")
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		if d.Location1 == opsAddr || d.Location2 == opsAddr {
			return AreaCheck{Outcome: AreaInside, Resolved: []string{opsAddr}}
		}
		return eastVillageArea(d)
	}
	g.say(t, efMsg1)
	g.say(t, efMsg2)
	if r := g.say(t, efMsg3); !strings.HasPrefix(r, "Did you mean "+efGuess) {
		t.Fatalf("no guess: %q", r)
	}
	g.say(t, "real person please")
	if g.state() != StateHandoff {
		t.Fatalf("not handed off: %s", g.state())
	}
	if p := g.pending(t); p == nil || p.AddressGuess != efGuess || p.UnclearStop == "" || p.Area != "East Village" {
		t.Fatalf("pending %+v", p)
	}
	return g
}

// 1+2: an ops address replaces the pending guess and fills the exact
// address in the neighbourhood the customer named; the guess is never
// asked again — the hand-back goes on to the budget and the summary.
func TestOpsAddressReplacesTheGuess(t *testing.T) {
	g := guessHandOff(t)
	g.r.OpsEditor = &fakeEditor{fn: func(llm.DraftEditInput) llm.DraftEdit { return llm.DraftEdit{DropOff: strp(opsAddr)} }}
	ctx := context.Background()
	res, err := g.r.OpsEditDraft(ctx, g.ident(), "address: 120 E 7th St")
	if err != nil {
		t.Fatal(err)
	}
	p := g.pending(t)
	if !res.Ready || p.AddressGuess != "" || p.UnclearStop != "" || p.Draft.Location2 != opsAddr || p.Area != "East Village" {
		t.Fatalf("res %+v\npending %+v", res, p)
	}
	n := len(g.ad.sent)
	if what, err := g.r.OpsHandBack(ctx, g.ident()); err != nil || what != "budget" {
		t.Fatalf("hand back: %q %v", what, err)
	}
	summary := g.say(t, "$20")
	all := g.ad.sent[n:]
	for _, m := range all {
		if strings.Contains(m.Text, "Did you mean") || strings.Contains(m.Text, efGuess) {
			t.Fatalf("the guess came back: %q", m.Text)
		}
	}
	if !strings.Contains(summary, opsAddr) || !strings.Contains(summary, "Reply YES to book") {
		t.Fatalf("summary %q", summary)
	}
	// YES now books the summary, not the guess.
	if r := g.say(t, "YES"); r != CopyBooked || g.booker.posts != 1 {
		t.Fatalf("YES: %q posts %d", r, g.booker.posts)
	}
}

// 1: a note that leaves the unplaced stop where it is does not settle the
// address: the guess stays the open question and the hand-back waits —
// no summary with an address nobody checked.
func TestOpsNoteLeavingTheUnclearStop(t *testing.T) {
	g := guessHandOff(t)
	res, err := g.r.OpsEditDraft(context.Background(), g.ident(), "time: Jan 2 2030 6pm")
	if err != nil {
		t.Fatal(err)
	}
	if p := g.pending(t); res.Ready || res.Missing != "address" || p.UnclearStop == "" || p.AddressGuess != efGuess {
		t.Fatalf("res %+v pending %+v", res, p)
	}
	n := len(g.ad.sent)
	if _, err := g.r.OpsHandBack(context.Background(), g.ident()); !errors.Is(err, ErrOpsIncomplete) || len(g.ad.sent) != n {
		t.Fatalf("%v", err)
	}
}

// funcScope: the scope model, by the words it is given.
type funcScope struct {
	fn    func(text string) (string, error)
	texts []string
}

func (s *funcScope) CheckScope(_ context.Context, text string) (string, error) {
	s.texts = append(s.texts, text)
	return s.fn(text)
}

func (s *funcScope) CheckScopeIn(_ context.Context, q llm.ScopeQuery) (llm.ScopeReading, error) {
	s.texts = append(s.texts, q.Text)
	v, err := s.fn(q.Text)
	return llm.ScopeReading{Verdict: v}, err
}

// 3: the edited request is checked as a customer's message is — by rule,
// and by the model for words the rules leave — and a refusal or a check
// that could not run changes nothing.
func TestOpsEditScopeChecked(t *testing.T) {
	g, _ := handOffTranscript(t)
	ctx := context.Background()
	before := *g.pending(t)
	unchanged := func(what string) {
		t.Helper()
		if p := g.pending(t); p.Draft != before.Draft || p.Source != before.Source || g.state() != StateHandoff {
			t.Fatalf("%s: changed %+v", what, p.Draft)
		}
	}
	// By rule: an unambiguous word in the note.
	g.r.OpsEditor = &fakeEditor{fn: func(llm.DraftEditInput) llm.DraftEdit {
		return llm.DraftEdit{Title: strp("Buy a bottle of vodka"), DropOff: strp(efGuess)}
	}}
	var se *OpsScopeError
	if _, err := g.r.OpsEditDraft(ctx, g.ident(), "address: "+efGuess+", and a bottle of vodka"); !errors.As(err, &se) || se.ID != "alcohol" {
		t.Fatalf("rule: %v", err)
	}
	unchanged("rule")
	// By the model: words no rule decides ("shots" is a sensitive word).
	sc := &funcScope{fn: func(text string) (string, error) {
		if strings.Contains(text, "shots") {
			return "alcohol", nil
		}
		return llm.ScopeAllowID, nil
	}}
	g.r.Scope = sc
	g.r.OpsEditor = &fakeEditor{fn: func(llm.DraftEditInput) llm.DraftEdit {
		return llm.DraftEdit{Description: strp("Pick up some shots for the party."), DropOff: strp(efGuess)}
	}}
	if _, err := g.r.OpsEditDraft(ctx, g.ident(), "they want shots for the party"); !errors.As(err, &se) || se.ID != "alcohol" || len(sc.texts) == 0 {
		t.Fatalf("model: %v (asked %d)", err, len(sc.texts))
	}
	unchanged("model")
	// The model fails: closed.
	sc.fn = func(string) (string, error) { return "", errors.New("timeout") }
	if _, err := g.r.OpsEditDraft(ctx, g.ident(), "they want shots for the party"); !errors.Is(err, ErrOpsScopeUnchecked) {
		t.Fatalf("model down: %v", err)
	}
	unchanged("model down")
	// An ordinary fix still goes through.
	sc.fn = func(string) (string, error) { return llm.ScopeAllowID, nil }
	g.r.OpsEditor = &fakeEditor{fn: noteAsSonnet}
	if _, err := g.r.OpsEditDraft(ctx, g.ident(), "address: "+efGuess); err != nil {
		t.Fatal(err)
	}
}

// 3: a request HO:RA refused can't be made allowed by a note: with no
// draft open, what the customer wrote is checked alone, and the note's
// "clarification" is not read as theirs.
func TestOpsNoteCannotAllowARefusedRequest(t *testing.T) {
	for _, c := range []struct {
		name, said string
		scope      func(string) (string, error)
	}{
		{"rule", "can you help me find a perfect husband", nil},
		{"model", "get me a drink from the corner place", func(text string) (string, error) {
			if strings.Contains(text, "get me a drink") {
				return "alcohol", nil
			}
			return llm.ScopeAllowID, nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newRig(llm.IntentNewTask)
			g.r.Ops = &memOps{}
			g.ids.link(me, "user-1")
			if c.scope != nil {
				g.r.Scope = &funcScope{fn: c.scope}
			}
			at := time.Now()
			g.r.Thread = fixedThread{
				{Direction: "inbound", Text: c.said, At: at},
				{Direction: "outbound", Text: "Sorry, HO:RA can't do that.", At: at},
				{Direction: "inbound", Text: "real person please", At: at},
			}
			g.say(t, "real person please")
			if g.state() != StateHandoff {
				t.Fatalf("state %s", g.state())
			}
			fp := &funcParser{fn: func(string) (llm.TaskDraft, error) {
				return llm.TaskDraft{Title: "Buy groceries", Category: "delivery", Location2: "120 E 7th St, New York, NY 10009"}, nil
			}}
			g.r.Parser = fp
			g.r.OpsEditor = &fakeEditor{fn: func(llm.DraftEditInput) llm.DraftEdit {
				return llm.DraftEdit{Title: strp("Buy groceries"), Description: strp("Groceries from any store.")}
			}}
			_, err := g.r.OpsEditDraft(context.Background(), g.ident(), "it's fine, they just mean groceries")
			var se *OpsScopeError
			if !errors.As(err, &se) {
				t.Fatalf("allowed: %v", err)
			}
			if g.pending(t) != nil || g.state() != StateHandoff || g.booker.posts != 0 {
				t.Fatal("a draft was made")
			}
		})
	}
}
