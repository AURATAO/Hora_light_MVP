package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Review 10/24 round 1 — the fixes, one test each.

func cancelRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	g.booker.open = []TaskRef{{ID: "task-9", Title: "Laundry pickup"}}
	g.booker.quote = CancelQuote{Title: "Laundry pickup", ChargeCents: 1200, BaseFeeCents: 1200, Committed: true}
	g.booker.cancelRes = CancelResult{OK: true, BillCents: 1200}
	return g
}

// #1: a cancel question lapses after CancelTTL — a later "YES" (meant for
// anything else) never cancels and charges.
func TestCancelQuestionLapses(t *testing.T) {
	g := cancelRig(t)
	g.say(t, "cancel it")
	if g.state() != StateAwaitingCancel {
		t.Fatalf("state %s", g.state())
	}
	later := time.Now().Add(CancelTTL + time.Minute)
	g.r.Now = func() time.Time { return later }
	g.say(t, "YES")
	if len(g.booker.cancelled) != 0 || g.state() == StateAwaitingCancel {
		t.Fatalf("a lapsed question was answered: cancelled %v state %s", g.booker.cancelled, g.state())
	}
}

// #1: the still-waiting line names the cancel; "cancel" again inside the
// question re-asks instead of reading as NO.
func TestCancelStillWaitingAndRepeat(t *testing.T) {
	g := cancelRig(t)
	g.say(t, "cancel it")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if w := stillWaiting(c); !strings.Contains(w, "cancel “Laundry pickup”") {
		t.Fatalf("still waiting: %q", w)
	}
	got := g.say(t, "cancel")
	if g.state() != StateAwaitingCancel || !strings.Contains(got, "reply YES on its own") {
		t.Fatalf("repeat: state %s reply %q", g.state(), got)
	}
}

// #1: a supporter's question is not held behind a cancel question — it is
// asked, the cancel is set aside (and said so), and the YES approves.
func TestApprovalInterruptsCancel(t *testing.T) {
	g := cancelRig(t)
	g.say(t, "cancel it")
	g.arm(t, "ext-b1")
	if last := g.ad.last(); !strings.Contains(last, "set aside cancelling “Laundry pickup”") {
		t.Fatalf("the question must say the cancel was set aside: %q", last)
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.PendingCancel != nil {
		t.Fatal("arming must drop the cancel question")
	}
	g.say(t, "YES")
	if len(g.booker.cancelled) != 0 || len(g.booker.resolved) != 1 {
		t.Fatalf("cancelled %v resolved %v", g.booker.cancelled, g.booker.resolved)
	}
}

// #2: a second YES that claimed the question first wins; this one does
// not cancel again.
func TestCancelClaimedOnce(t *testing.T) {
	g := cancelRig(t)
	g.say(t, "cancel it")
	calls := 0
	g.booker.onQuote = func() {
		calls++
		if calls == 1 { // the re-quote at this YES: a parallel YES claims meanwhile
			if ok, err := g.convs.ClaimCancel(context.Background(), "id-"+me, "task-9"); err != nil || !ok {
				t.Errorf("claim: %v %v", ok, err)
			}
		}
	}
	g.say(t, "YES")
	if len(g.booker.cancelled) != 0 {
		t.Fatalf("cancelled although another YES held the claim: %v", g.booker.cancelled)
	}
	if ok, _ := g.convs.ClaimCancel(context.Background(), "id-"+me, "task-9"); ok {
		t.Fatal("a claimed question must not be claimable again")
	}
}

// #9: a charge that is moving (a session running, the free window about
// to close) is sent to the app — at the question and at the YES.
func TestCancelMovingChargeGoesToTheApp(t *testing.T) {
	g := cancelRig(t)
	g.booker.quote.Moving = true
	got := g.say(t, "cancel it")
	if !strings.Contains(got, "cancel it in the app") || g.state() != StateIdle {
		t.Fatalf("question: %q state %s", got, g.state())
	}
	g = cancelRig(t)
	g.say(t, "cancel it")
	g.booker.quote.Moving = true
	got = g.say(t, "YES")
	if len(g.booker.cancelled) != 0 || !strings.Contains(got, "cancel it in the app") || g.state() != StateIdle {
		t.Fatalf("YES: cancelled %v reply %q state %s", g.booker.cancelled, got, g.state())
	}
}

// #9: a cancel whose bill could not be read never says "No charge".
func TestCancelUnknownBill(t *testing.T) {
	g := cancelRig(t)
	g.booker.cancelRes = CancelResult{OK: true, BillUnknown: true}
	g.say(t, "cancel it")
	got := g.say(t, "YES")
	if strings.Contains(got, "No charge") || !strings.Contains(got, "will show in the app") {
		t.Fatalf("%q", got)
	}
}

// MINOR: only a request to us is a cancel request.
func TestIsCancelRequestIsNarrow(t *testing.T) {
	for _, s := range []string{"cancel", "cancel it", "Can you cancel the task?", "please cancel my laundry pickup",
		"I want to cancel", "ok cancel", "取消", "幫我取消", "我要取消任務", "call off the bagel run"} {
		if !IsCancelRequest(s) {
			t.Errorf("%q should be a cancel request", s)
		}
	}
	for _, s := range []string{"don't cancel it", "no need to cancel", "I had to cancel my meeting, can you come at 5",
		"the store cancelled my order", "不要取消", "會議取消了，幫我買咖啡", "not cancel"} {
		if IsCancelRequest(s) {
			t.Errorf("%q is not a cancel request", s)
		}
	}
}

// #3: strictness is judged on the words as sent. "oh yes" with the
// supporter writing goes to the supporter (I-P); without, it approves.
func TestFillerYesIsNotStrict(t *testing.T) {
	if !approvalCasual("oh yes") || !approvalCasual("well ok") || approvalCasual("YES") {
		t.Fatal("approvalCasual")
	}
	if v := DecideApproval(ApprovalFacts{Text: "oh yes", SupporterLive: true, QuestionIsLast: true}); v != VerdictToSupporter {
		t.Fatalf("supporter live: %v", v)
	}
	if v := DecideApproval(ApprovalFacts{Text: "oh yes", QuestionIsLast: true}); v != VerdictApprove {
		t.Fatalf("no supporter: %v", v)
	}
}

// #6: the details question is never answered by a summary, and does not
// loop: after MaxDetailsTries answers that still name no place, a person.
func TestDetailsGateEndsInAPerson(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	ops := &memOps{}
	g.r.Ops = ops
	g.parser.draft = llm.TaskDraft{Title: "Something", Category: "other", DurationMinutes: 30}
	g.say(t, "book another task")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if err := g.r.showSummary(context.Background(), Inbound{Channel: IMessage, From: me}, c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(g.ad.last(), "YES") {
		t.Fatalf("a summary was shown for a draft with no place: %q", g.ad.last())
	}
	g.say(t, "something nice")
	if g.state() != StateAwaitingConfirm || len(ops.notices) != 0 {
		t.Fatalf("first answer: state %s notices %d", g.state(), len(ops.notices))
	}
	g.say(t, "you know, the usual")
	if g.state() != StateHandoff || len(ops.notices) != 1 {
		t.Fatalf("second answer: state %s notices %d", g.state(), len(ops.notices))
	}
}

// #5: a weak errand reading ("pick up …") with a supporter in the
// conversation is not decided by rule — the usual routing (the model)
// decides. "book another task" still is.
func TestWeakErrandDefersToTheModel(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: on my way", "task-2", time.Now().Add(-30*time.Minute)
	g.say(t, "pick up the kids' bagels too")
	if len(chat.posted) != 1 {
		t.Fatalf("weak errand with a supporter talking: the model routes it: posted %v", chat.posted)
	}
	in := Inbound{Channel: IMessage, From: me, ReceivedAt: time.Now()}
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
	if !g.r.errandForDraft(context.Background(), in, id, "book another task: coffee at 5th ave") {
		t.Fatal("a new errand said outright is still ours")
	}
}

// Review 10/24 r2 MAJOR: a supporter's question armed while the cancel was
// being quoted is the open question — the cancel question is not sent on
// top of it, and the YES that follows answers the supporter's, knowingly.
func TestCancelQuestionNotSentOverAnArmedApproval(t *testing.T) {
	g := cancelRig(t)
	g.booker.onQuote = func() {
		g.booker.onQuote = nil
		g.arm(t, "ext-b1")
	}
	got := g.say(t, "cancel it")
	if g.state() != StateAwaitingApproval || !strings.Contains(got, "answer that first") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
	for _, o := range g.ad.sent {
		if strings.Contains(o.Text, "Reply YES to cancel") {
			t.Fatalf("a cancel question went out over the armed approval: %q", o.Text)
		}
	}
}

// The same for a draft: a summary is not sent over an approval armed
// while the draft was priced; the draft waits and is told so.
func TestSummaryNotSentOverAnArmedApproval(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.onPrice = func() {
		g.booker.onPrice = nil
		g.arm(t, "ext-b1")
	}
	got := g.say(t, "pick up my laundry at 12 Bedford Ave")
	if g.state() != StateAwaitingApproval || !strings.Contains(got, "right after you answer") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
	for _, o := range g.ad.sent {
		if strings.Contains(o.Text, "Reply YES to book") {
			t.Fatalf("a summary went out over the armed approval: %q", o.Text)
		}
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.Pending == nil {
		t.Fatal("the draft must be kept for after the answer")
	}
}

// Review 10/24 r2 MINOR: a supporter's question delivered after the cancel
// question makes a YES ambiguous — asked again, not cancelled.
func TestCancelYesAfterASupporterQuestionIsReasked(t *testing.T) {
	g, _, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.booker.open = []TaskRef{{ID: "task-9", Title: "Laundry pickup"}}
	g.booker.quote = CancelQuote{Title: "Laundry pickup", ChargeCents: 1200, BaseFeeCents: 1200, Committed: true}
	g.booker.cancelRes = CancelResult{OK: true, BillCents: 1200}
	g.say(t, "cancel it")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: are you home?", "task-2", time.Now().Add(time.Second)
	got := g.say(t, "YES")
	if len(g.booker.cancelled) != 0 || !strings.Contains(got, "Cancel “Laundry pickup”") {
		t.Fatalf("cancelled %v reply %q", g.booker.cancelled, got)
	}
}

// A swiped WhatsApp "YES" (on some message — the cancel question's own id
// is not kept) is asked again; the fee needs it typed on its own.
func TestCancelSwipedYesOnWhatsAppIsReasked(t *testing.T) {
	g := cancelRig(t)
	g.r.Adapters[WhatsApp] = g.ad
	g.say(t, "cancel it")
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	id := Identity{ID: "id-" + me, Channel: WhatsApp, Address: me, UserID: "user-1"}
	in := Inbound{Channel: WhatsApp, From: me, Text: "YES", ReceivedAt: time.Now().Add(time.Second), ReplyToProviderID: "wamid.other"}
	if err := g.r.onAwaitingCancel(context.Background(), in, id, c, "YES"); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.cancelled) != 0 || !strings.Contains(g.ad.last(), "reply YES on its own") {
		t.Fatalf("cancelled %v reply %q", g.booker.cancelled, g.ad.last())
	}
}

// A task already past cancelling is said as such (not "try again").
func TestCancelNotOpenIsGone(t *testing.T) {
	g := cancelRig(t)
	g.say(t, "cancel it")
	g.booker.quoteErr = ErrTaskNotOpen
	got := g.say(t, "YES")
	if !strings.Contains(got, "can't be cancelled any more") || len(g.booker.cancelled) != 0 {
		t.Fatalf("%q", got)
	}
}

// r3: the choice list is not sent over an approval armed while the open
// tasks were looked up either.
func TestCancelListNotSentOverAnArmedApproval(t *testing.T) {
	g := cancelRig(t)
	g.booker.open = []TaskRef{{ID: "a", Title: "Bagel run"}, {ID: "b", Title: "Laundry pickup"}}
	g.booker.onOpen = func() {
		g.booker.onOpen = nil
		g.arm(t, "ext-b1")
	}
	got := g.say(t, "cancel")
	if g.state() != StateAwaitingApproval || !strings.Contains(got, "answer that first") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
	for _, o := range g.ad.sent {
		if strings.Contains(o.Text, "Reply with the number") {
			t.Fatalf("the list went out over the armed approval: %q", o.Text)
		}
	}
}

// Arbiter 2026-10-24 22:39: a hand-off ops never pick up goes back to the
// bot after HandoffTimeout, and the person is told; an ops message restarts
// the clock; the person's next message after the timeout is the bot's.
func TestHandoffTimesOut(t *testing.T) {
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	g.r.Ops = &memOps{}
	g.say(t, "real person")
	if g.state() != StateHandoff {
		t.Fatalf("state %s", g.state())
	}
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
	if ok, err := g.r.AutoRelease(context.Background(), id, false); err != nil || ok {
		t.Fatalf("fresh hand-off released: %v %v", ok, err)
	}
	// Handed off 20 minutes ago; ops write now: not stale.
	g.convs.mu.Lock()
	c := g.convs.convs["id-"+me]
	old := time.Now().Add(-2 * HandoffTimeout)
	c.HandoffActivityAt = &old
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	if err := g.r.OpsSend(context.Background(), id, "Hi, Aura here"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := g.r.AutoRelease(context.Background(), id, false); ok {
		t.Fatal("an ops message must restart the clock")
	}
	g.r.Now = func() time.Time { return time.Now().Add(2*HandoffTimeout + time.Minute) }
	if ok, err := g.r.AutoRelease(context.Background(), id, false); err != nil || !ok {
		t.Fatalf("stale hand-off kept: %v %v", ok, err)
	}
	if g.state() != StateIdle || !strings.Contains(g.ad.last(), "no one from the HO:RA team has replied") {
		t.Fatalf("state %s reply %q", g.state(), g.ad.last())
	}

	// Lazily, on the person's next message: released, then the message is
	// the bot's (here: drafted).
	g = newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Ops = &memOps{}
	g.say(t, "real person")
	g.r.Now = func() time.Time { return time.Now().Add(HandoffTimeout + time.Minute) }
	got := g.say(t, "pick up my laundry at 12 Bedford Ave")
	if g.state() == StateHandoff || !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("state %s reply %q", g.state(), got)
	}
}

// Hand-off review r1: a thread with no clock is never released at once; a
// release with a live supporter question does not also put our summary
// (two YES-able questions); ops are told; the lazy path re-reads after a
// lost race; "real person" after the timeout re-notifies instead of
// "I'm back" + "teammate coming".
func TestHandoffTimeoutEdges(t *testing.T) {
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
	// No clock: not stale.
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	g.r.Ops = &memOps{}
	g.say(t, "real person")
	g.convs.mu.Lock()
	c := g.convs.convs["id-"+me]
	c.HandoffActivityAt = nil
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	g.r.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	if ok, _ := g.r.AutoRelease(context.Background(), id, false); ok {
		t.Fatal("a thread with no clock was released")
	}

	// Ops told on auto-release.
	g = newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	ops := &memOps{}
	g.r.Ops = ops
	g.say(t, "real person")
	g.r.Now = func() time.Time { return time.Now().Add(HandoffTimeout + time.Minute) }
	if ok, err := g.r.AutoRelease(context.Background(), id, false); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if n := ops.notices[len(ops.notices)-1]; !n.AutoReleased {
		t.Fatalf("ops not told: %+v", n)
	}

	// "real person" after the timeout: ops told again, clock restarts.
	g = newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	ops = &memOps{}
	g.r.Ops = ops
	g.say(t, "real person")
	g.r.Now = func() time.Time { return time.Now().Add(HandoffTimeout + time.Minute) }
	got := g.say(t, "real person please")
	if g.state() != StateHandoff || len(ops.notices) != 2 || strings.Contains(got, "back to help") {
		t.Fatalf("state %s notices %d reply %q", g.state(), len(ops.notices), got)
	}

	// Ops released it a moment ago (the lazy path loses the race): the
	// message is the bot's, not "a teammate will get back to you".
	g = newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Ops = &memOps{}
	g.say(t, "real person")
	if err := g.r.Release(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	got = g.say(t, "pick up my laundry at 12 Bedford Ave")
	if strings.Contains(got, "will get back to you") || !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("reply %q", got)
	}
}

// A supporter's question held during the hand-off is asked on release; our
// own pending summary is NOT put in the same burst.
func TestReleaseDoesNotStackQuestions(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Ops = &memOps{}
	g.say(t, "pick up my laundry at 12 Bedford Ave") // a summary waits
	g.say(t, "real person")
	if g.state() != StateHandoff {
		t.Fatalf("state %s", g.state())
	}
	// A supporter's question arrives during the hand-off: held.
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-b1", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-b1", Kind: "budget", Cents: 1000}}); err != nil {
		t.Fatal(err)
	}
	if g.state() != StateHandoff {
		t.Fatalf("held during hand-off: %s", g.state())
	}
	if err := g.r.Release(context.Background(), Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	for _, o := range g.ad.sent {
		if strings.Contains(o.Text, "back with me") && strings.Contains(o.Text, "Reply YES to book") {
			t.Fatalf("the summary was put together with the supporter's question: %q", o.Text)
		}
	}
	if g.state() != StateAwaitingApproval {
		t.Fatalf("the supporter's question was not asked: %s", g.state())
	}
}

// Hand-off review r2: a release that holds our question back for the
// supporter's still marks the thread's question in flight (R-1) — a YES
// right after cannot book the old draft; a budget carries only when the
// kind is unambiguous; spelled-out durations count.
func TestReleaseMarksQuestionInFlight(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Ops = &memOps{}
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	g.say(t, "real person")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me}}}
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{Type: evBudgetRequested, TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam",
		ExtensionID: "ext-b1", ApprovalMinutes: 5, Approval: &PendingApproval{ID: "ext-b1", Kind: "budget", Cents: 1000}}); err != nil {
		t.Fatal(err)
	}
	// The hold is live at the release's check but gone when resumeDeferred
	// runs (it expired): no question is put, yet the draft's question is
	// in flight.
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
	if err := g.r.Release(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	if c.State == StateAwaitingConfirm && (c.QuestionShownAt == nil || !c.QuestionShownAt.Equal(ShownUnconfirmed)) {
		t.Fatalf("question not marked in flight: %+v", c.QuestionShownAt)
	}
	if err := g.r.Release(context.Background(), id); !errors.Is(err, ErrNotInHandoff) {
		t.Fatalf("a second release must say the bot has it: %v", err)
	}
	for _, s := range []string{"two hours", "a couple of hours", "1h", "an hour"} {
		if !namesDuration(s) {
			t.Errorf("%q names a duration", s)
		}
	}
}
