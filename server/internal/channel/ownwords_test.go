package channel

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// Gap C (2026-11-04): with several errands in one message, the budget and
// the duration are read from the splitter's copy of each errand. A figure
// the person never wrote is not used.

// rewritingSplitter answers with the model's own copy of the errands,
// whatever the message said.
type rewritingSplitter struct{ errands []string }

func (s rewritingSplitter) SplitErrands(context.Context, string) ([]string, llm.Usage, error) {
	return s.errands, llm.Usage{Model: "fake"}, nil
}

func splitRig(t *testing.T, errands []string, drafts map[string]llm.TaskDraft) *rig {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Splitter = rewritingSplitter{errands: errands}
	g.parser.byText = drafts
	return g
}

func TestSplitAmountNotInTheMessageAsksForTheBudget(t *testing.T) {
	g := splitRig(t, []string{"pick up my laundry", "get groceries with $400"},
		map[string]llm.TaskDraft{"pick up my laundry": laundryD, "get groceries with $400": groceryD})
	got := g.say(t, "pick up my laundry and get groceries with $40")
	if want := fmt.Sprintf(CopyAskBudgetFor, "Grocery run"); got != want || g.state() != StateAwaitingBudget {
		t.Fatalf("reply %q state %s, want the budget question", got, g.state())
	}
	if it := g.pending(t).Items()[1]; it.BudgetCents != 0 || it.BudgetAsked {
		t.Fatalf("the model's $400 was used: %+v", it)
	}
	// The answer, in the person's words, sets it.
	if got := g.say(t, "$40"); !strings.Contains(got, "Shopping budget: $40") {
		t.Fatalf("summary: %q", got)
	}
}

func TestSplitAmountInTheMessageIsUsed(t *testing.T) {
	for _, original := range []string{
		"pick up my laundry and get groceries with $40",
		"pick up my laundry and get groceries, 40 dollars",
		"pick up my laundry and get groceries, 40 usd max",
		"pick up my laundry and get groceries, budget 40",
		"pick up my laundry and get groceries, about forty dollars",
		"pick up my laundry and get groceries, spend forty tops",
		"pick up my laundry and get groceries for $40.00",
	} {
		g := splitRig(t, []string{"pick up my laundry", "get groceries with $40"},
			map[string]llm.TaskDraft{"pick up my laundry": laundryD, "get groceries with $40": groceryD})
		got := g.say(t, original)
		if !strings.Contains(got, "Shopping budget: $40") || g.state() != StateAwaitingConfirm {
			t.Errorf("%q: %q state %s, want the $40 used", original, got, g.state())
		}
	}
}

func TestSplitAmountWrittenInWords(t *testing.T) {
	g := splitRig(t, []string{"return a package", "get groceries, $45 budget"},
		map[string]llm.TaskDraft{"return a package": returnD, "get groceries, $45 budget": groceryD})
	if got := g.say(t, "return a package to UPS, and groceries — forty-five bucks"); !strings.Contains(got, "Shopping budget: $45") {
		t.Fatalf("forty-five: %q", got)
	}
	h := splitRig(t, []string{"return a package", "get groceries, $120 budget"},
		map[string]llm.TaskDraft{"return a package": returnD, "get groceries, $120 budget": groceryD})
	if got := h.say(t, "return a package and groceries, one hundred and twenty dollars"); !strings.Contains(got, "Shopping budget: $120") {
		t.Fatalf("one hundred and twenty: %q", got)
	}
	// "forty" for "$400" is not the same number.
	k := splitRig(t, []string{"return a package", "get groceries, $400 budget"},
		map[string]llm.TaskDraft{"return a package": returnD, "get groceries, $400 budget": groceryD})
	if got := k.say(t, "return a package and groceries, forty dollars"); k.state() != StateAwaitingBudget {
		t.Fatalf("forty read as $400: %q", got)
	}
}

// A "nothing to buy" the person never said does not settle the budget.
func TestSplitNoBudgetNotInTheMessageAsks(t *testing.T) {
	g := splitRig(t, []string{"pick up my laundry", "get groceries, already paid"},
		map[string]llm.TaskDraft{"pick up my laundry": laundryD, "get groceries, already paid": groceryD})
	if g.say(t, "pick up my laundry and get groceries"); g.state() != StateAwaitingBudget {
		t.Fatalf("state %s, want the budget question", g.state())
	}
}

func TestSplitDurationNotInTheMessageIsTheDefault(t *testing.T) {
	long := groceryD
	long.DurationMinutes = 120
	g := splitRig(t, []string{"pick up my laundry", "get groceries with $40, 2 hours"},
		map[string]llm.TaskDraft{"pick up my laundry": laundryD, "get groceries with $40, 2 hours": long})
	g.say(t, "pick up my laundry and get groceries with $40, 20 minutes")
	if m := g.pending(t).Items()[1].Draft.DurationMinutes; m != 0 {
		t.Fatalf("minutes %d, want the default (0) — 2 hours was never said", m)
	}

	said := groceryD
	said.DurationMinutes = 90
	h := splitRig(t, []string{"pick up my laundry", "get groceries with $40, 1.5 hours"},
		map[string]llm.TaskDraft{"pick up my laundry": laundryD, "get groceries with $40, 1.5 hours": said})
	h.say(t, "pick up my laundry and get groceries with $40, an hour and a half")
	if m := h.pending(t).Items()[1].Draft.DurationMinutes; m != 90 {
		t.Fatalf("minutes %d, want 90 (an hour and a half)", m)
	}
}

func TestSaidAmount(t *testing.T) {
	yes := map[string]int{
		"$40": 40, "$ 40": 40, "40$": 40, "40 dollars": 40, "40 USD": 40, "40 bucks": 40, "forty": 40,
		"Forty dollars": 40, "forty-five": 45, "twenty five bucks": 25, "a hundred": 100, "$1,200": 1200,
		"budget of 60": 60, "up to 30": 30, "$12.50": 12, "spend 15": 15,
	}
	for s, n := range yes {
		if !saidAmount(s, n) {
			t.Errorf("%q should name $%d", s, n)
		}
	}
	no := map[string]int{
		"$40": 400, "forty": 400, "40 minutes": 40, "up to 15 mins": 15, "at 10am": 10, "forty minutes": 40,
		"12 Bedford Ave": 12, "2 bags": 2, "": 40, "five ten": 510,
	}
	for s, n := range no {
		if saidAmount(s, n) {
			t.Errorf("%q must not name $%d", s, n)
		}
	}
}

func TestDurationsIn(t *testing.T) {
	yes := map[string]int{
		"20 minutes": 20, "20 min": 20, "2 hours": 120, "2h": 120, "1.5 hours": 90, "an hour": 60,
		"half an hour": 30, "an hour and a half": 90, "1 hour 30 min": 90, "1h30": 90, "forty five minutes": 45,
		"two hours": 120, "a couple of hours": 120, "20分鐘": 20, "兩個小時": 120, "半小時": 30, "十五分鐘": 15,
	}
	for s, n := range yes {
		if !saidMinutesIn(s, n) {
			t.Errorf("%q should name %d minutes (got %v)", s, n, durationsIn(s))
		}
	}
	no := map[string]int{"20 minutes": 120, "2 hours": 20, "$40": 40, "at 10am": 600, "forty": 40}
	for s, n := range no {
		if saidMinutesIn(s, n) {
			t.Errorf("%q must not name %d minutes", s, n)
		}
	}
}
