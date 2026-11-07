package channel

import (
	"context"
	"strings"
	"testing"
)

func TestItemChoice(t *testing.T) {
	for in, want := range map[string]int{
		"1": 1, " 2 ": 2, "1.": 1, "2)": 2, "1️⃣": 1, "2!": 2,
		"12": 0, "3": 0, "yes": 0, "1 please": 0, "option 1": 0, "": 0, "STOP": 0, "cancel": 0,
	} {
		if got := ItemChoice(in); got != want {
			t.Errorf("ItemChoice(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestDecideItemChoice(t *testing.T) {
	ok := ApprovalFacts{QuestionIsLast: true}
	cases := []struct {
		text string
		f    ApprovalFacts
		want ApprovalVerdict
	}{
		{"1", ok, VerdictApprove},
		{"2", ok, VerdictDecline},
		{"yes", ok, VerdictReask}, // YES says nothing to "1 or 2?"
		{"no", ok, VerdictReask},
		{"what brand is it?", ok, VerdictNotAnAnswer},
		{"1", ApprovalFacts{Unconfirmed: true}, VerdictUnconfirmed},
		{"2", ApprovalFacts{SentBefore: true}, VerdictSentBefore},
		{"1", ApprovalFacts{SwipedElsewhere: true}, VerdictReask},
	}
	for _, c := range cases {
		c.f.Text = c.text
		if got := DecideItemChoice(c.f); got != c.want {
			t.Errorf("%q %+v: got %d want %d", c.text, c.f, got, c.want)
		}
	}
}

func TestItemQuestionText(t *testing.T) {
	ev := StatusEvent{Type: evItemProposed, ExtensionID: "e1", ApprovalMinutes: 5, Approval: &PendingApproval{
		ID: "e1", Kind: "item", Item: "oat milk", Store: "Trader Joe's", Alternative: "almond milk", Cents: 449,
	}}
	got, ok := StatusText(ev)
	want := "Trader Joe's is out of oat milk. Your supporter found almond milk ($4.49).\n\nReply 1 for almond milk, 2 to skip it. No answer within 5 minutes: your supporter will decide."
	if !ok || got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "YES") {
		t.Error("an item question mentions YES")
	}
	ev.Approval.Store = ""
	if got, _ := StatusText(ev); !strings.HasPrefix(got, "Oat milk is out of stock.") {
		t.Errorf("no store: %q", got)
	}
	ev.Approval.Alternative = ""
	if _, ok := StatusText(ev); ok {
		t.Error("sent without an alternative")
	}
}

func TestItemUnavailableIsANotice(t *testing.T) {
	got, ok := StatusText(StatusEvent{Type: evItemUnavailable, TaskID: "t", Body: "Trader Joe's is out of oat milk, and there's nothing similar. Your supporter will skip it."})
	if !ok || !strings.HasPrefix(got, "Trader Joe's is out of oat milk") {
		t.Fatalf("got %q %v", got, ok)
	}
}

func askItem(t *testing.T, g *rig) string {
	t.Helper()
	q := PendingApproval{ID: "ext-9", Kind: "item", Item: "oat milk", Store: "Trader Joe's", Alternative: "almond milk", Cents: 449}
	g.booker.mu.Lock()
	g.booker.items = map[string]PendingApproval{"ext-9": q}
	g.booker.mu.Unlock()
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{
		Type: evItemProposed, TaskID: "task-1", TaskTitle: "Groceries", SupporterName: "Maya",
		ExtensionID: "ext-9", ApprovalMinutes: 5, Approval: &q,
	}); err != nil {
		t.Fatal(err)
	}
	return g.ad.last()
}

func TestItemQuestionIsAnsweredByDigits(t *testing.T) {
	g := approvalRig(t)
	got := askItem(t, g)
	if !strings.Contains(got, "Reply 1 for almond milk, 2 to skip it") || g.state() != StateAwaitingApproval {
		t.Fatalf("question %q state %s", got, g.state())
	}
	// YES / NO and sentences decide nothing; asked again in digits.
	for _, msg := range []string{"yes", "no", "maybe"} {
		reply := g.say(t, msg)
		if len(g.booker.resolved) != 0 {
			t.Fatalf("%q decided %v", msg, g.booker.resolved)
		}
		if !strings.Contains(reply, "reply 1 for almond milk, 2 to skip it") || g.state() != StateAwaitingApproval {
			t.Fatalf("%q → %q state %s", msg, reply, g.state())
		}
	}
	reply := g.say(t, "1")
	if len(g.booker.resolved) != 1 || g.booker.resolved[0] != "user-1:ext-9:true" {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
	if !strings.HasPrefix(reply, "Got it:") || !strings.Contains(reply, "almond milk instead") {
		t.Fatalf("%q", reply)
	}
	if g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
}

func TestItemQuestionTwoSkips(t *testing.T) {
	g := approvalRig(t)
	askItem(t, g)
	reply := g.say(t, "2")
	if len(g.booker.resolved) != 1 || g.booker.resolved[0] != "user-1:ext-9:false" || !strings.Contains(reply, "skip oat milk") {
		t.Fatalf("%v %q", g.booker.resolved, reply)
	}
}

// The other questions keep YES/NO: a digit answers nothing there.
func TestDigitsDoNotAnswerAYesNoQuestion(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	g.say(t, "1")
	if len(g.booker.resolved) != 0 {
		t.Fatalf("a digit answered a YES/NO question: %v", g.booker.resolved)
	}
}
