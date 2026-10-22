package channel

import (
	"context"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// D-17 R-1: the double tap. A draft waits for YES; an approval interrupts;
// two YESes go out together. The first approves; the second — sent before
// the summary was put again — must not book.
func TestDoubleTapYesDoesNotBook(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3") // awaiting_confirm
	g.arm(t, "ext-2")
	tap := time.Now()
	time.Sleep(5 * time.Millisecond) // both taps were sent before the first is processed
	for i, id := range []string{"tap-1", "tap-2"} {
		in := Inbound{Channel: IMessage, ProviderMessageID: id, From: me, Text: "YES", ReceivedAt: tap.Add(time.Duration(i) * time.Millisecond)}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.booker.resolved) != 1 || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("resolved %v posts %d state %s", g.booker.resolved, g.booker.posts, g.state())
	}
	if !strings.Contains(g.ad.last(), "YES") {
		t.Fatalf("the summary must be put again: %q", g.ad.last())
	}
	// A YES sent now, after reading it, books.
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("posts %d", g.booker.posts)
	}
}

// R-1: a YES with no usable send time is not a booking either.
func TestBookingYesWithoutSendTimeIsReasked(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	if err := g.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: "nt", From: me, Text: "YES"}); err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posts %d state %s", g.booker.posts, g.state())
	}
}

// R-2: a no with a reason, and "no thanks", decline; "no problem" never
// does; with the supporter mid-conversation only a bare no declines.
func TestApprovalNoWithReason(t *testing.T) {
	for _, tc := range []struct {
		msg      string
		declined bool
	}{
		{"No, he needs to finish now", true}, {"no thanks", true}, {"nope, too much", true},
		{"no problem", false}, {"no worries", false},
	} {
		g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		g.say(t, tc.msg)
		got := len(g.booker.resolved) == 1 && strings.HasSuffix(g.booker.resolved[0], ":ext-2:false")
		if got != tc.declined || (!tc.declined && len(g.booker.resolved) != 0) {
			t.Errorf("%q: resolved %v", tc.msg, g.booker.resolved)
		}
	}
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: the blue one?", "task-2", time.Now().Add(-time.Minute)
	g.say(t, "no, the red one")
	if len(g.booker.resolved) != 0 || len(chat.posted) != 1 {
		t.Fatalf("resolved %v posted %v", g.booker.resolved, chat.posted)
	}
}

// R-3: the widened yes list approves.
func TestApprovalWidenedYes(t *testing.T) {
	for _, msg := range []string{"Sounds good!", "Yesss", "100%", "Kk", "of course", "I'm down", "可以"} {
		g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		g.say(t, msg)
		if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-2:true") {
			t.Errorf("%q: resolved %v", msg, g.booker.resolved)
		}
	}
}

// R-3: a reaction word counts only when our last message is the question.
func TestReactionWordOnlyRightAfterTheQuestion(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	g.say(t, "Great")
	if len(g.booker.resolved) != 1 {
		t.Fatalf("right after the question: resolved %v", g.booker.resolved)
	}
	// A status update follows the question: "Great" is about that.
	g, _, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "CLOCK_IN", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam"}); err != nil {
		t.Fatal(err)
	}
	g.say(t, "👍")
	if len(g.booker.resolved) != 0 || g.state() != StateAwaitingApproval || g.ad.last() != CopyApprovalReask {
		t.Fatalf("after a status update: resolved %v state %s last %q", g.booker.resolved, g.state(), g.ad.last())
	}
	// A plain consent word is not limited.
	g.say(t, "sure")
	if len(g.booker.resolved) != 1 {
		t.Fatalf("plain word: resolved %v", g.booker.resolved)
	}
}

// R-3: yes + a message approves and passes the message on; with a
// condition nothing is approved or declined, the words go to the
// supporter, the request stays open.
func TestYesWithAMessage(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	got := g.say(t, "yes but tell him to hurry")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-2:true") ||
		len(chat.posted) != 1 || chat.posted[0] != "task-2:but tell him to hurry" || !strings.Contains(got, "Passed") {
		t.Fatalf("resolved %v posted %v reply %q", g.booker.resolved, chat.posted, got)
	}
	for _, msg := range []string{"yes but max $5", "yes if it's organic", "ok but not the money", "sure, why?"} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		got := g.say(t, msg)
		if len(g.booker.resolved) != 0 || g.state() != StateAwaitingApproval || len(chat.posted) != 1 ||
			chat.posted[0] != "task-2:"+msg || !strings.Contains(got, CopyStillWaitingApproval) {
			t.Errorf("%q: resolved %v state %s posted %v reply %q", msg, g.booker.resolved, g.state(), chat.posted, got)
		}
	}
	// Reaction word + message after a later message of ours: re-asked.
	g, chat, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "CLOCK_IN", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam"}); err != nil {
		t.Fatal(err)
	}
	g.say(t, "great, he's here")
	if len(g.booker.resolved) != 0 || len(chat.posted) != 0 {
		t.Fatalf("resolved %v posted %v", g.booker.resolved, chat.posted)
	}
}

// R-3 under I-P: with the supporter mid-conversation, the wider words go
// to the supporter; the strict YES still answers us.
func TestWidenedWordsRespectIP(t *testing.T) {
	for _, msg := range []string{"sounds good", "yes but tell him to hurry", "Great"} {
		g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: large bag?", "task-2", time.Now().Add(-time.Minute)
		g.say(t, msg)
		if len(g.booker.resolved) != 0 || len(chat.posted) != 1 {
			t.Errorf("%q: resolved %v posted %v", msg, g.booker.resolved, chat.posted)
		}
	}
}

// The booking YES keeps its own list: R-2/R-3 are scoped to approvals.
func TestBookingKeepsItsList(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	g.say(t, "Great")
	if g.booker.posts != 0 {
		t.Fatalf("a reaction word must not book: posts %d", g.booker.posts)
	}
}
