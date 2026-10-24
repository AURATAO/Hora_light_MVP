package channel

import (
	"context"
	"errors"
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

// R-2: a no with a reason, and "no thanks", decline ("no problem" never
// does — it approves, TestNoProblemApprovesRightAfterTheQuestion); with the supporter mid-conversation only a bare no declines.
func TestApprovalNoWithReason(t *testing.T) {
	for _, tc := range []struct {
		msg      string
		declined bool
	}{
		{"No, he needs to finish now", true}, {"no thanks", true}, {"nope, too much", true},
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
		len(chat.posted) != 1 || chat.posted[0] != "task-2:"+CopyToSupporterHurry || !strings.Contains(got, "speed up") { // polite template (Arbiter 2026-10-24)
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
	// Not an approval (I-AE: "he's here" is not a harmless tail): passed on,
	// the request stays open.
	if len(g.booker.resolved) != 0 || g.state() != StateAwaitingApproval {
		t.Fatalf("resolved %v posted %v state %s", g.booker.resolved, chat.posted, g.state())
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

// Review round 1, R-1: a YES sent after the summary was stamped but while
// it was still on its way — processed after delivery — does not book.
func TestYesWhileTheSummaryIsOnItsWay(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	var early Inbound
	hooked := &hookedAdapter{fakeSendAdapter: g.ad}
	g.r.Adapters[IMessage] = hooked
	hooked.during = func() { // the summary is being sent: the person taps YES now
		early = Inbound{Channel: IMessage, ProviderMessageID: "early", From: me, Text: "YES", ReceivedAt: time.Now()}
	}
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	time.Sleep(time.Millisecond)
	if err := g.r.HandleInbound(context.Background(), early); err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posts %d state %s", g.booker.posts, g.state())
	}
}

// R-1 at the retry prompt: the card was declined; a second tap sent with
// the first must not retry the charge unseen.
func TestDoubleTapAfterADeclinedCardDoesNotRetry(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	g.booker.result = PostResult{Outcome: PostDeclined, Message: "Your card was declined."}
	tap := time.Now()
	time.Sleep(5 * time.Millisecond)
	for i, id := range []string{"t1", "t2"} {
		if err := g.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: id, From: me, Text: "YES", ReceivedAt: tap.Add(time.Duration(i) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	if g.booker.posts != 1 {
		t.Fatalf("the second tap retried the charge: posts %d", g.booker.posts)
	}
}

// R-3: a reaction word with a message right after the question approves
// and passes the message on; the relay failing or the request having
// ended is said in the same reply.
func TestYesWithMessageEdges(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	got := g.say(t, "great, thanks Sam!")
	if len(g.booker.resolved) != 1 || len(chat.posted) != 1 || chat.posted[0] != "task-2:"+CopyToSupporterThanks || !strings.Contains(got, "thanks to Sam") { // polite template (Arbiter 2026-10-24)
		t.Fatalf("resolved %v posted %v reply %q", g.booker.resolved, chat.posted, got)
	}
	g, chat, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	chat.post = errors.New("talkjs 503")
	sent := len(g.ad.sent)
	got = g.say(t, "yes but tell him to hurry")
	if len(g.booker.resolved) != 1 || len(g.ad.sent)-sent != 1 || !strings.Contains(got, "Couldn't pass") {
		t.Fatalf("one reply with the failure: sent %d reply %q", len(g.ad.sent)-sent, got)
	}
	g, chat, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	g.booker.extResult = ExtensionResult{Outcome: ExtAlready, Status: "expired", TaskID: "task-2"}
	got = g.say(t, "yes but tell him to hurry")
	if len(chat.posted) != 0 || !strings.Contains(got, CopyNotPassedOn) {
		t.Fatalf("posted %v reply %q", chat.posted, got)
	}
	// No task to pass the words to: nothing approved on half a message.
	g, _, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.ApprovalTaskID = ""
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	g.say(t, "yes but tell him to hurry")
	if len(g.booker.resolved) != 0 {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
}

// R-2: a mixed no is passed on, the request stays open.
func TestMixedNoIsPassedOn(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	g.say(t, "no! go ahead")
	if len(g.booker.resolved) != 0 || len(chat.posted) != 1 || g.state() != StateAwaitingApproval {
		t.Fatalf("resolved %v posted %v state %s", g.booker.resolved, chat.posted, g.state())
	}
}

// The guards cover the wider words: sent before the question, or while it
// is unconfirmed, "Sounds good" moves nothing.
func TestGuardsCoverTheWiderWords(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	in := Inbound{Channel: IMessage, ProviderMessageID: "sg", From: me, Text: "Sounds good", ReceivedAt: time.Now().Add(-time.Minute)}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 0 {
		t.Fatalf("sent before: resolved %v", g.booker.resolved)
	}
	g, _, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", ExtensionID: "ext-9", Type: evBudgetRequested, Approval: PendingApproval{ID: "ext-9", Kind: "budget", Cents: 1000}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
	if ok, err := g.convs.ClaimHold(context.Background(), "id-"+me, "ext-9", "task-2", time.Now()); err != nil || !ok {
		t.Fatal(err)
	}
	if got := g.say(t, "yes but tell him to hurry"); len(g.booker.resolved) != 0 || got != CopyApprovalOnItsWay {
		t.Fatalf("unconfirmed: resolved %v reply %q", g.booker.resolved, got)
	}
}

// The outbound stamp is taken before the send and its failure fails the
// send: nothing goes out unstamped.
func TestSendFailsClosedWithoutAStamp(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.say(t, "hi") // a conversation row exists
	g.convs.touchErr = errors.New("db down")
	sent := len(g.ad.sent)
	if err := g.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "x"}, ""); err == nil || len(g.ad.sent) != sent {
		t.Fatalf("err %v sent %d", err, len(g.ad.sent)-sent)
	}
}

// Review round 2: a reply swiped on a message we cannot identify is asked
// again — the wider approval words included, not only yesRE.
func TestUnknownBubbleCoversTheWiderWords(t *testing.T) {
	for _, msg := range []string{"Sounds good", "100%", "Great", "yes but tell him to hurry", "no thanks"} {
		g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		in := Inbound{Channel: IMessage, ProviderMessageID: "u-" + msg, From: me, Text: msg, ReplyToProviderID: "not-on-record", ReceivedAt: time.Now()}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if len(g.booker.resolved) != 0 || g.ad.last() != CopyAmbiguousYes {
			t.Errorf("%q: resolved %v last %q", msg, g.booker.resolved, g.ad.last())
		}
	}
}

// Review round 2, R-1: a YES processed WHILE the retry prompt is being
// sent (after the old summary, before the new prompt) does not retry the
// charge.
func TestYesDuringTheRetryPromptDoesNotRetry(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	g.booker.result = PostResult{Outcome: PostDeclined, Message: "Your card was declined."}
	hooked := &hookedAdapter{fakeSendAdapter: g.ad}
	g.r.Adapters[IMessage] = hooked
	sentAfterSummary := time.Now()
	time.Sleep(2 * time.Millisecond)
	hooked.during = func() { // the first YES's reply ("declined… YES to retry") is going out
		hooked.during = nil
		in := Inbound{Channel: IMessage, ProviderMessageID: "yes-2", From: me, Text: "YES", ReceivedAt: sentAfterSummary}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Error(err)
		}
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("the second YES retried the charge: posts %d", g.booker.posts)
	}
}

// Review round 2: a pass-on that fails still says the question waits.
func TestFailedPassOnStillSaysTheQuestionWaits(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	chat.post = errors.New("talkjs 503")
	got := g.say(t, "yes but max $5")
	if len(g.booker.resolved) != 0 || !strings.Contains(got, "Couldn't pass") || !strings.Contains(got, CopyStillWaitingApproval) {
		t.Fatalf("resolved %v reply %q", g.booker.resolved, got)
	}
}

// Review round 3: the harmless-tail allowlist (I-AE). Unlisted tails —
// Chinese, Spanish/Italian, English deferrals — are passed on, never
// approved; listed instructions still approve and pass on.
func TestHarmlessTailAllowlist(t *testing.T) {
	notApproved := []string{
		"好，但是太貴了", "可以，太多了", "好，我覺得有點高",
		"ok, I'll get back to you", "sure, I'll let you know", "yes I'll see", "ok I'll consider it", "yes I'll decide", "ok figure it out",
		"ok, si es barato", "sì, se costa poco", "sí, es mucho", "sì, è troppo", "sí, es caro", "sì, ci penso", "vale, lo pienso",
		"ok manana", "ok piu tardi", "ok lesss", "yes unlesss he's late", "ok threee", "yes steeep",
		"yes un­less he's late", "ok i​f cheap",
	}
	for _, msg := range notApproved {
		_, _, c, ok := yesWithMessage(msg)
		if y, _ := approvalYes(msg); y || (ok && !c) {
			t.Errorf("%q approves: plain=%v ok=%v conditional=%v", msg, y, ok, c)
		}
	}
	for _, msg := range []string{"yes but tell him to hurry", "ok, thanks Alex!", "yes please hurry", "sure, let him know", "yes, tell Alex thanks", "ok thank you alex", "ok hurry up!"} {
		if _, _, c, ok := yesWithMessage(msg, "Alex"); !ok || c {
			t.Errorf("%q: a harmless shape should approve and pass on (ok=%v cond=%v)", msg, ok, c)
		}
	}
	// Round 4: anything outside the closed shapes is passed on — the
	// first word being harmless is not enough.
	for _, msg := range []string{
		"yes, get the cheapest", "ok pick the smaller", "yes get the small", "ok get the generic",
		"yeah, nice try", "sure lol", "ok, cool story",
		"ok thanks anyway", "ok thanks, I'd rather he left", "ok thanks, I'm good",
		"yes, tell him sorry I changed my mind", "yes hurry, I'm changing my mind",
		"ok get someone else", "ok pick another", "ok thanks, demasiado",
		"yes, tell him to skip it", "yes, tell him to leave", "yes and grab napkins", "ok, thanks Bob",
	} {
		_, _, c, ok := yesWithMessage(msg, "Alex")
		if y, _ := approvalYes(msg); y || (ok && !c) {
			t.Errorf("%q approves: plain=%v ok=%v conditional=%v", msg, y, ok, c)
		}
	}
	// A strong yes beside a negation is mixed, not a decline; a negated
	// yes is a decline (round 4).
	for _, msg := range []string{"no, don't worry, go ahead", "no, I don't mind", "no, I approve", "no, don't worry, it's fine", "no, don't worry, sounds good", "nope, no worries"} {
		if approvalNo(msg) || !mixedNo(msg) {
			t.Errorf("%q: mixed (no=%v mixed=%v)", msg, approvalNo(msg), mixedNo(msg))
		}
	}
	for _, msg := range []string{"no, don't do it", "no, don't go ahead", "no, I don't approve", "no, not sure"} {
		if !approvalNo(msg) || mixedNo(msg) {
			t.Errorf("%q: a decline (no=%v mixed=%v)", msg, approvalNo(msg), mixedNo(msg))
		}
	}
}

// Review round 3, R-1: while the retry prompt (or any question of ours) is
// in flight, no booking YES counts — even one sent after the stamp.
func TestYesWhileTheQuestionIsInFlight(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	g.booker.result = PostResult{Outcome: PostDeclined, Message: "Your card was declined."}
	hooked := &hookedAdapter{fakeSendAdapter: g.ad}
	g.r.Adapters[IMessage] = hooked
	hooked.during = func() {
		hooked.during = nil
		in := Inbound{Channel: IMessage, ProviderMessageID: "yes-late", From: me, Text: "YES", ReceivedAt: time.Now()} // after the stamp, before delivery
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Error(err)
		}
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("a YES during the retry prompt retried: posts %d", g.booker.posts)
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.QuestionShownAt == nil || !c.QuestionShownAt.Before(ShownUnconfirmed) {
		t.Fatalf("the in-flight mark must be replaced after delivery: %+v", c.QuestionShownAt)
	}
}

// A draft from before R-1 (no stamp) is put again once, not booked blind;
// a stale awaiting_confirm with no draft goes back to idle.
func TestLegacyAndEmptyDrafts(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.QuestionShownAt = nil
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	g.say(t, "YES")
	if g.booker.posts != 0 {
		t.Fatalf("legacy draft booked blind: posts %d", g.booker.posts)
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("after the re-show: posts %d", g.booker.posts)
	}
	g, _, _, _ = contextRig(llm.IntentNewTask, bagelT)
	c, _ = g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.Pending, c.QuestionShownAt = StateAwaitingConfirm, nil, nil
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	g.say(t, "YES")
	if g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
}

// WhatsApp swipes (round 4): on the question itself "Great" answers it;
// on another message a reaction is asked again, a conditional message is
// still passed on, and a strict YES still approves.
func TestWhatsAppSwipes(t *testing.T) {
	setup := func(t *testing.T) (*rig, *fakeChat, *fakeTasks, string) {
		t.Helper()
		g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.r.Adapters[WhatsApp] = g.ad
		g.ids.link(me, "user-1")
		g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: ptrTime(time.Now())}}
		g.convs.setLastInbound("id-"+me, ptrTime(time.Now()))
		g.arm(t, "ext-2")
		c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
		if c.ApprovalQuestionPID == "" {
			t.Fatal("the question's own message id must be kept")
		}
		tasks.byMsg[c.ApprovalQuestionPID] = "task-2"
		tasks.byMsg["status-1"] = "task-2" // a status line of ours, same task
		return g, chat, tasks, c.ApprovalQuestionPID
	}
	send := func(t *testing.T, g *rig, text, replyTo string) {
		t.Helper()
		in := Inbound{Channel: WhatsApp, ProviderMessageID: "w-" + text + replyTo, From: me, Text: text, ReplyToProviderID: replyTo, ReceivedAt: time.Now()}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	g, _, _, q := setup(t)
	send(t, g, "Great", q)
	if len(g.booker.resolved) != 1 {
		t.Fatalf("swipe on the question: resolved %v", g.booker.resolved)
	}
	g, _, _, _ = setup(t)
	send(t, g, "Great", "status-1")
	if len(g.booker.resolved) != 0 || g.ad.last() != CopyApprovalReask || g.state() != StateAwaitingApproval {
		t.Fatalf("swipe elsewhere: resolved %v last %q state %s", g.booker.resolved, g.ad.last(), g.state())
	}
	g, chat, _, _ := setup(t)
	send(t, g, "yes but max $5", "status-1")
	if len(g.booker.resolved) != 0 || len(chat.posted) != 1 || g.state() != StateAwaitingApproval {
		t.Fatalf("conditional swipe: resolved %v posted %v state %s", g.booker.resolved, chat.posted, g.state())
	}
	g, _, _, _ = setup(t)
	send(t, g, "yes", "status-1")
	if len(g.booker.resolved) != 1 {
		t.Fatalf("strict YES swiped elsewhere: resolved %v", g.booker.resolved)
	}
}

// Round 4: a draft-less awaiting_confirm with a valid, delivered stamp
// goes idle on any message; and after a resolution that left the in-flight
// mark (no resume line was sent), the next YES re-shows the summary and the
// one after books.
func TestConfirmEdgesRound4(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.Pending, c.QuestionShownAt = StateAwaitingConfirm, nil, ptrTime(time.Now().Add(-time.Hour))
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	g.say(t, "YES")
	if g.state() != StateIdle || g.booker.posts != 0 {
		t.Fatalf("state %s posts %d", g.state(), g.booker.posts)
	}
	g, _, _, _ = contextRig(llm.IntentToSupporter, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	g.arm(t, "ext-2")
	g.booker.pendingExt = map[string]bool{}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evResolved, TaskID: "task-2", ExtensionID: "ext-2", Title: "A request timed out"}); err != nil {
		t.Fatal(err)
	}
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	g.say(t, "YES") // in-flight mark left by the clear: re-shown
	if g.booker.posts != 0 {
		t.Fatalf("posts %d", g.booker.posts)
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("after the re-show: posts %d", g.booker.posts)
	}
}

// Round 5, finding 1: negation is read within its own clause — "no, never
// mind, go ahead" is not a decline (it is mixed: passed on, kept open);
// a negated yes-word is a decline, contractions included (finding 4).
func TestNegationIsScopedToItsClause(t *testing.T) {
	for _, msg := range []string{"no, never mind, go ahead", "no, I don't care, go ahead", "nope, never mind, sure", "no, don't ask, do it", "no, nvm, go ahead"} {
		if !mixedNo(msg) || approvalNo(msg) {
			t.Errorf("%q: must be mixed (passed on), not declined", msg)
		}
	}
	for _, msg := range []string{"no, I can't approve this", "no, I won't approve", "no, I'm not fine with that", "no, I didn't say yes", "no, it shouldn't be approved"} {
		if !approvalNo(msg) {
			t.Errorf("%q: must decline", msg)
		}
	}
	// End to end: the mixed no is not a decline.
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	g.say(t, "no, never mind, go ahead")
	if len(g.booker.resolved) != 0 || len(chat.posted) != 1 || g.state() != StateAwaitingApproval {
		t.Fatalf("resolved %v posted %v state %s", g.booker.resolved, chat.posted, g.state())
	}
}

// Round 5, findings 2–3 and 7: the supporter's name. A ruled marker in the
// raw tail is never washed out by the name (Max, Min); non-ASCII names
// substitute; names that are shape words change nothing; a failed lookup
// fails closed (relayed, kept open).
func TestSupporterNameInTheTail(t *testing.T) {
	for _, tc := range []struct {
		name, msg string
		approve   bool
	}{
		{"Max", "yes, thanks Max", false},
		{"Min", "ok thanks min!", false},
		{"José", "yes, thanks José", true},
		{"Zoë", "sure, tell Zoë to hurry", true},
		{"王小明", "yes thanks 王小明", true},
		{"Him", "yes, tell him to hurry", true},
		{"Please", "yes please", true},
		{"Ty", "yes ty", true},
		{"Sam Lee", "yes thanks Sam", true},
		{"Sam", "yes thanks Bob", false},
	} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, ActiveTask{ID: "task-2", Title: "Bagel run", SupporterName: tc.name})
		g.arm(t, "ext-2")
		g.say(t, tc.msg)
		approved := len(g.booker.resolved) == 1 && strings.HasSuffix(g.booker.resolved[0], ":ext-2:true")
		if approved != tc.approve || (!tc.approve && (len(g.booker.resolved) != 0 || len(chat.posted) != 1)) {
			t.Errorf("%s / %q: resolved %v posted %v", tc.name, tc.msg, g.booker.resolved, chat.posted)
		}
	}
	// ActiveTasks failing: "thanks Sam" cannot be checked — relayed... and
	// with the chat down, nothing approved either way.
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	chat.err = errors.New("talkjs 503")
	g.say(t, "yes, thanks Sam")
	if len(g.booker.resolved) != 0 {
		t.Fatalf("lookup failed: resolved %v", g.booker.resolved)
	}
}

// Round 5, finding 6: a stale draft-less awaiting_confirm reads the
// message as if idle — a real errand is drafted, not swallowed.
func TestDraftlessConfirmDraftsTheErrand(t *testing.T) {
	g, _, _, _ := contextRig(llm.IntentNewTask, bagelT)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.State, c.Pending = StateAwaitingConfirm, nil
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	g.say(t, "new task: book a cab at 5")
	if c2, _ := g.convs.GetConversation(context.Background(), "id-"+me); c2.Pending == nil {
		t.Fatalf("the errand was swallowed: state %s last %q", c2.State, g.ad.last())
	}
}

// Round 5, finding 9: an approving yes + message swiped on the question
// approves; swiped elsewhere it is asked again; an empty question pid
// (none returned) makes every swiped reaction a re-ask, a bare YES still
// approves.
func TestWhatsAppSwipesRound5(t *testing.T) {
	setup := func(t *testing.T) (*rig, string) {
		t.Helper()
		g, _, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.r.Adapters[WhatsApp] = g.ad
		g.ids.link(me, "user-1")
		g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: ptrTime(time.Now())}}
		g.convs.setLastInbound("id-"+me, ptrTime(time.Now()))
		g.arm(t, "ext-2")
		c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
		tasks.byMsg[c.ApprovalQuestionPID] = "task-2"
		tasks.byMsg["status-1"] = "task-2"
		return g, c.ApprovalQuestionPID
	}
	send := func(t *testing.T, g *rig, text, replyTo string) {
		t.Helper()
		in := Inbound{Channel: WhatsApp, ProviderMessageID: "w-" + text + replyTo, From: me, Text: text, ReplyToProviderID: replyTo, ReceivedAt: time.Now()}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	g, q := setup(t)
	send(t, g, "yes please hurry", q)
	if len(g.booker.resolved) != 1 {
		t.Fatalf("on the question: resolved %v", g.booker.resolved)
	}
	g, _ = setup(t)
	send(t, g, "yes please hurry", "status-1")
	if len(g.booker.resolved) != 0 || g.ad.last() != CopyApprovalReask {
		t.Fatalf("elsewhere: resolved %v last %q", g.booker.resolved, g.ad.last())
	}
	g, _ = setup(t)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.ApprovalQuestionPID = ""
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	send(t, g, "Great", "status-1")
	if len(g.booker.resolved) != 0 {
		t.Fatalf("empty pid, reaction: resolved %v", g.booker.resolved)
	}
	send(t, g, "YES", "status-1")
	if len(g.booker.resolved) != 1 {
		t.Fatalf("empty pid, YES: resolved %v", g.booker.resolved)
	}
}

// Arbiter 2026-10-22: "no problem" / "no worries" approve — as reaction
// words, so only when our last message is the question ("no worries" to
// "Sam is running 5 min late" is about that, and is asked again).
func TestNoProblemApprovesRightAfterTheQuestion(t *testing.T) {
	for _, msg := range []string{"no problem", "No worries!", "no problem 👍"} {
		g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		g.say(t, msg)
		if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-2:true") {
			t.Errorf("%q: resolved %v", msg, g.booker.resolved)
		}
	}
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: "CLOCK_IN", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam"}); err != nil {
		t.Fatal(err)
	}
	g.say(t, "no worries")
	if len(g.booker.resolved) != 0 || g.ad.last() != CopyApprovalReask {
		t.Fatalf("after a status line: resolved %v last %q", g.booker.resolved, g.ad.last())
	}
}

// Arbiter 2026-10-22 16:51: "yes that's fine" approves; Chinese particles
// 啊 呀 喔 哦 囉 啦 do not change the answer; 吧 / 嘛 are asked again.
func TestThatsFineAndChineseParticles(t *testing.T) {
	for _, tc := range []struct {
		text string
		want ApprovalVerdict
	}{
		{"yes that's fine", VerdictApprove},
		{"Yes, that’s fine!", VerdictApprove},
		{"ok its fine", VerdictApprove},
		{"可以啊", VerdictApprove},
		{"可以呀！", VerdictApprove},
		{"好啊", VerdictApprove},
		{"好喔", VerdictApprove},
		{"可以啦啦", VerdictApprove},
		{"不要啦", VerdictDecline},
		{"不用喔", VerdictDecline},
		{"好吧", VerdictNotAnAnswer},
		{"可以嗎", VerdictNotAnAnswer},
		{"yes that's fine but max $5", VerdictPassOnKeep},
		{"no worries, get it", VerdictPassOnKeep},
	} {
		if got := DecideApproval(ApprovalFacts{Text: tc.text, QuestionIsLast: true}); got != tc.want {
			t.Errorf("%q: %d, want %d", tc.text, got, tc.want)
		}
	}
	// The booking YES keeps its own list (I-S).
	for _, msg := range []string{"可以啊", "yes that's fine"} {
		if IsYes(msg) {
			t.Errorf("%q must not book", msg)
		}
	}
}

// Review (words r1): the negative side of I-AL / I-AM, pinned.
func TestThatsFineAndParticlesNegatives(t *testing.T) {
	for _, tc := range []struct {
		f    ApprovalFacts
		want ApprovalVerdict
	}{
		{ApprovalFacts{Text: "可以吧", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "好嘛", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "可以啊嗎", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "好啊不要", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "ok啊", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "不要，可以啊", QuestionIsLast: true}, VerdictPassOnKeep},
		// Arbiter 2026-10-22 17:14: a no-word with a particle and a reason
		// declines. Other mid-message particles are not stripped (I-AM).
		{ApprovalFacts{Text: "不要啦，太貴了", QuestionIsLast: true}, VerdictDecline},
		{ApprovalFacts{Text: "不行喔，太晚了", QuestionIsLast: true}, VerdictDecline},
		{ApprovalFacts{Text: "好啊 thanks", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "yes that's fine?", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "yes, that's fine for now", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "ok it's fine I guess", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "no, that's fine", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "nope, it's fine", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "great that's fine", QuestionIsLast: false}, VerdictReask},
		// A plain yes swiped on another message approves, as "ok" does
		// (I-AG: plain-consent words are not limited) — pinned on purpose.
		{ApprovalFacts{Text: "yes that's fine", QuestionIsLast: true, SwipedElsewhere: true}, VerdictApprove},
		{ApprovalFacts{Text: "可以啊", QuestionIsLast: true, SupporterLive: true}, VerdictToSupporter},
		// Arbiter 2026-10-22 17:14: 不要啦 is a strict no — a decline even
		// with the supporter mid-conversation; 可以啊 still goes to them.
		{ApprovalFacts{Text: "不要啦", QuestionIsLast: true, SupporterLive: true}, VerdictDecline},
		{ApprovalFacts{Text: "取消啦", QuestionIsLast: true, SupporterLive: true}, VerdictDecline},
		{ApprovalFacts{Text: "不用喔", QuestionIsLast: true, SupporterLive: true}, VerdictToSupporter},
	} {
		if got := DecideApproval(tc.f); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.f, got, tc.want)
		}
	}
}

// Arbiter 2026-10-22 17:14, end to end: with the supporter writing in the
// last minutes, "不要啦" declines the request instead of going to them.
func TestBuYaoLaDeclinesUnderIP(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.arm(t, "ext-2")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: which one?", "task-2", time.Now().Add(-2*time.Minute)
	g.say(t, "不要啦")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":ext-2:false") || len(chat.posted) != 0 {
		t.Fatalf("resolved %v posted %v", g.booker.resolved, chat.posted)
	}
}

// Review r4: I-AN's edges — particles only (not letter collapsing or emoji),
// English no-words take no particles, and the router's I-P gate and
// DecideApproval agree.
func TestIANEdges(t *testing.T) {
	for _, tc := range []struct {
		f    ApprovalFacts
		want ApprovalVerdict
	}{
		{ApprovalFacts{Text: "nooo 😂", QuestionIsLast: true, SupporterLive: true}, VerdictToSupporter},
		{ApprovalFacts{Text: "Nooo", QuestionIsLast: true, SupporterLive: true}, VerdictToSupporter},
		{ApprovalFacts{Text: "no 👍", QuestionIsLast: true, SupporterLive: true}, VerdictToSupporter},
		{ApprovalFacts{Text: "no啦, too much", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "nope啦, too much", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "不要啦！", QuestionIsLast: true}, VerdictDecline},
		{ApprovalFacts{Text: "不行喔，可以啊", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "不要啦太貴了", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "不用啦，好", QuestionIsLast: true}, VerdictPassOnKeep},
	} {
		if got := DecideApproval(tc.f); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.f, got, tc.want)
		}
	}
}

// Review r4: the same I-P table through the router (casualToSupporter),
// and the booking state unchanged by I-AN.
func TestIPThroughTheRouter(t *testing.T) {
	for _, tc := range []struct {
		msg      string
		declined bool
		relayed  bool
	}{
		{"不要啦", true, false},
		{"不用喔", false, true},
		{"可以啊", false, true},
		{"ok", false, true},
		{"no", true, false},
	} {
		g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: which one?", "task-2", time.Now().Add(-2*time.Minute)
		g.say(t, tc.msg)
		declined := len(g.booker.resolved) == 1 && strings.HasSuffix(g.booker.resolved[0], ":false")
		if declined != tc.declined || (len(chat.posted) == 1) != tc.relayed || (tc.relayed && len(g.booker.resolved) != 0) {
			t.Errorf("%q: resolved %v posted %v", tc.msg, g.booker.resolved, chat.posted)
		}
	}
	// Strict forms with punctuation or full width decline too.
	for _, msg := range []string{"不要啦！", "不要啦～", "ＮＯ", "no,"} {
		g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: which one?", "task-2", time.Now().Add(-2*time.Minute)
		g.say(t, msg)
		if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":false") || len(chat.posted) != 0 {
			t.Errorf("%q: resolved %v posted %v", msg, g.booker.resolved, chat.posted)
		}
	}
	// Booking with the supporter writing: I-AN does not apply (I-S). "ok"
	// is caught by the I-P gate (no model call); "不要啦" is not — it goes
	// on to the summary's own handling (the model is asked).
	for _, tc := range []struct {
		msg       string
		modelCall int
	}{{"ok", 0}, {"不要啦", 1}} {
		g, chat, tasks, fc := contextRig(llm.IntentToSupporter, bagelT)
		g.say(t, "new task: pick up my laundry tomorrow at 3")
		tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: which one?", "task-2", time.Now().Add(-2*time.Minute)
		before := len(fc.calls)
		g.say(t, tc.msg)
		c, err := g.convs.GetConversation(context.Background(), "id-"+me)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(fc.calls) - before; got != tc.modelCall || g.booker.posts != 0 || c.Pending == nil || len(chat.posted) != 1 {
			t.Errorf("booking %q: model calls %d posts %d pending %v posted %v", tc.msg, got, g.booker.posts, c.Pending != nil, chat.posted)
		}
	}
}

// Review r6: strictForm trims only trailing ! . , ~ (half and full width)
// around particles — never "?" or an emoticon.
func TestStrictFormEdges(t *testing.T) {
	for msg, want := range map[string]bool{
		"不要啦！": true, "不要啦～": true, "不要啦。": true, "取消啦...": true, "ＮＯ": true, "no,": true, "No!!": true,
		"no?": false, "不要啦??": false, "no :)": false, "no!?": false, "nooo": false, "no 😂": false, "不要！啦": false,
	} {
		if got := approvalStrictAnswer(msg); got != want {
			t.Errorf("%q: strict=%v, want %v", msg, got, want)
		}
	}
}

// Arbiter 2026-10-24: "Well ok, but hurry up!" approves; a leading
// interjection is dropped only before a yes ("oh no, he's late" is not a
// decline); with the supporter mid-conversation "well ok" is casual.
func TestLeadingInterjection(t *testing.T) {
	for _, tc := range []struct {
		f    ApprovalFacts
		want ApprovalVerdict
	}{
		{ApprovalFacts{Text: "Well ok, but hurry up!", QuestionIsLast: true}, VerdictApprovePassOn},
		{ApprovalFacts{Text: "oh yes", QuestionIsLast: true}, VerdictApprove},
		{ApprovalFacts{Text: "ah sure", QuestionIsLast: true}, VerdictApprove},
		{ApprovalFacts{Text: "oh no, he's late", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "well, max $5", QuestionIsLast: true}, VerdictNotAnAnswer},
		{ApprovalFacts{Text: "well ok but max $5", QuestionIsLast: true}, VerdictPassOnKeep},
	} {
		if got := DecideApproval(tc.f); got != tc.want {
			t.Errorf("%q: %d, want %d", tc.f.Text, got, tc.want)
		}
	}
	if !approvalCasual("well ok") {
		t.Error(`"well ok" must be casual under I-P`)
	}
}

// Arbiter 2026-10-24: the supporter gets a polite fixed line, the person is
// told what was said.
func TestApprovalPassOnTemplates(t *testing.T) {
	for _, tc := range []struct{ msg, toSupporter, toPerson string }{
		{"Yes but hurry up", CopyToSupporterHurry, "I've asked Sam to speed up a little."},
		{"Well ok, but hurry up!", CopyToSupporterHurry, "I've asked Sam to speed up a little."},
		{"yes, tell him to hurry", CopyToSupporterHurry, "I've asked Sam to speed up a little."},
		{"great, thanks Sam!", CopyToSupporterThanks, "I passed your thanks to Sam."},
		{"ok, let him know", CopyToSupporterLetKnow, "I've let Sam know."},
	} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.arm(t, "ext-2")
		got := g.say(t, tc.msg)
		if len(g.booker.resolved) != 1 || len(chat.posted) != 1 || chat.posted[0] != "task-2:"+tc.toSupporter || !strings.Contains(got, tc.toPerson) {
			t.Errorf("%q: resolved %v posted %v reply %q", tc.msg, g.booker.resolved, chat.posted, got)
		}
	}
}
