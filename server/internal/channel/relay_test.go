package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

type fakeChat struct {
	active []ActiveTask
	err    error
	posted []string // "task:text"
	post   error
}

func (f *fakeChat) ActiveTasks(context.Context, string) ([]ActiveTask, error) { return f.active, f.err }
func (f *fakeChat) PostAsRequester(_ context.Context, taskID, _ string, text string) error {
	if f.post != nil {
		return f.post
	}
	f.posted = append(f.posted, taskID+":"+text)
	return nil
}

type fakeTasks struct {
	byMsg map[string]string
	set   []string
}

func (f *fakeTasks) TaskForProviderMessage(_ context.Context, _ Channel, id string) (string, error) {
	return f.byMsg[id], nil
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
	tasks := &fakeTasks{byMsg: map[string]string{}}
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
	}{{llm.IntentOther, "yes"}, {llm.IntentQuestion, "where are you now?"}, {llm.IntentGreeting, "hi!"}} {
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
	if !strings.HasPrefix(got, "Maya · Laundry pickup: Want the large bag too?") || !strings.Contains(got, "A plain YES or NO here still answers my question") || !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("%q", got)
	}
	// An inline reply to Maya's message is for Maya: nothing books.
	tasks.byMsg[g.st.lastOutboundID()] = "task-1"
	in := Inbound{Channel: IMessage, ProviderMessageID: "r1", From: me, Text: "yes please", ReplyToProviderID: g.st.lastOutboundID(), ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(chat.posted) != 1 || chat.posted[0] != "task-1:yes please" || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posted %v booked %d state %s", chat.posted, g.booker.posts, g.state())
	}
	// A bare YES, not in reply to anything, still answers the pending draft.
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("booked %d", g.booker.posts)
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
	if got := g.ad.last(); !strings.Contains(got, "still answers my question about Maya’s request") {
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
