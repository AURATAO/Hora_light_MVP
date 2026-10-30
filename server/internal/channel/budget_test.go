package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	// Staging 2026-10-24 22:48: the re-parse of a correction worded the
	// place differently ("Farmer's Market") and the budget was asked again.
	// Same kind of errand: kept.
	reworded := bagelD
	reworded.Location1 = "Russ and Daughters, Houston St"
	g.parser.draft = reworded
	got = g.say(t, "not asap, on Friday")
	if g.state() != StateAwaitingConfirm || !strings.Contains(got, "Shopping budget: $25") {
		t.Fatalf("budget asked again after a reworded place: %q state %s", got, g.state())
	}
}

// Arbiter 2026-10-24 22:48: 30 minutes unless the person says how long —
// the model's own guess ("15" for a pack of eggs) is not used.
func TestDefaultThirtyMinutesUnlessNamed(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	guess := laundry
	guess.DurationMinutes = 15
	g.parser.draft = guess
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.Pending == nil || c.Pending.Items()[0].Draft.DurationMinutes != 0 {
		t.Fatalf("the model's guess was kept: %+v", c.Pending)
	}
	g.say(t, "make it 45 minutes")
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	// The fake parser still says 15; the newest duration-only line sets it
	// (review 2026-10-30), so the person's 45 wins.
	if c.Pending.Items()[0].Draft.DurationMinutes != 45 {
		t.Fatalf("the named duration must be the one said: %+v", c.Pending.Items()[0].Draft)
	}
	for _, s := range []string{"15 mins", "an hour", "half an hour", "2 hours", "20分鐘", "一個小時"} {
		if !namesDuration(s) {
			t.Errorf("%q names a duration", s)
		}
	}
	for _, s := range []string{"a pack of eggs", "at 5pm", "on Friday", "15 Bedford Ave"} {
		if namesDuration(s) {
			t.Errorf("%q does not name a duration", s)
		}
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

// firstLineSplitter is the real splitter's behaviour on a correction: it
// keeps the errand's words and drops the follow-up lines.
type firstLineSplitter struct{}

func (firstLineSplitter) SplitErrands(_ context.Context, text string) ([]string, llm.Usage, error) {
	return []string{strings.SplitN(text, "\n", 2)[0]}, llm.Usage{Model: "fake"}, nil
}

// Staging 2026-10-30: "15 mins" said three times after the summary; the
// splitter dropped it, so the parser never saw it and the draft stayed 30.
func TestCorrectionDurationReachesTheParser(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Splitter = firstLineSplitter{}
	named := laundry
	named.DurationMinutes = 15
	g.parser.draft = named
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	g.say(t, "15 mins")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.Pending == nil || c.Pending.Items()[0].Draft.DurationMinutes != 15 {
		t.Fatalf("the named duration was dropped: %+v", c.Pending)
	}
	last := g.parser.sources[len(g.parser.sources)-1]
	if !strings.Contains(last, "15 mins") {
		t.Fatalf("the parser did not see the correction: %q", last)
	}
}

func TestNoExtraAloneTurnsOffAutoExtend(t *testing.T) {
	for _, s := range []string{"No extra", "no extra.", "No extras", "no extra time"} {
		if !IsNoAutoExtend(s) {
			t.Errorf("%q should turn off the auto-extension", s)
		}
	}
	for _, s := range []string{"no extra stops", "no extra charge for the bag", "no"} {
		if IsNoAutoExtend(s) {
			t.Errorf("%q must not turn it off", s)
		}
	}
}

// Review 2026-10-30 (c189ef8): the newest duration-only line wins over the
// model's reading of older lines.
func TestNewestDurationLineWins(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Splitter = firstLineSplitter{}
	old := laundry
	old.DurationMinutes = 120 // the model anchored on the first line
	g.parser.draft = old
	g.say(t, "pick up my laundry at 12 Bedford Ave, 2 hours")
	g.say(t, "ASAP 15 mins")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if got := c.Pending.Items()[0].Draft.DurationMinutes; got != 15 {
		t.Fatalf("minutes = %d, want 15 (the newest line)", got)
	}
}

func TestDurationOnly(t *testing.T) {
	want := map[string]int{"15 mins": 15, "About 15 mins": 15, "ASAP 15 mins": 15, "make it 45 minutes": 45,
		"Can you do 15 mins?": 15, "an hour": 60, "2 hours please": 120, "20分鐘": 20, "半小時": 30}
	for s, n := range want {
		if got, ok := DurationOnly(s); !ok || got != n {
			t.Errorf("%q = %d,%v want %d", s, got, ok, n)
		}
	}
	for _, s := range []string{"I'll be there in 10 min", "store closes in 20 minutes", "15", "1 min", "pick up 15 mins"} {
		if got, ok := DurationOnly(s); ok {
			t.Errorf("%q must not be a duration-only line (got %d)", s, got)
		}
	}
}

func TestAmountSkipsDurations(t *testing.T) {
	if c, ok := AmountCents("up to 15 mins?"); ok {
		t.Errorf("up to 15 mins read as %d cents", c)
	}
	if c, ok := AmountCents("up to 20 mins, max $30"); !ok || c != 3000 {
		t.Errorf("got %d,%v want 3000", c, ok)
	}
	if c, ok := LatestAmountCents("buy bagels $20\nactually budget $35"); !ok || c != 3500 {
		t.Errorf("latest amount %d,%v want 3500", c, ok)
	}
}

func TestNoExtraVariants(t *testing.T) {
	for _, s := range []string{"no extras please", "no, no extra", "No extra please", "no overtime"} {
		if !IsNoAutoExtend(s) {
			t.Errorf("%q should turn off the auto-extension", s)
		}
	}
}

// Review r2: a later non-duration correction keeps the duration line before
// it; "$0" after an amount wins.
func TestDurationLineSurvivesALaterCorrection(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Splitter = firstLineSplitter{}
	guess := laundry
	guess.DurationMinutes = 120
	g.parser.draft = guess
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	g.say(t, "15 mins")
	g.say(t, "make it 4pm")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if got := c.Pending.Items()[0].Draft.DurationMinutes; got != 15 {
		t.Fatalf("minutes = %d, want 15", got)
	}
	if c, ok := LatestAmountCents("buy bagels $20\n$0"); !ok || c != 0 {
		t.Fatalf("$0 after $20 = %d,%v want 0,true", c, ok)
	}
}

// Production 2026-10-30: "Oh sorry I change my mind never mind I don't need
// it" to the budget question was relayed to an old task's supporter. It
// drops the draft; "no thanks" still answers the question.
func TestDeclineAtTheBudgetQuestionDropsTheDraft(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	g.say(t, "bring me a bagel from Russ & Daughters")
	if g.state() != StateAwaitingBudget {
		t.Fatalf("state %s", g.state())
	}
	if got := g.say(t, "Oh sorry I change my mind never mind I don’t need it"); got != CopyDropped || g.state() != StateIdle {
		t.Fatalf("reply %q state %s", got, g.state())
	}
	g.say(t, "bring me a bagel from Russ & Daughters")
	g.say(t, "no thanks")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("\"no thanks\" must answer the budget (nothing to buy): state %s", g.state())
	}
}

func TestYesWithThanksIsYes(t *testing.T) {
	for _, s := range []string{"Yes thanks", "yes, thank you!", "ok thx", "YES please"} {
		if !IsYes(s) {
			t.Errorf("%q should be a YES", s)
		}
	}
	for _, s := range []string{"yes but max $5", "yesterday", "yes if it's organic"} {
		if IsYes(s) {
			t.Errorf("%q must not be a plain YES", s)
		}
	}
}

// One sender's messages are routed one at a time, in order; another
// sender's are not held up.
func TestKeyedSerialRunsInOrderPerKey(t *testing.T) {
	k := &keyedSerial{queues: map[string][]func(){}}
	var mu sync.Mutex
	var got []string
	release := make(chan struct{})
	done := make(chan struct{}, 4)
	k.do("a", func() { <-release; mu.Lock(); got = append(got, "a1"); mu.Unlock(); done <- struct{}{} })
	k.do("a", func() { mu.Lock(); got = append(got, "a2"); mu.Unlock(); done <- struct{}{} })
	k.do("b", func() { mu.Lock(); got = append(got, "b1"); mu.Unlock(); done <- struct{}{} })
	<-done // b1 runs while a1 is still blocked
	close(release)
	<-done
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0] != "b1" || got[1] != "a1" || got[2] != "a2" {
		t.Fatalf("order %v, want [b1 a1 a2]", got)
	}
}
