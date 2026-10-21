package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// D-17: with a supporter on the job, the thread's context decides.

type fakeContext struct {
	intent llm.Intent
	err    error
	calls  []llm.Context
}

func (f *fakeContext) ClassifyInContext(_ context.Context, _ string, c llm.Context) (llm.Intent, error) {
	f.calls = append(f.calls, c)
	if f.err != nil {
		return llm.IntentToSupporter, f.err
	}
	return f.intent, nil
}

func contextRig(intent llm.Intent, active ...ActiveTask) (*rig, *fakeChat, *fakeTasks, *fakeContext) {
	g, chat, tasks := relayRig(llm.IntentNewTask, active...) // the plain classifier would draft
	fc := &fakeContext{intent: intent}
	g.r.InContext = fc
	return g, chat, tasks, fc
}

// The incident: the supporter asked "what kind of bagel?"; "I want
// everything 🥯" is for them — relayed, no draft, no budget question.
func TestShortReplyToSupportersQuestionIsRelayedByRule(t *testing.T) {
	g, chat, tasks, fc := contextRig(llm.IntentNewTask, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: what kind of bagel you want?", "task-2", time.Now().Add(-2*time.Minute)
	g.say(t, "I want everything 🥯")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:I want everything 🥯" || g.state() != StateIdle || g.booker.posts != 0 {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
	if len(fc.calls) != 0 {
		t.Fatal("a short answer to a fresh question needs no model")
	}
	// Eleven minutes later the rule no longer applies; the model decides.
	tasks.lastAt = time.Now().Add(-11 * time.Minute)
	fc.intent = llm.IntentToSupporter
	g.say(t, "actually sesame please")
	if len(fc.calls) != 1 || len(chat.posted) != 2 {
		t.Fatalf("calls %d posted %v", len(fc.calls), chat.posted)
	}
	if fc.calls[0].LastSupporterLine != "what kind of bagel you want?" || len(fc.calls[0].ActiveTasks) != 1 || fc.calls[0].ActiveTasks[0].SupporterName != "Sam" {
		t.Fatalf("context %+v", fc.calls[0])
	}
}

// The model decides between the supporter and a new errand; a new errand
// drafts with the way out on its first line, and "for Sam" takes it.
func TestContextClassifierAndTheWayOut(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentToSupporter, bagelT)
	g.say(t, "leave it with the doorman")
	if len(chat.posted) != 1 || g.state() != StateIdle {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}

	fc.intent = llm.IntentNewTask
	got := g.say(t, "pick up my laundry tomorrow at 3")
	if g.state() != StateAwaitingConfirm || !strings.HasPrefix(got, "Booking this as a new errand. If it was for Sam instead, reply “for Sam”.") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
	got = g.say(t, "for Sam")
	if len(chat.posted) != 2 || chat.posted[1] != "task-2:pick up my laundry tomorrow at 3" || g.state() != StateIdle || g.booker.posts != 0 {
		t.Fatalf("posted %v state %s reply %q", chat.posted, g.state(), got)
	}
	if !strings.Contains(got, "Passed that on to Sam") {
		t.Fatalf("%q", got)
	}
	// "new task:" forces a draft even when the model would relay.
	fc.intent = llm.IntentToSupporter
	got = g.say(t, "new task: walk my dog at 5")
	if g.state() != StateAwaitingConfirm || !strings.Contains(got, "Reply YES") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
	// The lead-in shows once, not on every re-draft.
	got = g.say(t, "make it 6")
	if strings.Contains(got, "Booking this as a new errand") {
		t.Fatalf("lead-in repeated: %q", got)
	}
}

// P7: a classifier failure goes to the supporter, never to a draft.
func TestContextClassifierFailureFailsTowardTheHuman(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentNewTask, bagelT)
	fc.err = errors.New("output truncated")
	g.say(t, "I want everything bagel")
	if len(chat.posted) != 1 || g.state() != StateIdle || g.booker.posts != 0 {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
}

// Mid-question (budget / confirm) with a supporter on the job: a sentence
// for them is relayed and the question stays open, re-stated in one line.
func TestSentenceMidQuestionIsRelayedAndTheQuestionStays(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentNewTask, bagelT)
	g.parser.draft = bagelD
	g.say(t, "new task: bring me a bagel from Russ & Daughters") // → awaiting_budget
	if g.state() != StateAwaitingBudget {
		t.Fatalf("state %s", g.state())
	}
	fc.intent = llm.IntentToSupporter
	got := g.say(t, "Sam, take your time")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:Sam, take your time" || g.state() != StateAwaitingBudget || got != CopyBudgetStillPending {
		t.Fatalf("posted %v state %s reply %q", chat.posted, g.state(), got)
	}
	g.say(t, "$12") // the budget answer still works
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	got = g.say(t, "thanks!")
	if len(chat.posted) != 2 || g.state() != StateAwaitingConfirm || got != CopyDraftStillPending {
		t.Fatalf("posted %v state %s reply %q", chat.posted, g.state(), got)
	}
	// A plain YES still books; nothing of the relay touched the draft.
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("booked %d", g.booker.posts)
	}
}

// P4: the supporter's request interrupts our budget question; the YES
// answers it; the budget question comes back. Nothing is eaten.
func TestApprovalInterruptsTheBudgetQuestion(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.parser.draft = bagelD
	g.say(t, "new task: bring me a bagel from Russ & Daughters")
	if g.state() != StateAwaitingBudget {
		t.Fatalf("state %s", g.state())
	}
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-7", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-7", Kind: "budget", Cents: 1000, Reason: "price went up"}}); err != nil {
		t.Fatal(err)
	}
	if g.state() != StateAwaitingApproval || !strings.Contains(g.ad.last(), "Sam is asking for $10 more") {
		t.Fatalf("state %s last %q", g.state(), g.ad.last())
	}
	got := g.say(t, "no")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-7:false") {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
	if g.state() != StateAwaitingBudget || !strings.Contains(got, CopyBackToDraft) || !strings.Contains(got, "how much should I set aside") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
	g.say(t, "nothing")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
}

// P4 / I-O: a request that cannot be armed is not sent to the phone.
func TestUnarmableApprovalIsNotSent(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.r.Ops, g.r.Thread = &memOps{}, &memThread{st: g.st}
	g.say(t, "I want to talk to a human")
	if g.state() != StateHandoff {
		t.Fatalf("state %s", g.state())
	}
	sent := len(g.ad.sent)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evTimeRequested, TaskID: "task-2", SupporterName: "Sam",
		ExtensionID: "ext-8", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-8", Kind: "time", Minutes: 15}}); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != sent || g.state() != StateHandoff {
		t.Fatalf("sent %d state %s", len(g.ad.sent)-sent, g.state())
	}
}

// Name question survives an interruption too.
func TestApprovalInterruptsTheNameQuestion(t *testing.T) {
	g := newRig(llm.IntentNewTask) // not linked: a first-timer
	g.say(t, "laundry pickup")
	if g.state() != StateAwaitingName {
		t.Fatalf("state %s", g.state())
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if !c.Pending.NameAsked || c.ResumeState() != StateAwaitingName {
		t.Fatalf("%+v", c.Pending)
	}
	g.say(t, "Aura")
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.Pending.NameAsked || c.ResumeState() != StateAwaitingConfirm {
		t.Fatalf("%+v", c.Pending)
	}
}

// P5: the footer never tells an iMessage user to "reply to this message".
func TestFooterPerChannel(t *testing.T) {
	for _, c := range []struct {
		ch   Channel
		want string
	}{{IMessage, "just write your reply"}, {WhatsApp, "reply to this message"}} {
		g, _, _ := relayRig(llm.IntentNewTask, laundryT)
		g.say(t, "laundry pickup")
		g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: c.ch, Address: me}, LastInboundAt: ptrTime(time.Now())}}
		g.r.Adapters[c.ch] = g.ad
		if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry", "large bag too?", ""); err != nil {
			t.Fatal(err)
		}
		if got := g.ad.last(); !strings.Contains(got, c.want) {
			t.Fatalf("%s: %q", c.ch, got)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
