package channel

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// 2026-11-17: a pickup or a purchase with no drop-off is asked where to
// bring it before the budget and the summary.

const (
	cvsAddr  = "600 Broadway, New York, NY 10012"
	homeAddr = "120 E 7th St, New York, NY 10009"
)

// placedArea: every stop with a building number is inside, as typed.
func placedArea(d llm.TaskDraft) AreaCheck {
	var resolved []string
	for _, s := range []string{d.Location1, d.Location2} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if !hasDigitRE.MatchString(s) {
			return AreaCheck{Outcome: AreaUnclear, Stop: s}
		}
		resolved = append(resolved, s)
	}
	return AreaCheck{Outcome: AreaInside, Resolved: resolved}
}

// tylenolParse: the pickup at the CVS, and the drop-off once one is said.
func tylenolParse(text string) (llm.TaskDraft, error) {
	d := llm.TaskDraft{Title: "Pick up Tylenol at CVS", Category: "delivery", Location1: cvsAddr}
	if strings.Contains(text, "120 E 7th") {
		d.Location2 = homeAddr
	}
	return d, nil
}

func pharmacyRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Parser = &funcParser{fn: tylenolParse}
	g.booker.area = placedArea
	g.r.Purchases = yesPurchases{}
	withPlaybooks(t, g)
	return g
}

// The pharmacy pickup: the drop-off first, then the budget (a purchase the
// words make clear: only how much), then the summary with both stops.
func TestPharmacyPickupAsksTheDropOff(t *testing.T) {
	g := pharmacyRig(t)
	got := g.say(t, "can you pick up Tylenol at the CVS at 600 Broadway")
	t.Logf("pick up Tylenol → %s", got)
	if got != CopyAskDropOff || g.state() != StateAwaitingConfirm {
		t.Fatalf("first question: %q state %s", got, g.state())
	}
	if g.say(t, "YES"); g.booker.posts != 0 {
		t.Fatal("a YES with nowhere to bring it booked")
	}
	if got := g.ad.last(); got != CopyAskDropOffAgain {
		t.Fatalf("YES with nothing suggested: %q", got)
	}
	got = g.say(t, "120 E 7th St")
	t.Logf("120 E 7th St → %s", got)
	if got != CopyAskBudgetClear {
		t.Fatalf("budget after the drop-off: %q", got)
	}
	sum := g.say(t, "$15")
	t.Logf("$15 → %s", sum)
	if !strings.Contains(sum, cvsAddr+" → 120 E 7th St") || !strings.Contains(sum, "Reply YES to book") {
		t.Fatalf("summary: %q", sum)
	}
}

// The last drop-off is offered; YES takes it.
func TestDropOffOffersTheLastAddress(t *testing.T) {
	g := pharmacyRig(t)
	g.r.History = fakeHistory{tasks: []PastTask{{Title: "Bagel run", Category: "delivery", Location1: "Russ & Daughters", Location2: "2 Bond St, New York, NY 10012"}}}
	got := g.say(t, "can you pick up Tylenol at the CVS at 600 Broadway")
	if want := fmt.Sprintf(CopyAskDropOffLast, "2 Bond St"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := g.say(t, "yes"); got != CopyAskBudgetClear {
		t.Fatalf("YES to the last address: %q", got)
	}
	if p := g.pending(t); p.Draft.Location2 != "2 Bond St, New York, NY 10012" || p.DropOffAsked {
		t.Fatalf("drop-off %+v", p.Draft)
	}
	// "no" to the suggestion asks for the address instead.
	g2 := pharmacyRig(t)
	g2.r.History = g.r.History
	g2.say(t, "can you pick up Tylenol at the CVS at 600 Broadway")
	if got := g2.say(t, "no"); got != CopyAskDropOff || g2.pending(t) == nil {
		t.Fatalf("no to the suggestion: %q", got)
	}
}

// Errands that happen at one place are not asked.
func TestOneLocationErrandsAreNotAsked(t *testing.T) {
	for _, c := range []struct {
		msg string
		d   llm.TaskDraft
	}{
		{"I need someone wait line for me at Apple Store 767 5th Ave, ASAP", llm.TaskDraft{Title: "Wait in line at Apple Store", Category: "queue", Location1: "767 5th Ave, New York, NY 10153"}},
		{"check if Levain at 167 W 74th St still has the walnut cookie, just look", llm.TaskDraft{Title: "Check cookie stock at Levain", Category: "delivery", Location1: "167 W 74th St, New York, NY 10023"}},
		{"drop my laundry at the laundromat at 12 Bedford Ave", llm.TaskDraft{Title: "Drop laundry at the laundromat", Category: "laundry", Location1: "12 Bedford Ave, Brooklyn, NY 11211"}},
		{"return a package at the UPS Store at 10 Union Sq E, label attached, boxed", llm.TaskDraft{Title: "Return a package at UPS", Category: "delivery", Location1: "10 Union Sq E, New York, NY 10003"}},
		{"put the groceries away in my kitchen at 12 Bedford Ave, my roommate Alex is home, substitutions fine", llm.TaskDraft{Title: "Put groceries away", Category: "grocery", Location1: "12 Bedford Ave, Brooklyn, NY 11211"}},
	} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.booker.area = placedArea
		withPlaybooks(t, g)
		g.parser.draft = c.d
		g.say(t, c.msg)
		for _, s := range g.sentTexts() {
			if strings.Contains(s, "bring it") || strings.Contains(s, "bring “") {
				t.Errorf("%q asked the drop-off: %q", c.msg, g.sentTexts())
			}
		}
	}
}

// Never a summary — nor a booking — for a pickup with nowhere to bring it:
// the hand-back puts the drop-off question, and a YES to it does not book.
func TestNoSummaryWithoutADropOff(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.booker.area = placedArea
	g.say(t, "talk to a human")
	c := g.conv(t)
	p := &PendingDraft{Source: "pick up Tylenol at CVS", BudgetAskIndex: -1}
	p.SetItems([]DraftItem{{Draft: llm.TaskDraft{Title: "Pick up Tylenol at CVS", Category: "delivery", Location1: cvsAddr}, QuoteCents: 1200, NeedsBudget: true, BudgetAsked: true}})
	c.Pending = p
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := g.r.Release(context.Background(), pilotID); err != nil {
		t.Fatal(err)
	}
	if got := g.ad.last(); got != CopyAskDropOff || g.state() != StateAwaitingConfirm {
		t.Fatalf("hand-back: %q state %s", got, g.state())
	}
	g.say(t, "YES")
	if g.booker.posts != 0 {
		t.Fatal("booked with no drop-off")
	}
	if got := g.say(t, "120 E 7th St"); !strings.Contains(got, "Reply YES to book") || !strings.Contains(got, "120 E 7th St") {
		t.Fatalf("summary after the drop-off: %q", got)
	}
	for _, s := range []*PendingDraft{
		{Draft: llm.TaskDraft{Title: "Coffee", Category: "delivery", Location1: bbAddr}},
		{Draft: llm.TaskDraft{Title: "Groceries", Category: "grocery", Location1: "Whole Foods on 3rd"}},
	} {
		if needsDropOff(s) != 0 {
			t.Errorf("%q needs a drop-off", s.Draft.Title)
		}
	}
}
