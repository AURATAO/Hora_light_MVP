package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

type fakePurchases struct {
	yes   bool
	err   error
	asked []string
}

func (f *fakePurchases) NeedsPurchase(_ context.Context, text string) (bool, error) {
	f.asked = append(f.asked, text)
	return f.yes, f.err
}

var bagelD = llm.TaskDraft{Title: "Bagel from Russ & Daughters", Category: "delivery", Location1: "Russ & Daughters, Houston St", DurationMinutes: 30}

func TestAmountCents(t *testing.T) {
	for text, want := range map[string]int{
		"$20": 2000, "about $15 should do": 1500, "20 dollars": 2000, "budget of 60": 6000,
		"up to $30": 3000, "twenty bucks": 0, "no": 0, "$0": 0, "the 3rd floor": 0, "max 45": 4500,
	} {
		got, ok := AmountCents(text)
		if (want == 0 && ok) || (want > 0 && (!ok || got != want)) {
			t.Errorf("%q → %d ok=%v, want %d", text, got, ok, want)
		}
	}
}

func TestPurchaseRuleAndOptOut(t *testing.T) {
	for _, yes := range []string{"bring me a bagel from Russ & Daughters", "get me a coffee", "buy flowers for my mom", "pick up my prescription", "幫我買早餐"} {
		if !LooksLikePurchase(yes) {
			t.Errorf("%q should look like a purchase", yes)
		}
	}
	for _, no := range []string{"pick up my laundry", "wait in line at the DMV", "walk my mom to her appointment", "return a package to UPS"} {
		if LooksLikePurchase(no) {
			t.Errorf("%q must not look like a purchase", no)
		}
	}
	for _, s := range []string{"no extra time", "please ask me first", "don't go over", "不要超時"} {
		if !IsNoAutoExtend(s) {
			t.Errorf("%q should turn auto-extend off", s)
		}
	}
	if IsNoAutoExtend("yes") || IsNoAutoExtend("make it 4pm") {
		t.Error("ordinary replies must not touch auto-extend")
	}
}

// The incident: "bring me a bagel" → the chat asks how much to set aside,
// the amount goes on the task as the shopping budget, the hold covers it.
func TestPurchaseWithoutAmountAsksForABudget(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	got := g.say(t, "bring me a bagel from Russ & Daughters")
	if !strings.Contains(got, "how much should I set aside") || g.state() != StateAwaitingBudget {
		t.Fatalf("%q state %s", got, g.state())
	}
	if g.booker.posts != 0 {
		t.Fatal("nothing books before the budget is agreed")
	}
	got = g.say(t, "$15 is fine")
	if !strings.Contains(got, "Shopping budget: $15") || !strings.Contains(got, "Estimate: $27") || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", got, g.state())
	}
	g.say(t, "YES")
	if len(g.booker.opts) != 1 || g.booker.opts[0].BudgetCents != 1500 || !g.booker.opts[0].AutoExtend {
		t.Fatalf("posted with %+v", g.booker.opts)
	}
}

func TestAmountInTheMessageSkipsTheQuestion(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	got := g.say(t, "bring me a bagel from Russ & Daughters, budget $20")
	if g.state() != StateAwaitingConfirm || !strings.Contains(got, "Shopping budget: $20") {
		t.Fatalf("%q state %s", got, g.state())
	}
}

func TestNothingToBuyMeansNoBudget(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	g.say(t, "bring me a bagel from Russ & Daughters")
	got := g.say(t, "nothing, it's already paid")
	if strings.Contains(got, "Shopping budget") || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", got, g.state())
	}
	g.say(t, "YES")
	if g.booker.opts[0].BudgetCents != 0 {
		t.Fatalf("%+v", g.booker.opts)
	}
}

// The budget answer is parsed by rule: "maybe twenty" is not an amount.
func TestBudgetAnswerMustBeAnAmount(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	g.say(t, "bring me a bagel from Russ & Daughters")
	if got := g.say(t, "maybe twenty?"); got != CopyAskBudgetAgain || g.state() != StateAwaitingBudget {
		t.Fatalf("%q state %s", got, g.state())
	}
	if got := g.say(t, "YES"); got != CopyAskBudgetAgain || g.booker.posts != 0 {
		t.Fatalf("a YES is not an amount and books nothing: %q posts %d", got, g.booker.posts)
	}
}

// The model is a tiebreak only when the rule saw nothing; unavailable → no
// question, and the supporter can still ask for a budget from the app.
func TestModelTiebreakOnlyWhenRuleIsSilent(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	p := &fakePurchases{yes: true}
	g.r.Purchases = p
	g.parser.draft = laundry
	g.say(t, "pick up my laundry") // rule: no → model asked → yes
	if len(p.asked) != 1 || g.state() != StateAwaitingBudget {
		t.Fatalf("asked %v state %s", p.asked, g.state())
	}

	g = newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	p = &fakePurchases{yes: true}
	g.r.Purchases = p
	g.parser.draft = bagelD
	g.say(t, "bring me a bagel") // rule: yes → model not consulted
	if len(p.asked) != 0 {
		t.Fatalf("model consulted although the rule decided: %v", p.asked)
	}

	g = newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Purchases = &fakePurchases{err: errors.New("down")}
	g.parser.draft = laundry
	g.say(t, "pick up my laundry")
	// Review 10/24: unknown is asked, not assumed "no purchase" — the
	// budget question takes "nothing", and the draft goes on from there.
	if g.state() != StateAwaitingBudget {
		t.Fatalf("model down: the budget is asked: %s", g.state())
	}
	g.say(t, "nothing")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("after \"nothing\" the draft goes on: %s", g.state())
	}
}

func TestSummaryStatesAutoExtendAndOptOutHolds(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	got := g.say(t, "laundry pickup")
	if !strings.Contains(got, "up to 15 extra minutes are OK without asking") {
		t.Fatalf("summary must state the over-run consent:\n%s", got)
	}
	got = g.say(t, "no extra time please")
	if !strings.Contains(got, CopyAutoExtendOff) || strings.Contains(got, "extra minutes are OK") || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", got, g.state())
	}
	// A correction re-drafts; the opt-out survives it.
	got = g.say(t, "make it 4pm")
	if !strings.Contains(got, CopyAutoExtendOff) {
		t.Fatalf("opt-out lost on re-draft:\n%s", got)
	}
	g.say(t, "YES")
	if len(g.booker.opts) != 1 || g.booker.opts[0].AutoExtend {
		t.Fatalf("posted with %+v", g.booker.opts)
	}
}

// First-timer: name first, then the budget question, then the summary.
func TestFirstTimerNameThenBudget(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.parser.draft = bagelD
	if got := g.say(t, "bring me a bagel from Russ & Daughters"); !strings.Contains(got, "first name") {
		t.Fatalf("%q", got)
	}
	if got := g.say(t, "Aura"); !strings.Contains(got, "how much should I set aside") {
		t.Fatalf("%q", got)
	}
	if got := g.say(t, "$12"); !strings.Contains(got, "Reply YES to book") {
		t.Fatalf("%q", got)
	}
}

// The window figure is injected; an unconfigured router must not state a
// consent it cannot quantify.
func TestSummaryRefusesWithoutAutoExtendWindow(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.AutoExtendMinutes = 0
	in := Inbound{Channel: IMessage, ProviderMessageID: "w1", From: me, Text: "laundry pickup", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err == nil {
		t.Fatal("expected a configuration error")
	}
}

// Staging 2026-10-24: a correction ("can you do 15 mins?") keeps the budget
// already agreed; a correction that names an amount replaces it.
func TestCorrectionKeepsTheBudget(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	g.say(t, "bring me a bagel from Russ & Daughters")
	g.say(t, "10 usd is fine")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	got := g.say(t, "Can you do 15 mins?")
	if g.state() != StateAwaitingConfirm || !strings.Contains(got, "Shopping budget: $10") {
		t.Fatalf("budget asked again: %q state %s", got, g.state())
	}
	got = g.say(t, "actually make the budget $25")
	if !strings.Contains(got, "Shopping budget: $25") {
		t.Fatalf("new amount ignored: %q", got)
	}
}

// Arbiter 2026-10-24: no summary for a request with nothing in it or no
// place — the person is asked first; a YES then books nothing; the answer
// re-drafts. Staging: "Book another thing?" was summarised and booked.
func TestDetailsBeforeASummary(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Book another task", Category: "anything_else", DurationMinutes: 30}
	got := g.say(t, "Book another thing?")
	if got != CopyAskWhatWhere || g.state() != StateAwaitingConfirm {
		t.Fatalf("vague: %q state %s", got, g.state())
	}
	g.say(t, "YES")
	if g.booker.posts != 0 {
		t.Fatal("a YES must not book a draft that was never summarised")
	}
	g.parser.draft = bagelD
	got = g.say(t, "buy me a bagel to 260 Mercer street")
	if !strings.Contains(got, bagelD.Title) && !strings.Contains(got, "set aside") {
		t.Fatalf("after details: %q", got)
	}
	if last := g.parser.sources[len(g.parser.sources)-1]; strings.Contains(last, "Book another") {
		t.Fatalf("the empty request must not be re-parsed with the details: %q", last)
	}

	// A real errand with no place: asked where; the place re-drafts with
	// both lines.
	g = newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Hot Cheetos", Category: "delivery", DurationMinutes: 30}
	got = g.say(t, "buy me hot cheetos")
	if !strings.Contains(got, "Where should the supporter go") {
		t.Fatalf("no place: %q", got)
	}
	g.parser.draft = llm.TaskDraft{Title: "Hot Cheetos", Category: "delivery", Location1: "260 Mercer St", DurationMinutes: 30}
	g.say(t, "260 Mercer st")
	if last := g.parser.sources[len(g.parser.sources)-1]; !strings.Contains(last, "hot cheetos") || !strings.Contains(last, "260 Mercer") {
		t.Fatalf("re-draft source: %q", last)
	}
}
