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
		len(chat.posted) != 1 || chat.posted[0] != "task-2:yes but tell him to hurry" || !strings.Contains(got, "Passed") {
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
	if len(g.booker.resolved) != 1 || len(chat.posted) != 1 || chat.posted[0] != "task-2:great, thanks Sam!" || !strings.Contains(got, "Passed") {
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
	for _, msg := range []string{"yes but tell him to hurry", "ok, thanks Alex!", "yes and grab napkins", "sure, let him know I'm home", "yes please hurry", "ok get it"} {
		if _, _, c, ok := yesWithMessage(msg); !ok || c {
			t.Errorf("%q: a harmless instruction should approve and pass on (ok=%v cond=%v)", msg, ok, c)
		}
	}
	// A strong yes beside a negation is mixed, not a decline.
	for _, msg := range []string{"no, don't worry, go ahead", "no, I don't mind", "no, I approve"} {
		if approvalNo(msg) || !mixedNo(msg) {
			t.Errorf("%q: mixed (no=%v mixed=%v)", msg, approvalNo(msg), mixedNo(msg))
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

// WhatsApp: "Great" swiped on another message is asked again, not an
// approval of the question.
func TestWhatsAppSwipedReactionIsReasked(t *testing.T) {
	g, _, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Adapters[WhatsApp] = g.ad
	g.ids.link(me, "user-1")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: WhatsApp, Address: me}, LastInboundAt: ptrTime(time.Now())}}
	g.convs.setLastInbound("id-"+me, ptrTime(time.Now()))
	g.arm(t, "ext-2")
	tasks.byMsg["status-1"] = "task-2" // a status line of ours, task-tagged
	in := Inbound{Channel: WhatsApp, ProviderMessageID: "w1", From: me, Text: "Great", ReplyToProviderID: "status-1", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 0 {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
}
