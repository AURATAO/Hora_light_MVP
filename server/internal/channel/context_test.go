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
	if g.state() != StateAwaitingConfirm || !strings.HasPrefix(got, "Booking this as a new errand. If it was for your supporter instead, reply “for Sam”.") {
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
	// A correction re-drafts (the model says so, with the open question in
	// view) and the lead-in is not repeated.
	fc.intent = llm.IntentCorrection
	got = g.say(t, "make it 6")
	if g.state() != StateAwaitingConfirm || !strings.Contains(got, "Reply YES") || strings.Contains(got, "Booking this as a new errand") || len(chat.posted) != 2 {
		t.Fatalf("state %s reply %q posted %v", g.state(), got, chat.posted)
	}
	if fc.calls[len(fc.calls)-1].OpenQuestion != "booking" {
		t.Fatalf("the model must know a booking question is open: %+v", fc.calls[len(fc.calls)-1])
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

// Name question survives an interruption too: the approval is armed over
// it, answered, and the name question comes back.
func TestApprovalInterruptsTheNameQuestion(t *testing.T) {
	g := newRig(llm.IntentNewTask) // not linked: a first-timer
	g.say(t, "laundry pickup")
	if g.state() != StateAwaitingName {
		t.Fatalf("state %s", g.state())
	}
	g.arm(t, "ext-3")
	if g.state() != StateAwaitingApproval {
		t.Fatalf("state %s", g.state())
	}
	got := g.say(t, "YES")
	if len(g.booker.resolved) != 1 || g.state() != StateAwaitingName || !strings.Contains(got, "first name") {
		t.Fatalf("resolved %v state %s reply %q", g.booker.resolved, g.state(), got)
	}
	g.say(t, "Aura")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.Pending.NameAsked || c.ResumeState() != StateAwaitingConfirm {
		t.Fatalf("%+v", c.Pending)
	}
}

// P5: the footer never tells an iMessage user to "reply to this message".
func TestFooterPerChannel(t *testing.T) {
	for _, c := range []struct {
		ch   Channel
		want string
	}{{IMessage, "just write. To answer me"}, {WhatsApp, "reply to this message or just write"}} {
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

// ── review round 2 (D-17) ──────────────────────────────────────────────────

// MAJOR-1: a sentence while an approval is pending reaches the supporter;
// the question stays open.
func TestSentenceDuringApprovalIsRelayed(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentToSupporter, bagelT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.PendingExtensionID = StateAwaitingApproval, "ext-1"
	c.ApprovalArmedAt = ptrTime(time.Now().Add(-time.Minute)) // a delivered question
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got := g.say(t, "why $10 more?")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:why $10 more?" || g.state() != StateAwaitingApproval || got != CopyApprovalStillPending {
		t.Fatalf("posted %v state %s reply %q", chat.posted, g.state(), got)
	}
	if fc.calls[0].OpenQuestion != "approval" {
		t.Fatalf("%+v", fc.calls[0])
	}
	g.say(t, "NO")
	if len(g.booker.resolved) != 1 {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
}

// MAJOR-2: the supporter's last line is a target only while that task is
// active; several supporters and no signal → the list (idle) or a line
// naming the way out (mid-question); a name in the message picks.
func TestLastLineOnlyWhileActiveAndSeveralSupporters(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT) // laundry (Maya) finished
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Maya · Laundry: anything else?", "task-1", time.Now().Add(-time.Minute)
	g.say(t, "can you get oat milk too?")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:can you get oat milk too?" {
		t.Fatalf("a finished supporter's question must not capture the message: %v", chat.posted)
	}

	g, chat, tasks, fc := contextRig(llm.IntentToSupporter, laundryT, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: toasted?", "task-2", time.Now().Add(-90*time.Minute)
	g.say(t, "Maya please ring the bell")
	if len(chat.posted) != 1 || chat.posted[0] != "task-1:Maya please ring the bell" {
		t.Fatalf("a named supporter wins over the last line: %v", chat.posted)
	}
	tasks.lastText = ""
	got := g.say(t, "ring the bell please")
	if !strings.Contains(got, CopyWhichTask) || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("no signal, two supporters → the list: %q state %s", got, g.state())
	}
	g.say(t, "never mind")
	fc.intent = llm.IntentNewTask
	g.say(t, "new task: walk my dog at 5") // mid-question now
	fc.intent = llm.IntentToSupporter
	got = g.say(t, "ring the bell please")
	if !strings.Contains(got, "Which supporter is that for?") || !strings.Contains(got, "“for Maya” or “for Sam”") || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", got, g.state())
	}
}

// MAJOR-3: "for Maya" with a pending draft delivers first; a failed relay
// keeps the draft and says only that it failed.
func TestForSupporterKeepsTheDraftWhenRelayFails(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "pick up my laundry tomorrow at 3")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	chat.post = errors.New("talkjs 503")
	got := g.say(t, "for Sam")
	if got != CopyRelayFailed || g.state() != StateAwaitingConfirm || len(chat.posted) != 0 {
		t.Fatalf("reply %q state %s posted %v", got, g.state(), chat.posted)
	}
	if sentText(g, "Passed that on to Sam. The new errand is dropped.") {
		t.Fatal("must not claim delivery")
	}
	chat.post = nil
	fc.intent = llm.IntentToSupporter
	got = g.say(t, "for Sam")
	if g.state() != StateIdle || len(chat.posted) != 1 || !strings.Contains(got, "Passed that on to Sam") {
		t.Fatalf("reply %q state %s posted %v", got, g.state(), chat.posted)
	}
}

// MAJOR-4: only "for <Name>" / "tell <Name>" / "for my supporter" are the
// way out — never a pronoun; "For her birthday…" is an errand.
func TestEscapePhraseIsNarrow(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentNewTask, bagelT)
	got := g.say(t, "For her birthday I need flowers delivered Friday")
	if len(chat.posted) != 0 || g.state() == StateIdle || !strings.Contains(got, "Booking this as a new errand") {
		t.Fatalf("posted %v state %s reply %q", chat.posted, g.state(), got)
	}
	fc.intent = llm.IntentCorrection
	g.say(t, "for him, 3pm instead")
	if len(chat.posted) != 0 || g.state() == StateIdle {
		t.Fatalf("a pronoun is not the way out: posted %v state %s", chat.posted, g.state())
	}
	// Two supporters with the same first name: the name alone is ambiguous.
	g, chat, _, _ = contextRig(llm.IntentToSupporter, ActiveTask{ID: "t1", Title: "A", SupporterName: "Sam Lee"}, ActiveTask{ID: "t2", Title: "B", SupporterName: "Sam Wu"})
	got = g.say(t, "for Sam: ring twice")
	if len(chat.posted) != 0 || !strings.Contains(got, "Which task") {
		t.Fatalf("posted %v reply %q", chat.posted, got)
	}
	// "for Maya" with nothing pending and no words: asked what to say.
	g, chat, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	if got := g.say(t, "for Sam"); got != CopyWhichTaskNothingHeld || len(chat.posted) != 0 {
		t.Fatalf("%q %v", got, chat.posted)
	}
}

// MINOR-3: an answered question no longer captures; the rule is idle-only.
func TestRuleNeedsAnUnansweredQuestionAndIdle(t *testing.T) {
	g, chat, tasks, fc := contextRig(llm.IntentNewTask, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt, tasks.lastAnswered = "Sam · Bagel run: toasted?", "task-2", time.Now().Add(-time.Minute), true
	g.say(t, "pick up my laundry tomorrow at 3")
	if len(chat.posted) != 0 || g.state() != StateAwaitingConfirm || len(fc.calls) != 1 {
		t.Fatalf("answered question must not capture: posted %v state %s calls %d", chat.posted, g.state(), len(fc.calls))
	}
	tasks.lastAnswered = false
	fc.intent = llm.IntentCorrection
	got := g.say(t, "make it 4pm")
	if len(chat.posted) != 0 || !strings.Contains(got, "Reply YES") {
		t.Fatalf("mid-question the model decides, not the rule: posted %v reply %q", chat.posted, got)
	}
}

// MINOR-4: "no onions" is not "nothing to buy".
func TestNoOnionsIsNotNoBudget(t *testing.T) {
	if IsNoBudget("no onions") || IsNoBudget("no, the other one") || !IsNoBudget("no") || !IsNoBudget("nothing") || !IsNoBudget("nothing to buy") {
		t.Fatal("budget opt-out regex")
	}
}

// NIT-1: without a context classifier, a plain-classifier failure still
// goes to the supporter.
func TestPlainClassifierFailureGoesToTheSupporter(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentNewTask, bagelT)
	g.r.Classifier = failingClassifier{}
	g.say(t, "I want everything bagel")
	if len(chat.posted) != 1 || g.state() != StateIdle {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
}

type failingClassifier struct{}

func (failingClassifier) Classify(context.Context, string) (llm.Intent, error) {
	return llm.IntentNewTask, errors.New("down")
}

// An answer to our question in words ("yes, approve it") is re-asked for
// a plain YES/NO — never relayed, never acted on.
func TestWordyAnswerIsReaskedNotRelayed(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentAnswer, bagelT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.PendingExtensionID = StateAwaitingApproval, "ext-1"
	c.ApprovalArmedAt = ptrTime(time.Now().Add(-time.Minute)) // a delivered question
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := g.say(t, "yes, approve it"); got != CopyApprovalReask || len(chat.posted) != 0 || len(g.booker.resolved) != 0 {
		t.Fatalf("reply %q posted %v resolved %v", got, chat.posted, g.booker.resolved)
	}
}

// ── review round 3 (D-17) ──────────────────────────────────────────────────

// F2: a bare "yes" in idle answers the supporter who just asked.
func TestBareYesInIdleAnswersTheSupporter(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentNewTask, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: want me to wait?", "task-2", time.Now().Add(-time.Minute)
	if got := g.say(t, "yes"); got != "" || len(chat.posted) != 1 || chat.posted[0] != "task-2:yes" || g.booker.posts != 0 {
		t.Fatalf("reply %q posted %v booked %d", got, chat.posted, g.booker.posts)
	}
	// Answered: the next bare yes is an acknowledgement again.
	tasks.lastAnswered = true
	if got := g.say(t, "yes"); got != CopyAck || len(chat.posted) != 1 {
		t.Fatalf("reply %q posted %v", got, chat.posted)
	}
}

// F3: the one who last wrote is a target only until the person answered.
func TestLastWriterIsATargetOnlyUntilAnswered(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, laundryT, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt, tasks.lastAnswered = "Maya · Laundry: heading over", "task-1", time.Now().Add(-time.Hour), true
	got := g.say(t, "get the large one")
	if len(chat.posted) != 0 || !strings.Contains(got, CopyWhichTask) {
		t.Fatalf("an answered line must not pick the target: posted %v reply %q", chat.posted, got)
	}
}

// F4: the escapes never disarm a pending approval.
func TestEscapesKeepTheApprovalArmed(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "pick up my laundry tomorrow at 3") // draft pending
	g.arm(t, "ext-5")
	fc.intent = llm.IntentToSupporter
	got := g.say(t, "for Sam, why $10 more?")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	// Explicit words relay; the draft underneath and the approval both stay.
	if len(chat.posted) != 1 || c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-5" || c.Pending == nil || !strings.Contains(got, CopyApprovalStillPending) {
		t.Fatalf("posted %v conv %+v reply %q", chat.posted, c, got)
	}
	got = g.say(t, "new task: walk my dog at 5")
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if got != CopyApprovalFirst || c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-5" {
		t.Fatalf("reply %q conv %+v", got, c)
	}
	g.say(t, "YES")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-5:true") || g.booker.posts != 0 {
		t.Fatalf("resolved %v booked %d", g.booker.resolved, g.booker.posts)
	}
}

// F5: a worded reply to the list goes by name or by context, never to the
// plain classifier's "when in doubt, new_task"; a failure joins the hold.
func TestWordedReplyToTheListUsesContext(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentToSupporter, laundryT, bagelT)
	g.say(t, "door code 1234")
	g.say(t, "Sam, everything bagel") // names a supporter on the list
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:door code 1234\nSam, everything bagel" || g.state() != StateIdle {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
	g, chat, _, fc = contextRig(llm.IntentToSupporter, laundryT, bagelT)
	g.say(t, "door code 1234")
	fc.err = errors.New("down")
	got := g.say(t, "the bagels one, everything")
	if len(chat.posted) != 0 || g.state() != StateAwaitingTaskChoice || !strings.Contains(got, CopyWhichTask) || g.booker.posts != 0 {
		t.Fatalf("a failure must join the hold, not draft: posted %v state %s reply %q", chat.posted, g.state(), got)
	}
}

// MINOR-1: "book it please" in awaiting_confirm is re-asked, not re-drafted.
func TestWordyYesOnTheSummaryIsReasked(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "pick up my laundry tomorrow at 3")
	fc.intent = llm.IntentAnswer
	drafts := g.booker.quotes
	if got := g.say(t, "book it please"); got != CopyConfirmReask || len(chat.posted) != 0 || g.booker.posts != 0 || g.booker.quotes != drafts {
		t.Fatalf("reply %q posted %v booked %d requoted %v", got, chat.posted, g.booker.posts, g.booker.quotes != drafts)
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("booked %d", g.booker.posts)
	}
}

// MINOR-5: a failed relay mid-question says only that it failed.
func TestFailedRelayMidQuestionSaysOnlyThat(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.PendingExtensionID = StateAwaitingApproval, "ext-1"
	c.ApprovalArmedAt = ptrTime(time.Now().Add(-time.Minute)) // a delivered question
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	chat.post = errors.New("talkjs 503")
	if got := g.say(t, "why $10 more?"); got != CopyRelayFailed || sentText(g, CopyApprovalStillPending) {
		t.Fatalf("%q", got)
	}
}

// "I will be there" is not a message to a supporter named Will.
func TestSupporterNameMatchIsCaseSensitive(t *testing.T) {
	will := ActiveTask{ID: "t9", Title: "Dog walk", SupporterName: "Will Park"}
	if _, ok := supporterNamed([]ActiveTask{will, bagelT}, "I will be there in 5"); ok {
		t.Fatal("lower-case 'will' must not match the name")
	}
	if t2, ok := supporterNamed([]ActiveTask{will, bagelT}, "Will, I'll be there in 5"); !ok || t2.ID != "t9" {
		t.Fatal("the name itself must match")
	}
}

// Round-3 minors: multi-line escapes; a bare yes during the budget question
// answers us (re-asked), not the supporter; a correction mid-budget
// re-drafts; "tell Sam <words>" leaves an unrelated draft alone.
func TestRoundThreeMinors(t *testing.T) {
	g, chat, _, fc := contextRig(llm.IntentToSupporter, bagelT)
	got := g.say(t, "new task: laundry 3pm\nbring bags")
	if g.state() == StateIdle || len(chat.posted) != 0 || !strings.Contains(got, "Reply YES") {
		t.Fatalf("multi-line 'new task:' must draft: state %s posted %v", g.state(), chat.posted)
	}
	g.say(t, "tell Sam I'll be 5 min late\nthanks")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:I'll be 5 min late\nthanks" || g.state() != StateAwaitingConfirm {
		t.Fatalf("explicit words relay and keep the draft: posted %v state %s", chat.posted, g.state())
	}

	g, chat, _, fc = contextRig(llm.IntentToSupporter, bagelT)
	g.parser.draft = bagelD
	g.say(t, "new task: bring me a bagel from Russ & Daughters") // awaiting_budget
	if got := g.say(t, "yes"); got != CopyAskBudgetAgain || len(chat.posted) != 0 || g.state() != StateAwaitingBudget {
		t.Fatalf("bare yes mid-budget: %q posted %v state %s", got, chat.posted, g.state())
	}
	fc.intent = llm.IntentCorrection
	if got := g.say(t, "actually two bagels"); !strings.Contains(got, "how much should I set aside") || len(chat.posted) != 0 {
		t.Fatalf("a correction mid-budget re-drafts: %q posted %v", got, chat.posted)
	}
}

// ── M3.2c: Arbiter rulings I-O and I-P ─────────────────────────────────────

// I-P version B: "ok" while a supporter wrote in the last minutes goes to
// the supporter; the person is told what still waits; a plain YES books.
// With no recent supporter message, "ok" books as before.
func TestCasualAffirmativeGoesToTheSupporterWhoJustWrote(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3") // awaiting_confirm
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: toasted?", "task-2", time.Now().Add(-time.Minute)
	got := g.say(t, "ok")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:ok" || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posted %v booked %d state %s", chat.posted, g.booker.posts, g.state())
	}
	if !strings.Contains(got, "(Passed “ok” to Sam.)") || !strings.Contains(got, "Still waiting on your YES or NO — book") {
		t.Fatalf("%q", got)
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("booked %d", g.booker.posts)
	}
	// Eleven minutes of silence from the supporter: "ok" is a YES again.
	g, chat, tasks, _ = contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: toasted?", "task-2", time.Now().Add(-11*time.Minute)
	g.say(t, "ok")
	if g.booker.posts != 1 || len(chat.posted) != 0 {
		t.Fatalf("booked %d posted %v", g.booker.posts, chat.posted)
	}
	// Same for an approval: "sure" is relayed, "YES" approves.
	g, chat, tasks, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: ok if I take 15 more?", "task-2", time.Now().Add(-time.Minute)
	got = g.say(t, "sure")
	if len(chat.posted) != 1 || len(g.booker.resolved) != 0 || !strings.Contains(got, CopyStillWaitingApproval) {
		t.Fatalf("posted %v resolved %v reply %q", chat.posted, g.booker.resolved, got)
	}
	g.say(t, "YES")
	if len(g.booker.resolved) != 1 {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
}

// I-O: an approval that arrives during a handoff is held, then sent and
// armed when ops release the thread — if still the person's to answer;
// dropped otherwise. Two holds are asked one at a time, oldest first.
func TestDeferredApprovalIsSentWhenTheThreadCanTakeIt(t *testing.T) {
	ev := StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-9", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}}
	ev2 := StatusEvent{Type: evTimeRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-10", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-10", Kind: "time", Minutes: 15}}
	held := func(t *testing.T) (*rig, int) {
		t.Helper()
		g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
		g.r.Ops, g.r.Thread = &memOps{}, &memThread{st: g.st}
		g.say(t, "I want to talk to a human")
		g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
		sent := len(g.ad.sent)
		for _, e := range []StatusEvent{ev, ev, ev2} { // the repeat must not double the hold
			if err := g.r.NotifyStatus(context.Background(), "user-1", e); err != nil {
				t.Fatal(err)
			}
		}
		c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
		if len(g.ad.sent) != sent || len(c.Deferred) != 2 || c.Deferred[0].ExtensionID != "ext-9" || c.Deferred[1].ExtensionID != "ext-10" || c.State != StateHandoff {
			t.Fatalf("must be held, not sent: sent %d conv %+v", len(g.ad.sent)-sent, c)
		}
		return g, sent
	}
	release := func(t *testing.T, g *rig) Conversation {
		t.Helper()
		if err := g.r.Release(context.Background(), Identity{ID: "id-" + me, UserID: "user-1", Channel: IMessage, Address: me}); err != nil {
			t.Fatal(err)
		}
		c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
		return c
	}

	// Released while both still pending: the first is sent and armed, the
	// second follows the answer.
	g, _ := held(t)
	g.booker.pendingExt = map[string]bool{"ext-9": true, "ext-10": true}
	c := release(t, g)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-9" || len(c.Deferred) != 1 || !strings.Contains(g.ad.last(), "Sam is asking for $10 more") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}
	g.say(t, "NO")
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-9:false") ||
		c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-10" || len(c.Deferred) != 0 || !strings.Contains(g.ad.last(), "15 more minutes") {
		t.Fatalf("resolved %v conv %+v last %q", g.booker.resolved, c, g.ad.last())
	}
	g.say(t, "YES")
	if len(g.booker.resolved) != 2 || !strings.HasSuffix(g.booker.resolved[1], ":ext-10:true") {
		t.Fatalf("resolved %v", g.booker.resolved)
	}

	// Resolved or expired in the app before release: dropped silently; the
	// one still live is asked.
	g, _ = held(t)
	g.booker.pendingExt = map[string]bool{"ext-10": true}
	c = release(t, g)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-10" || len(c.Deferred) != 0 || strings.Contains(g.ad.last(), "$10") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}

	// Timed out by the clock while held (lazy expiry: the row may still
	// read pending): dropped without asking, whatever the booker says.
	g, _ = held(t)
	g.booker.pendingExt = map[string]bool{"ext-9": true, "ext-10": true}
	base := time.Now()
	g.r.Now = func() time.Time { return base.Add(6 * time.Minute) }
	c = release(t, g)
	if c.State != StateIdle || len(c.Deferred) != 0 || strings.Contains(g.ad.last(), "asking") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}

	// Time left is recomputed: asked with the minutes that remain.
	g, _ = held(t)
	g.booker.pendingExt = map[string]bool{"ext-9": true}
	base = time.Now()
	g.r.Now = func() time.Time { return base.Add(3 * time.Minute) }
	c = release(t, g)
	if c.State != StateAwaitingApproval || !strings.Contains(g.ad.last(), "2 minutes") || strings.Contains(g.ad.last(), "5 minutes") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}

	// A booker error keeps the hold for the next resume point.
	g, _ = held(t)
	g.booker.pendingErr = errors.New("db down")
	c = release(t, g)
	if c.State != StateIdle || len(c.Deferred) != 2 {
		t.Fatalf("conv %+v", c)
	}

	// A definite refusal of the send: the claim is undone, the hold kept
	// in place. An uncertain failure (the question may have gone through):
	// the claim stays unconfirmed for the repair.
	for _, tc := range []struct {
		err   error
		state State
	}{
		{&ProviderStatusError{Provider: "sendblue", Status: 400, Text: "bad"}, StateIdle},
		{errors.New("dial tcp: i/o timeout"), StateAwaitingApproval},
		{&ProviderStatusError{Provider: "sendblue", Status: 408, Text: "timeout"}, StateAwaitingApproval},
	} {
		g, _ = held(t)
		g.booker.pendingExt = map[string]bool{"ext-9": true, "ext-10": true}
		c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
		c.State = StateIdle
		if err := g.convs.SaveConversation(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		g.ad.err = tc.err
		g.r.resumeDeferred(context.Background(), Identity{ID: "id-" + me, UserID: "user-1", Channel: IMessage, Address: me})
		c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
		if c.State != tc.state || len(c.Deferred) != 2 || c.Deferred[0].ExtensionID != "ext-9" || (tc.state == StateAwaitingApproval && !c.Unconfirmed()) {
			t.Fatalf("%v: conv %+v", tc.err, c)
		}
	}

	// Held for another user's identity: dropped, never asked.
	g, _ = held(t)
	g.booker.pendingExt = map[string]bool{"ext-9": true, "ext-10": true}
	if err := g.r.Release(context.Background(), Identity{ID: "id-" + me, UserID: "user-2", Channel: IMessage, Address: me}); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateIdle || len(c.Deferred) != 0 || strings.Contains(g.ad.last(), "asking") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}
}

// I-O, one at a time: a second live request while one is armed is held,
// not asked over it; a resolution of some other request leaves the armed
// question standing; the resolution of the armed one clears it.
func TestSecondRequestWhileOneIsArmedIsHeld(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	evA := StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-A", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-A", Kind: "budget", Cents: 1000}}
	evB := StatusEvent{Type: evTimeRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-B", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-B", Kind: "time", Minutes: 15}}
	g.booker.pendingExt = map[string]bool{"ext-A": true, "ext-B": true}
	for _, e := range []StatusEvent{evA, evA, evB} { // A asked once; B held
		if err := g.r.NotifyStatus(context.Background(), "user-1", e); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-A" || len(c.Deferred) != 1 || c.Deferred[0].ExtensionID != "ext-B" || len(g.ad.sent) != 1 {
		t.Fatalf("conv %+v sent %d", c, len(g.ad.sent))
	}
	// B resolves in the app: A stays armed.
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", ExtensionID: "ext-B", Title: "You approved the request"}); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-A" {
		t.Fatalf("another request's resolution must not disarm A: %+v", c)
	}
	// A resolution without an id: the booker says A is still pending → stays.
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", Title: "A request timed out"}); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-A" {
		t.Fatalf("A still pending must stay armed: %+v", c)
	}
	// The person answers A: it resolves, and B (still held, still live) follows.
	g.say(t, "YES")
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-A:true") || c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-B" || len(c.Deferred) != 0 {
		t.Fatalf("resolved %v conv %+v", g.booker.resolved, c)
	}
	// A's own late resolution hook must not disarm B.
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", ExtensionID: "ext-A", Title: "You approved the request"}); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-B" {
		t.Fatalf("A's resolution must not disarm B: %+v", c)
	}
	// B's resolution does.
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", ExtensionID: "ext-B", Title: "A request timed out"}); err != nil {
		t.Fatal(err)
	}
	if g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
}

// S-11/5: a YES sent before the question was asked is not its answer — the
// double-tap on the booking's YES cannot approve the supporter's request.
func TestAnswerFromBeforeTheQuestionIsNotAnAnswer(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	in := Inbound{Channel: IMessage, ProviderMessageID: "early-yes", From: me, Text: "YES", ReceivedAt: time.Now().Add(-time.Minute)}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 0 || g.state() != StateAwaitingApproval || g.ad.last() != CopyApprovalReask {
		t.Fatalf("resolved %v state %s last %q", g.booker.resolved, g.state(), g.ad.last())
	}
	g.say(t, "YES") // sent now: the answer
	if len(g.booker.resolved) != 1 {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
}

// Two resume points at once ask a held question once.
func TestConcurrentResumeAsksOnce(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.r.Ops, g.r.Thread = &memOps{}, &memThread{st: g.st}
	g.say(t, "I want to talk to a human")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	ev := StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-9", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	g.booker.pendingExt = map[string]bool{"ext-9": true}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State = StateIdle
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	before := len(g.ad.sent)
	id := Identity{ID: "id-" + me, UserID: "user-1", Channel: IMessage, Address: me}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.r.resumeDeferred(context.Background(), id) }()
	}
	wg.Wait()
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.ad.sent)-before != 1 || c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-9" || len(c.Deferred) != 0 {
		t.Fatalf("sent %d conv %+v", len(g.ad.sent)-before, c)
	}
}

// The WhatsApp 24h window is measured from the person's own last message:
// outside it the hold waits for a later resume point.
func TestDeferredApprovalWaitsForTheWhatsAppWindow(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}}}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State = StatePosting
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ev := StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-9", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
		t.Fatal(err)
	}
	g.booker.pendingExt = map[string]bool{"ext-9": true}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	c.State = StateIdle
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	g.r.Adapters[WhatsApp] = g.ad
	old := time.Now().Add(-25 * time.Hour)
	g.convs.setLastInbound("id-"+me, &old)
	id := Identity{ID: "id-" + me, UserID: "user-1", Channel: WhatsApp, Address: me}
	before := len(g.ad.sent)
	g.r.resumeDeferred(context.Background(), id)
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.ad.sent) != before || len(c.Deferred) != 1 || c.State != StateIdle {
		t.Fatalf("sent %d conv %+v", len(g.ad.sent)-before, c)
	}
	now := time.Now()
	g.convs.setLastInbound("id-"+me, &now)
	g.r.resumeDeferred(context.Background(), id)
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.ad.sent) != before+1 || c.State != StateAwaitingApproval {
		t.Fatalf("sent %d conv %+v", len(g.ad.sent)-before, c)
	}
}

// I-O at the booking resume point: a question held while the booking was
// in flight follows the booking confirmation.
func TestDeferredApprovalFollowsTheBooking(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	// The request arrives while the booking is in flight — through the real
	// claim-and-post path, whose full-row saves must not wipe the hold.
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	ev := StatusEvent{Type: evTimeRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-4", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-4", Kind: "time", Minutes: 15}}
	g.booker.pendingExt = map[string]bool{"ext-4": true}
	g.booker.during = func() {
		if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
			t.Fatal(err)
		}
		c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
		if c.State != StatePosting || len(c.Deferred) != 1 {
			t.Fatalf("must be held while posting: %+v", c)
		}
	}
	g.say(t, "YES")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if g.booker.posts != 1 || c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-4" || len(c.Deferred) != 0 || !strings.Contains(g.ad.last(), "15 more minutes") {
		t.Fatalf("booked %d conv %+v last %q", g.booker.posts, c, g.ad.last())
	}
	if n := len(g.ad.sent); !strings.Contains(g.ad.sent[n-2].Text, "Booked") && !strings.Contains(g.ad.sent[n-2].Text, "Posted") {
		t.Fatalf("the question follows the booking, not the other way: %q", g.ad.sent[n-2].Text)
	}
}

// I-P edges: a casual "ok" swiped on our own prompt answers us (I-L wins);
// a lookup failure must not let the word answer us by accident; 確認 is a
// strict YES.
func TestCasualAffirmativeGateEdges(t *testing.T) {
	// Swiped on the approval prompt (WhatsApp, where reply context is
	// real): an answer, even with a fresh question.
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Adapters[WhatsApp] = g.ad
	g.ids.link(me, "user-1")
	g.arm(t, "ext-2")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: ok if I take 15 more?", "task-2", time.Now().Add(-time.Minute)
	tasks.byMsg["prompt-1"] = "task-2"
	in := Inbound{Channel: WhatsApp, ProviderMessageID: "ok1", From: me, Text: "ok", ReplyToProviderID: "prompt-1", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 0 || len(g.booker.resolved) != 1 {
		t.Fatalf("posted %v resolved %v", chat.posted, g.booker.resolved)
	}
	// On iMessage the swipe carries nothing (P5): the word goes to Sam.
	g, chat, tasks, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: ok if I take 15 more?", "task-2", time.Now().Add(-time.Minute)
	tasks.byMsg["prompt-1"] = "task-2"
	in = Inbound{Channel: IMessage, ProviderMessageID: "ok1b", From: me, Text: "ok", ReplyToProviderID: "prompt-1", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || len(g.booker.resolved) != 0 {
		t.Fatalf("posted %v resolved %v", chat.posted, g.booker.resolved)
	}

	// The lookup fails: fail closed — nothing is approved, nothing posted.
	g, chat, tasks, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	tasks.lastErr = errors.New("db down")
	in = Inbound{Channel: IMessage, ProviderMessageID: "ok2", From: me, Text: "ok", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err == nil || len(chat.posted) != 0 || len(g.booker.resolved) != 0 {
		t.Fatalf("err %v posted %v resolved %v", err, chat.posted, g.booker.resolved)
	}

	// 確認 is strict: approves even with the supporter's question open.
	g, chat, tasks, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: ok if I take 15 more?", "task-2", time.Now().Add(-time.Minute)
	g.say(t, "確認")
	if len(chat.posted) != 0 || len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-2:true") {
		t.Fatalf("posted %v resolved %v", chat.posted, g.booker.resolved)
	}
}

// I-O: when the armed question ends in the app (resolved or timed out), a
// held one is asked right away — not at the person's next message.
func TestHeldQuestionFollowsTheArmedOnesResolution(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-A")
	g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evTimeRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-B", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-B", Kind: "time", Minutes: 15}})
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.PendingExtensionID != "ext-A" || len(c.Deferred) != 1 {
		t.Fatalf("%+v", c)
	}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", ExtensionID: "ext-A", Title: "A request timed out", Body: "…declined automatically."}); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-B" || len(c.Deferred) != 0 || !strings.Contains(g.ad.last(), "15 more minutes") {
		t.Fatalf("B must follow A's resolution: %+v last %q", c, g.ad.last())
	}
}

// Two requests arriving together on the direct path: one is armed, the
// other held — never both asked.
func TestConcurrentRequestsAskOne(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	evs := []StatusEvent{
		{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-A", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-A", Kind: "budget", Cents: 1000}},
		{Type: evTimeRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-B", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-B", Kind: "time", Minutes: 15}},
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(ev StatusEvent) {
			defer wg.Done()
			if err := g.r.NotifyStatus(context.Background(), "user-1", ev); err != nil {
				t.Error(err)
			}
		}(evs[i])
	}
	wg.Wait()
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.ad.sent) != 1 || c.State != StateAwaitingApproval || len(c.Deferred) != 1 || c.Deferred[0].ExtensionID == c.PendingExtensionID {
		t.Fatalf("sent %d conv %+v", len(g.ad.sent), c)
	}
	// The one sent is the one armed.
	if (c.PendingExtensionID == "ext-A") != strings.Contains(g.ad.last(), "$10") {
		t.Fatalf("armed %s, sent %q", c.PendingExtensionID, g.ad.last())
	}
}

// A claim whose delivery was never confirmed: nothing answers it, the
// person is told a question waits, and once the claim is old enough it is
// re-sent from the hold and confirmed.
func TestUnconfirmedClaimIsUnanswerableThenResent(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-9",
		Type: evBudgetRequested, Approval: PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	if ok, err := g.convs.ClaimHold(context.Background(), "id-"+me, "ext-9", base); err != nil || !ok {
		t.Fatal(err)
	}
	// (the process died here) — a YES is refused and told a question is
	// on its way; a relayed line does not describe the question either.
	sent := len(g.ad.sent)
	got := g.say(t, "YES")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.booker.resolved) != 0 || !c.Unconfirmed() || len(c.Deferred) != 1 || got != CopyApprovalOnItsWay || len(g.ad.sent) != sent+1 {
		t.Fatalf("resolved %v conv %+v last %q", g.booker.resolved, c, got)
	}
	if note := g.r.pendingNote(c, "sam", IMessage); note != "" {
		t.Fatalf("note %q", note)
	}
	// Old enough, through an inbound message (which touches the row): the
	// YES is still refused, the question is re-sent and confirmed.
	g.r.Now = func() time.Time { return base.Add(UnconfirmedResend + time.Second) }
	g.convs.touch("id-"+me, base.Add(UnconfirmedResend))
	in := Inbound{Channel: IMessage, ProviderMessageID: "yes-2", From: me, Text: "YES", ReceivedAt: base.Add(UnconfirmedResend)}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.booker.resolved) != 0 || c.Unconfirmed() || len(c.Deferred) != 0 || c.PendingExtensionID != "ext-9" || !strings.Contains(g.ad.last(), "$10 more") {
		t.Fatalf("resolved %v conv %+v last %q", g.booker.resolved, c, g.ad.last())
	}
	// A YES sent after the real stamp answers it.
	in = Inbound{Channel: IMessage, ProviderMessageID: "late-yes", From: me, Text: "YES", ReceivedAt: base.Add(UnconfirmedResend + 2*time.Second)}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 1 {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
}

// A YES with no usable send time is refused and the question re-asked.
func TestAnswerWithoutASendTimeIsRefused(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	in := Inbound{Channel: IMessage, ProviderMessageID: "no-time", From: me, Text: "YES"}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 0 || g.ad.last() != CopyApprovalReask || g.state() != StateAwaitingApproval {
		t.Fatalf("resolved %v last %q state %s", g.booker.resolved, g.ad.last(), g.state())
	}
}

// The end-of-message resume waits when this turn just asked a question of
// ours: no two questions in one burst.
func TestHeldQuestionDoesNotFollowANewQuestionOfOurs(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-9",
		Type: evBudgetRequested, Approval: PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	g.say(t, "new task: pick up my laundry tomorrow at 3") // idle → awaiting_confirm: a question of ours
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingConfirm || len(c.Deferred) != 1 || strings.Contains(g.ad.last(), "$10 more") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}
	g.say(t, "YES") // booked → the held question follows the confirmation
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if g.booker.posts != 1 || c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-9" {
		t.Fatalf("posts %d conv %+v", g.booker.posts, c)
	}
}

// A request that ends while its question is in flight: the confirm finds
// the claim gone, and the person is told rather than left with a question
// a YES would answer against the wrong state.
func TestRequestEndingDuringTheSendIsTold(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	id := Identity{ID: "id-" + me, UserID: "user-1", Channel: IMessage, Address: me}
	hooked := &hookedAdapter{fakeSendAdapter: g.ad, during: func() {
		// The resolution lands mid-send (and the app no longer has it pending).
		g.booker.pendingExt = map[string]bool{}
		if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", ExtensionID: "ext-9", Title: "A request timed out"}); err != nil {
			t.Error(err)
		}
	}}
	g.r.Adapters[IMessage] = hooked
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-9",
		Type: evBudgetRequested, Approval: PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	g.r.resumeDeferred(context.Background(), id)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateIdle || c.PendingExtensionID != "" || g.ad.last() != CopyApprovalGone {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}
	g.say(t, "YES")
	if len(g.booker.resolved) != 0 || g.booker.posts != 0 {
		t.Fatalf("resolved %v posts %d", g.booker.resolved, g.booker.posts)
	}
}

// hookedAdapter runs a callback inside Send, once.
type hookedAdapter struct {
	*fakeSendAdapter
	during func()
}

func (h *hookedAdapter) Send(ctx context.Context, o Outbound) (string, error) {
	if h.during != nil {
		f := h.during
		h.during = nil
		f()
	}
	return h.fakeSendAdapter.Send(ctx, o)
}

// A reply that merely re-asks our own question is not followed by the
// supporter's: no burst.
func TestHeldQuestionDoesNotFollowAReask(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentOther, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	g.say(t, "new task: pick up my laundry tomorrow at 3") // awaiting_confirm
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-9",
		Type: evBudgetRequested, Approval: PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	g.say(t, "hmm") // re-asks the summary; state unchanged
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingConfirm || len(c.Deferred) != 1 || strings.Contains(g.ad.last(), "$10 more") {
		t.Fatalf("conv %+v last %q", c, g.ad.last())
	}
}

// Two repairs at once re-send once (the re-claim is a compare-and-set);
// a dead hold is cleared with a reply, not re-sent.
func TestRepairIsOnceAndClearsDeadHolds(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	id := Identity{ID: "id-" + me, UserID: "user-1", Channel: IMessage, Address: me}
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-9",
		Type: evBudgetRequested, Approval: PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	base := time.Now()
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.convs.ClaimHold(context.Background(), "id-"+me, "ext-9", base); err != nil || !ok {
		t.Fatal(err)
	}
	g.r.Now = func() time.Time { return base.Add(UnconfirmedResend + time.Second) }
	before := len(g.ad.sent)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.r.resumeDeferred(context.Background(), id) }()
	}
	wg.Wait()
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if len(g.ad.sent)-before != 1 || c.Unconfirmed() || len(c.Deferred) != 0 {
		t.Fatalf("sent %d conv %+v", len(g.ad.sent)-before, c)
	}

	// Dead by the app: cleared, and the YES that triggered it is answered.
	g, _, _, _ = contextRig(llm.IntentNewTask, bagelT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	g.say(t, "new task: pick up my laundry tomorrow at 3") // a draft underneath
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.convs.ClaimHold(context.Background(), "id-"+me, "ext-9", base); err != nil || !ok {
		t.Fatal(err)
	}
	g.booker.pendingExt = map[string]bool{}
	g.r.Now = func() time.Time { return base.Add(UnconfirmedResend + time.Second) }
	in := Inbound{Channel: IMessage, ProviderMessageID: "yes-dead", From: me, Text: "YES", ReceivedAt: base.Add(UnconfirmedResend)}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingConfirm || len(c.Deferred) != 0 || g.booker.posts != 0 || len(g.booker.resolved) != 0 ||
		!strings.Contains(g.ad.last(), CopyApprovalGone) || !strings.Contains(g.ad.last(), "YES") {
		t.Fatalf("conv %+v posts %d last %q", c, g.booker.posts, g.ad.last())
	}
}
