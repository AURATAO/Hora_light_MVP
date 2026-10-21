package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

type fakeChat struct {
	active []ActiveTask
	err    error
	posted []string // "task:text"
	keys   []string
	post   error
}

func (f *fakeChat) ActiveTasks(context.Context, string) ([]ActiveTask, error) { return f.active, f.err }
func (f *fakeChat) PostAsRequester(_ context.Context, taskID, _ string, text, key string) error {
	if f.post != nil {
		return f.post
	}
	for _, t := range f.active {
		if t.ID == taskID {
			f.posted = append(f.posted, taskID+":"+text)
			f.keys = append(f.keys, key)
			return nil
		}
	}
	return fmt.Errorf("fake: %w", ErrTaskNotActive)
}

type fakeTasks struct {
	byMsg map[string]string // provider id → task
	relay map[string]bool   // provider id → was a relayed supporter message
	set   []string
	err   error
}

func (f *fakeTasks) TaskForProviderMessage(_ context.Context, _ Channel, _, id string) (string, bool, bool, error) {
	if f.err != nil {
		return "", false, false, f.err
	}
	tid, known := f.byMsg[id]
	return tid, f.relay[id], known, nil
}
func (f *fakeTasks) SetMessageTask(_ context.Context, _ Channel, id, task string) error {
	f.set = append(f.set, id+"→"+task)
	return nil
}

var (
	laundryT = ActiveTask{ID: "task-1", Title: "Laundry pickup", SupporterName: "Maya"}
	bagelT   = ActiveTask{ID: "task-2", Title: "Bagel run", SupporterName: "Sam"}
)

func relayRig(intent llm.Intent, active ...ActiveTask) (*rig, *fakeChat, *fakeTasks) {
	g := newRig(intent)
	g.ids.link(me, "user-1")
	chat := &fakeChat{active: active}
	tasks := &fakeTasks{byMsg: map[string]string{}, relay: map[string]bool{}}
	g.r.Chat, g.r.Tasks = chat, tasks
	return g, chat, tasks
}

// One supporter on the job: "thanks, leave it at the door" goes to them, no
// echo back, and the message is tagged with the task for later replies.
func TestOneActiveTaskRelaysSmallTalk(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentOther, laundryT)
	in := Inbound{Channel: IMessage, ProviderMessageID: "m1", From: me, Text: "thanks, leave it at the door", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-1:thanks, leave it at the door" {
		t.Fatalf("posted %v", chat.posted)
	}
	if len(g.ad.sent) != 0 {
		t.Fatalf("the requester must get no echo, got %+v", g.ad.sent)
	}
	if len(tasks.set) != 1 || tasks.set[0] != "m1→task-1" {
		t.Fatalf("message not tagged with its task: %v", tasks.set)
	}
}

// A YES/NO or a question with a supporter on the job is for the supporter;
// nothing is pending, so nothing can book.
func TestYesAndQuestionsReachTheSupporter(t *testing.T) {
	for _, c := range []struct {
		intent llm.Intent
		text   string
	}{{llm.IntentOther, "yes please, thank you"}, {llm.IntentQuestion, "where are you now?"}, {llm.IntentGreeting, "hi!"}} {
		g, chat, _ := relayRig(c.intent, laundryT)
		g.say(t, c.text)
		if len(chat.posted) != 1 || !strings.HasSuffix(chat.posted[0], ":"+c.text) {
			t.Fatalf("%q: posted %v", c.text, chat.posted)
		}
		if g.booker.posts != 0 || g.state() != StateIdle {
			t.Fatalf("%q: booked %d state %s", c.text, g.booker.posts, g.state())
		}
	}
}

// A new errand still drafts — the supporter is not asked to do it.
func TestNewErrandStillDraftsWithASupporterOnTheJob(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentNewTask, laundryT)
	got := g.say(t, "also pick up my dry cleaning tomorrow")
	if len(chat.posted) != 0 || g.state() != StateAwaitingConfirm || !strings.Contains(got, "Reply YES") {
		t.Fatalf("posted %v state %s reply %q", chat.posted, g.state(), got)
	}
}

// Several supporters: a numbered list, the message is held, the number
// delivers it. A bad number re-asks; the held text survives.
func TestSeveralActiveTasksAskWhichOne(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	got := g.say(t, "please get everything bagels")
	if !strings.Contains(got, CopyWhichTask) || !strings.Contains(got, "1. Laundry pickup (Maya)") || !strings.Contains(got, "2. Bagel run (Sam)") {
		t.Fatalf("%q", got)
	}
	if g.state() != StateAwaitingTaskChoice || len(chat.posted) != 0 {
		t.Fatalf("state %s posted %v", g.state(), chat.posted)
	}
	if got := g.say(t, "7"); got != CopyWhichTaskBadPick || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("%q state %s", got, g.state())
	}
	g.say(t, "#2")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:please get everything bagels" {
		t.Fatalf("posted %v", chat.posted)
	}
	if g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
}

// While a pick is pending, a non-number is an ordinary message: a new errand
// drafts instead of being mistaken for an answer.
func TestNotAPickIsHandledAsAFreshMessage(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentNewTask, laundryT, bagelT)
	g.r.Classifier = fakeClassifier{intent: llm.IntentOther}
	g.say(t, "see you soon")
	if g.state() != StateAwaitingTaskChoice {
		t.Fatalf("state %s", g.state())
	}
	g.r.Classifier = fakeClassifier{intent: llm.IntentNewTask}
	got := g.say(t, "and walk my dog at 5")
	if g.state() != StateAwaitingConfirm || !strings.Contains(got, "Reply YES") || len(chat.posted) != 0 {
		t.Fatalf("state %s reply %q posted %v", g.state(), got, chat.posted)
	}
}

// An inline reply to a status message about task 2 goes to task 2's chat even
// when the words look like a new errand — the person replied to that thread.
func TestInlineReplyRoutesByTheRepliedMessage(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentNewTask, laundryT, bagelT)
	tasks.byMsg["out-status"] = "task-2"
	in := Inbound{Channel: IMessage, ProviderMessageID: "m2", From: me, Text: "get me two extra", ReplyToProviderID: "out-status", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:get me two extra" || g.state() != StateIdle {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
}

// No supporter on the job, or relay not configured: the old behaviour.
func TestNoActiveTaskFallsBackToTheAck(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther)
	if got := g.say(t, "thanks!"); got != CopyAck || len(chat.posted) != 0 {
		t.Fatalf("%q posted %v", got, chat.posted)
	}
	g = newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	if got := g.say(t, "thanks!"); got != CopyAck {
		t.Fatalf("%q", got)
	}
	// A stranger with nothing linked is never relayed anywhere.
	g = newRig(llm.IntentOther)
	g.r.Chat = &fakeChat{active: []ActiveTask{laundryT}}
	if got := g.say(t, "thanks!"); got != CopyWelcome {
		t.Fatalf("%q", got)
	}
}

// The post failing tells the requester, instead of dropping their words.
func TestRelayFailureIsToldNotSwallowed(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT)
	chat.post = errors.New("talkjs 503")
	if got := g.say(t, "running late?"); got != CopyRelayFailed {
		t.Fatalf("%q", got)
	}
}

// A YES while a pick is pending does not book anything: the only pending
// thing is a relay, and YES is not a number.
func TestYesDuringTaskChoiceBooksNothing(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	g.say(t, "YES")
	if g.booker.posts != 0 || len(chat.posted) != 0 {
		t.Fatalf("booked %d posted %v", g.booker.posts, chat.posted)
	}
}

// Supporter → requester: the message lands on the booking thread, named and
// tagged with the task; a photo rides along. Outside the WhatsApp window it
// is not sent.
func TestRelayToChatDeliversToTheRequester(t *testing.T) {
	g := newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: IMessage, Address: me}}}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya Chen", "Laundry pickup", "On my way!", "https://cdn.example/p.jpg"); err != nil {
		t.Fatal(err)
	}
	if len(g.ad.sent) != 1 || g.ad.sent[0].Text != "Maya Chen · Laundry pickup: On my way!" || g.ad.sent[0].MediaURL != "https://cdn.example/p.jpg" {
		t.Fatalf("sent %+v", g.ad.sent)
	}
	if g.st.lastOutboundTask() != "task-1" {
		t.Fatal("relayed message must be recorded against its task, so an inline reply finds it")
	}

	old := time.Now().Add(-25 * time.Hour)
	g = newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{Channel: WhatsApp, Address: me}, LastInboundAt: &old}}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry", "hi", ""); err != nil || len(g.ad.sent) != 0 {
		t.Fatalf("err %v sent %+v", err, g.ad.sent)
	}

	g = newRig("")
	g.r.Status = &memStatus{found: false}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry", "hi", ""); err != nil || len(g.ad.sent) != 0 {
		t.Fatalf("app-only requester: err %v sent %+v", err, g.ad.sent)
	}
}

func TestTaskChoiceParsing(t *testing.T) {
	for text, want := range map[string]int{"1": 1, " 2 ": 2, "#3": 3, "2.": 2, "10": 10, "0": 0, "yes": 0, "2 bagels": 0, "": 0} {
		got, ok := taskChoice(text)
		if (want == 0 && ok) || (want > 0 && (!ok || got != want)) {
			t.Errorf("%q → %d ok=%v want %d", text, got, ok, want)
		}
	}
}

// ── review round 2 (M-1, M-3) ──────────────────────────────────────────────

// M-1: a supporter's message relayed while a YES is pending says what a bare
// YES still answers; an inline reply to the supporter relays in that state
// instead of booking.
func TestRelayDuringPendingYesIsDisambiguated(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentNewTask, laundryT)
	g.say(t, "laundry pickup") // draft pending → awaiting_confirm
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry pickup", "Want the large bag too?", ""); err != nil {
		t.Fatal(err)
	}
	got := g.ad.last()
	if !strings.HasPrefix(got, "Maya · Laundry pickup: Want the large bag too?") || !strings.Contains(got, "A YES or NO sent on its own answers my question above") || !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("%q", got)
	}
	if !g.st.lastOutboundRelay() {
		t.Fatal("a relayed supporter message must be recorded as a relay bubble")
	}
	maya := g.st.lastOutboundID()
	tasks.byMsg[maya], tasks.relay[maya] = "task-1", true
	// Anything in reply to Maya's bubble is for Maya — a "yes please" too:
	// nothing books.
	for i, text := range []string{"yes, the large one", "yes please"} {
		in := Inbound{Channel: IMessage, ProviderMessageID: fmt.Sprintf("r%d", i), From: me, Text: text, ReplyToProviderID: maya, ReceivedAt: time.Now()}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if len(chat.posted) != 2 || chat.posted[1] != "task-1:yes please" || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posted %v booked %d state %s", chat.posted, g.booker.posts, g.state())
	}
	if chat.keys[0] != "r0" {
		t.Fatalf("idempotency key must be the inbound message id: %v", chat.keys)
	}
	// Nothing pending → no note.
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry pickup", "On my way", ""); err != nil {
		t.Fatal(err)
	}
	if got := g.ad.last(); strings.Contains(got, "still answers") {
		t.Fatalf("no note when nothing is pending: %q", got)
	}
}

func TestRelayDuringPendingApprovalIsDisambiguated(t *testing.T) {
	g, _, _ := relayRig(llm.IntentOther, laundryT)
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.PendingExtensionID = StateAwaitingApproval, "ext-1"
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry pickup", "ok if I take 15 more?", ""); err != nil {
		t.Fatal(err)
	}
	if got := g.ad.last(); !strings.Contains(got, "answers my question above about Maya’s request") {
		t.Fatalf("%q", got)
	}
}

// M-3: the hold survives a bad pick and a failed post; a second message
// joins the hold instead of replacing it; nothing is relayed as "1".
func TestHoldSurvivesBadPickAndFailedPost(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	if got := g.say(t, "7"); got != CopyWhichTaskBadPick || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("%q state %s", got, g.state())
	}
	chat.post = errors.New("talkjs 503")
	if got := g.say(t, "2"); got != CopyRelayFailedPick || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("%q state %s", got, g.state())
	}
	chat.post = nil
	g.say(t, "2")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:please get everything bagels" || g.state() != StateIdle {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}

	g, chat, _ = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	got := g.say(t, "and a coffee")
	if !strings.Contains(got, CopyWhichTask) || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("%q state %s", got, g.state())
	}
	g.say(t, "1")
	if len(chat.posted) != 1 || chat.posted[0] != "task-1:please get everything bagels\nand a coffee" {
		t.Fatalf("second message must join the hold, not replace it: %v", chat.posted)
	}
}

// An empty supporter message (a location, a sticker) sends nothing.
func TestRelayToChatSkipsEmptyBody(t *testing.T) {
	g := newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "Maya", "Laundry", "   ", ""); err != nil || len(g.ad.sent) != 0 {
		t.Fatalf("err %v sent %+v", err, g.ad.sent)
	}
}

// ── review round 2 ─────────────────────────────────────────────────────────

// N-1: an inline YES/NO swiped on the approval prompt approves — it is not
// relayed to the supporter as the word "YES".
func TestInlineYesOnApprovalPromptApproves(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentOther, laundryT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.PendingExtensionID = StateAwaitingApproval, "ext-1"
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	tasks.byMsg["prompt-1"] = "task-1" // the approval prompt is task-tagged
	in := Inbound{Channel: IMessage, ProviderMessageID: "y1", From: me, Text: "YES", ReplyToProviderID: "prompt-1", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 0 || len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-1:true") || g.state() != StateIdle {
		t.Fatalf("posted %v resolved %v state %s", chat.posted, g.booker.resolved, g.state())
	}
}

// N-2: a reply to a message about a finished task is not a dead end — it
// routes like any other message (here: a new errand drafts).
func TestInlineReplyToFinishedTaskRoutesNormally(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentNewTask) // no active task any more
	tasks.byMsg["done-msg"] = "task-9"
	in := Inbound{Channel: IMessage, ProviderMessageID: "n1", From: me, Text: "book the same again tomorrow", ReplyToProviderID: "done-msg", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
	// A pick naming a task that closed between the list and the number:
	// told once, hold dropped, no loop.
	g, chat, _ = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	chat.active = []ActiveTask{laundryT} // bagel run finished meanwhile
	if got := g.say(t, "2"); got != CopyRelayGone || g.state() != StateIdle {
		t.Fatalf("%q state %s", got, g.state())
	}
}

// N-4: an approval request arriving under a hold is armed; the YES answers
// it, and the hold comes back with its list.
func TestApprovalArrivingUnderAHoldIsArmedAndHoldResumes(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	if g.state() != StateAwaitingTaskChoice {
		t.Fatalf("state %s", g.state())
	}
	if err := g.r.askApproval(context.Background(), "id-"+me, "ext-1"); err != nil {
		t.Fatal(err)
	}
	if g.state() != StateAwaitingApproval {
		t.Fatalf("approval must be armed under a hold, state %s", g.state())
	}
	got := g.say(t, "YES")
	if len(g.booker.resolved) != 1 || !strings.Contains(got, CopyBackToHold) || !strings.Contains(got, "1. Laundry pickup (Maya)") || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("resolved %v reply %q state %s", g.booker.resolved, got, g.state())
	}
	g.say(t, "2")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:please get everything bagels" {
		t.Fatalf("posted %v", chat.posted)
	}
}

// N-7: "never mind" drops the hold; a hold is capped; relay switched off
// under a hold does not panic.
func TestHoldEscapeHatchCapAndNilChat(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	if got := g.say(t, "never mind"); got != CopyHoldDropped || g.state() != StateIdle || len(chat.posted) != 0 {
		t.Fatalf("%q state %s posted %v", got, g.state(), chat.posted)
	}

	g, chat, _ = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, strings.Repeat("x", 1400))
	g.say(t, strings.Repeat("y", 1400))
	g.say(t, "1")
	if len(chat.posted) != 1 || len(chat.posted[0]) > relayTextCap+len("task-1:")+len("…") {
		t.Fatalf("hold not capped: %d", len(chat.posted[0]))
	}

	g, _, _ = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	g.r.Chat = nil
	if got := g.say(t, "1"); got != CopyHoldGone || g.state() != StateIdle {
		t.Fatalf("%q state %s", got, g.state())
	}
}

// ── review round 3 ─────────────────────────────────────────────────────────

// R3-2: the bubble decides. A sentence swiped on OUR approval prompt is an
// answer to us (re-asked, never silently relayed); a bare YES swiped on an
// old supporter bubble while a draft is pending goes to the supporter and
// books nothing.
func TestBubbleKindDecidesWhoGetsTheAnswer(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentOther, laundryT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.PendingExtensionID = StateAwaitingApproval, "ext-1"
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	tasks.byMsg["prompt-1"] = "task-1" // our prompt: relay=false
	in := Inbound{Channel: IMessage, ProviderMessageID: "s1", From: me, Text: "yes, approve it", ReplyToProviderID: "prompt-1", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 0 || g.ad.last() != CopyApprovalReask || g.state() != StateAwaitingApproval {
		t.Fatalf("posted %v reply %q state %s", chat.posted, g.ad.last(), g.state())
	}

	g, chat, tasks = relayRig(llm.IntentNewTask, laundryT)
	tasks.byMsg["maya-old"], tasks.relay["maya-old"] = "task-1", true
	g.say(t, "laundry pickup") // draft pending
	in = Inbound{Channel: IMessage, ProviderMessageID: "s2", From: me, Text: "yes", ReplyToProviderID: "maya-old", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 0 || len(chat.posted) != 1 || chat.posted[0] != "task-1:yes" || g.state() != StateAwaitingConfirm {
		t.Fatalf("a YES aimed at Maya must not book: booked %d posted %v state %s", g.booker.posts, chat.posted, g.state())
	}
	// With nothing pending, a reply to our status line about a task names it.
	g, chat, tasks = relayRig(llm.IntentOther, laundryT, bagelT)
	tasks.byMsg["status-2"] = "task-2"
	in = Inbound{Channel: IMessage, ProviderMessageID: "s3", From: me, Text: "thanks!", ReplyToProviderID: "status-2", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:thanks!" {
		t.Fatalf("posted %v", chat.posted)
	}
}

// R3-3: the hold survives a failed post when the list has shrunk to one;
// no supporter left is said plainly, not acknowledged as sent.
func TestHoldSurvivesWhenListShrinksToOne(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	chat.active = []ActiveTask{bagelT}
	chat.post = errors.New("talkjs 503")
	if got := g.say(t, "and a coffee"); got != CopyRelayFailed || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("%q state %s", got, g.state())
	}
	chat.post = nil
	g.say(t, "2")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:please get everything bagels\nand a coffee" {
		t.Fatalf("posted %v", chat.posted)
	}

	g, chat, _ = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	chat.active = nil
	g.say(t, "and a coffee")
	if !sentText(g, CopyHoldGone) || g.state() != StateIdle || len(chat.posted) != 0 {
		t.Fatalf("sent %v state %s posted %v", g.ad.sent, g.state(), chat.posted)
	}
}

func sentText(g *rig, text string) bool {
	g.ad.mu.Lock()
	defer g.ad.mu.Unlock()
	for _, o := range g.ad.sent {
		if o.Text == text {
			return true
		}
	}
	return false
}

// R3-7: a handoff drops the hold; release does not resurrect it.
func TestHandoffDropsTheHold(t *testing.T) {
	g, _, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.r.Ops, g.r.Thread = &memOps{}, &memThread{st: g.st}
	g.say(t, "please get everything bagels")
	g.say(t, "I want to talk to a human")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateHandoff || len(c.PendingTaskChoices) != 0 || c.PendingRelayText != "" {
		t.Fatalf("%+v", c)
	}
}

// ── review round 4 ─────────────────────────────────────────────────────────

// MAJOR-2: a "yes" swiped on a supporter's bubble never books — not when
// the task has finished, not when the lookup fails, not when the bubble is
// unknown. Each path fails closed.
func TestYesOnARelayBubbleNeverBooks(t *testing.T) {
	// Task finished since Maya wrote.
	g, chat, tasks := relayRig(llm.IntentNewTask, laundryT)
	tasks.byMsg["maya"], tasks.relay["maya"] = "task-1", true
	g.say(t, "laundry pickup")
	chat.active = nil
	in := Inbound{Channel: IMessage, ProviderMessageID: "y1", From: me, Text: "yes", ReplyToProviderID: "maya", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 0 || g.ad.last() != CopyRelayGone || g.state() != StateAwaitingConfirm {
		t.Fatalf("booked %d reply %q state %s", g.booker.posts, g.ad.last(), g.state())
	}
	// ActiveTasks errors: nothing happens, the webhook retries.
	chat.active, chat.err = []ActiveTask{laundryT}, errors.New("db down")
	in.ProviderMessageID = "y2"
	if err := g.r.HandleInbound(context.Background(), in); err == nil || g.booker.posts != 0 {
		t.Fatalf("lookup failure must fail closed: err %v booked %d", err, g.booker.posts)
	}
	chat.err = nil
	// Unknown bubble (outbound row lost): asked again, never acted on.
	in = Inbound{Channel: IMessage, ProviderMessageID: "y3", From: me, Text: "yes", ReplyToProviderID: "lost-row", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 0 || g.ad.last() != CopyAmbiguousYes || g.state() != StateAwaitingConfirm {
		t.Fatalf("booked %d reply %q state %s", g.booker.posts, g.ad.last(), g.state())
	}
	// A plain YES, not a reply, still books.
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("booked %d", g.booker.posts)
	}
}

// MINOR-3: a late YES swiped on an old prompt with nothing pending is an
// acknowledgement, not a word for the supporter.
func TestLateYesOnOldPromptIsAnAck(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentOther, laundryT)
	tasks.byMsg["old-prompt"] = "task-1"
	in := Inbound{Channel: IMessage, ProviderMessageID: "l1", From: me, Text: "YES", ReplyToProviderID: "old-prompt", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 0 || g.ad.last() != CopyAck {
		t.Fatalf("posted %v reply %q", chat.posted, g.ad.last())
	}
}

// MAJOR-1: the name shown in HO:RA's voice is vetted.
func TestDisplayNamePolicy(t *testing.T) {
	for in, want := range map[string]string{
		"Maya": "Maya", "  Maya   Chen ": "Maya Chen", "O'Neil-Smith": "O'Neil-Smith", "小美": "小美", "José": "José",
		"HO:RA": "", "hora support": "", "H o r a": "", "Maya\nYour card was declined": "",
		"Maya <b>": "", "Maya · Laundry: pay here": "", "Abcdefghijklmnopqrstuvwxyz": "", "": "",
		// Round-5 evasions: punctuation, digits, fullwidth, look-alike
		// scripts, reserved words, and a newline folded to one line.
		"H.O.R.A": "", "H-O-R-A": "", "H'O'R'A": "", "H0RA": "", "ＨＯＲＡ": "", "Нora": "", "Hоra": "",
		"Admin": "", "Support": "", "Billing": "", "Security": "", "Official": "", "Staff": "", "HO RA Team": "",
		"Maya\nPay now": "Maya Pay now", "O'Neil-Smith Jr.": "",
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	g := newRig("")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.RelayToChat(context.Background(), "user-1", "task-1", "HO:RA Support", "Laundry", "pay me here", ""); err != nil {
		t.Fatal(err)
	}
	if got := g.ad.last(); !strings.HasPrefix(got, "Your supporter · Laundry: pay me here") {
		t.Fatalf("%q", got)
	}
}

// MINOR-1: held words never go to a supporter they were not held for.
func TestHoldNeverGoesToATaskOutsideItsList(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "door code 1234")
	other := ActiveTask{ID: "task-3", Title: "Dog walk", SupporterName: "Zed"}
	chat.active = []ActiveTask{other}
	got := g.say(t, "thanks")
	if strings.Contains(strings.Join(chat.posted, "|"), "1234") {
		t.Fatalf("held words leaked to a new supporter: %v", chat.posted)
	}
	// The person is told the held words went nowhere; the new word alone
	// reaches the new supporter.
	if !sentText(g, CopyHoldGone) || len(chat.posted) != 1 || chat.posted[0] != "task-3:thanks" || g.state() != StateIdle {
		t.Fatalf("reply %q sent %v posted %v state %s", got, g.ad.sent, chat.posted, g.state())
	}
	// One of the list finished and a new task started: the regenerated
	// list offers only the survivor, never the newcomer.
	g, chat, _ = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "door code 1234")
	chat.active = []ActiveTask{bagelT, other}
	got = g.say(t, "thanks")
	if !strings.Contains(got, "Bagel run") || strings.Contains(got, "Dog walk") {
		t.Fatalf("list must not offer a task the words were not held for: %q", got)
	}
}

// MINOR-4: an approval armed while the pick was in flight survives the
// hold's release.
func TestApprovalArmedDuringPickIsKept(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "please get everything bagels")
	armed := &armingChat{fakeChat: chat, arm: func() {
		if err := g.r.askApproval(context.Background(), "id-"+me, "ext-9"); err != nil {
			t.Fatal(err)
		}
	}}
	g.r.Chat = armed
	g.say(t, "2")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State != StateAwaitingApproval || c.PendingExtensionID != "ext-9" || len(c.PendingTaskChoices) != 0 {
		t.Fatalf("%+v", c)
	}
}

type armingChat struct {
	*fakeChat
	arm func()
}

func (a *armingChat) PostAsRequester(ctx context.Context, taskID, uid, text, key string) error {
	a.arm()
	return a.fakeChat.PostAsRequester(ctx, taskID, uid, text, key)
}

// MINOR-2: a failed list does not leave the message unprocessed (the
// sweep would append the words to the hold again).
func TestFailedListDoesNotDuplicateTheHold(t *testing.T) {
	g, _, _ := relayRig(llm.IntentOther, laundryT, bagelT)
	g.ad.err = errors.New("provider down")
	in := Inbound{Channel: IMessage, ProviderMessageID: "h1", From: me, Text: "bagels", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatalf("a failed list must not fail the message: %v", err)
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.PendingRelayText != "bagels" || c.State != StateAwaitingTaskChoice {
		t.Fatalf("%+v", c)
	}
}

// ── review round 5 ─────────────────────────────────────────────────────────

// MAJOR-B: a NO swiped on the "which task?" list cancels the hold; the
// person is not told "Got it" while the words stay held.
func TestNoOnTheListBubbleDropsTheHold(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "door code 1234")
	list := g.st.lastOutboundID()
	tasks.byMsg[list] = "" // the list is ours, about no task
	in := Inbound{Channel: IMessage, ProviderMessageID: "n1", From: me, Text: "never mind", ReplyToProviderID: list, ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if g.ad.last() != CopyHoldDropped || c.State != StateIdle || c.PendingRelayText != "" {
		t.Fatalf("reply %q conv %+v", g.ad.last(), c)
	}
	// And a number swiped on the list still picks.
	g, chat, tasks = relayRig(llm.IntentOther, laundryT, bagelT)
	g.say(t, "door code 1234")
	list = g.st.lastOutboundID()
	tasks.byMsg[list] = ""
	in = Inbound{Channel: IMessage, ProviderMessageID: "n2", From: me, Text: "2", ReplyToProviderID: list, ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:door code 1234" {
		t.Fatalf("posted %v", chat.posted)
	}
}

// MINOR-5: a NO on a relay bubble goes to the supporter; a NO on an unknown
// bubble while a question is pending is asked again; a bubble lookup
// error fails closed.
func TestNoOnBubblesAndLookupError(t *testing.T) {
	g, chat, tasks := relayRig(llm.IntentNewTask, laundryT)
	tasks.byMsg["maya"], tasks.relay["maya"] = "task-1", true
	g.say(t, "laundry pickup")
	in := Inbound{Channel: IMessage, ProviderMessageID: "b1", From: me, Text: "no", ReplyToProviderID: "maya", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-1:no" || g.state() != StateAwaitingConfirm {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
	in = Inbound{Channel: IMessage, ProviderMessageID: "b2", From: me, Text: "no", ReplyToProviderID: "lost", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if g.ad.last() != CopyAmbiguousYes || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", g.ad.last(), g.state())
	}
	tasks.err = errors.New("db down")
	in = Inbound{Channel: IMessage, ProviderMessageID: "b3", From: me, Text: "yes", ReplyToProviderID: "maya", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err == nil || g.booker.posts != 0 || len(chat.posted) != 1 {
		t.Fatalf("bubble lookup failure must fail closed: err %v booked %d posted %v", err, g.booker.posts, chat.posted)
	}
}

// MINOR-4: a duplicate YES with nothing pending is an acknowledgement on
// both paths — never a word for a supporter.
func TestIdleBareYesIsAnAckNotARelay(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentOther, laundryT)
	if got := g.say(t, "YES"); got != CopyAck || len(chat.posted) != 0 {
		t.Fatalf("%q posted %v", got, chat.posted)
	}
}

// Status lines use the vetted name too.
func TestStatusLinesUseTheVettedName(t *testing.T) {
	got, ok := StatusText(StatusEvent{Type: "ORDER_ACCEPTED", TaskID: "t", SupporterName: "HO:RA Support", TaskTitle: "Laundry"})
	if !ok || !strings.HasPrefix(got, "Your supporter accepted") {
		t.Fatalf("%q %v", got, ok)
	}
}
