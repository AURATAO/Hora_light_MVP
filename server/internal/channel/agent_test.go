package channel

import (
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// fakeAgent returns a fixed proposal and records what it was shown.
type fakeAgent struct {
	p     llm.Proposal
	raw   string // when set: the model's JSON, decoded by the production DecodeProposal
	err   error
	calls []llm.AgentContext
	texts []string
	// during runs inside the call — a message that arrives while the model
	// is thinking.
	during func()
}

func (f *fakeAgent) Propose(_ context.Context, text string, c llm.AgentContext) (llm.Proposal, error) {
	f.calls, f.texts = append(f.calls, c), append(f.texts, text)
	if d := f.during; d != nil {
		f.during = nil
		d()
	}
	if f.err != nil {
		return llm.SafeProposal(), f.err
	}
	if f.raw != "" {
		return llm.DecodeProposal(f.raw, c)
	}
	return f.p, nil
}

func agentRig(p llm.Proposal, active ...ActiveTask) (*rig, *fakeChat, *fakeTasks, *fakeAgent) {
	g, chat, tasks, _ := contextRig(llm.IntentNewTask, active...) // the old classifier would draft: it must not be asked
	fa := &fakeAgent{p: p}
	g.r.Agent = fa
	return g, chat, tasks, fa
}

// D-18: an agent "approve" or "decline" on a sentence never resolves the
// request — the router puts its plain YES/NO question again.
func TestAgentCannotResolveARequest(t *testing.T) {
	for _, a := range []llm.Action{llm.ActionApprove, llm.ActionDecline} {
		g, chat, _, fa := agentRig(llm.Proposal{Action: a, Task: 0}, bagelT)
		g.arm(t, "ext-2")
		g.say(t, "honestly whatever he thinks is right, he's the expert")
		if len(g.booker.resolved) != 0 || len(chat.posted) != 0 || g.state() != StateAwaitingApproval || len(fa.calls) != 1 {
			t.Fatalf("%s: resolved %v posted %v state %s calls %d", a, g.booker.resolved, chat.posted, g.state(), len(fa.calls))
		}
	}
}

// D-18: the rules run before the agent — a plain yes approves and a plain
// no declines whatever the agent would say (it is not even asked).
func TestRulesRunBeforeTheAgent(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionDecline, Task: -1}, bagelT)
	g.arm(t, "ext-2")
	g.say(t, "yes")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":true") || len(fa.calls) != 0 {
		t.Fatalf("resolved %v calls %d", g.booker.resolved, len(fa.calls))
	}
	g, _, _, fa = agentRig(llm.Proposal{Action: llm.ActionApprove, Task: -1}, bagelT)
	g.arm(t, "ext-2")
	g.say(t, "no")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":false") || len(fa.calls) != 0 {
		t.Fatalf("resolved %v calls %d", g.booker.resolved, len(fa.calls))
	}
}

// D-18: a booking is never made by the agent — the model's "approve" (as
// production decodes it) on a sentence at the summary gets the plain-YES
// re-ask; nothing is booked.
func TestAgentCannotBook(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{}, bagelT)
	g.r.Agent = nil
	g.say(t, "new task: pick up my laundry tomorrow at 3") // drafted without the agent
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("no draft: state %s", g.state())
	}
	g.r.Agent = fa
	fa.raw = `{"action":"approve","task":"","escalation":""}`
	got := g.say(t, "that all sounds wonderful to me")
	if len(fa.calls) != 1 || fa.calls[0].OpenQuestion != "booking" {
		t.Fatalf("the agent must be asked: %+v", fa.calls)
	}
	if g.booker.posts != 0 || g.state() != StateAwaitingConfirm || got != CopyConfirmReask {
		t.Fatalf("posts %d state %s reply %q", g.booker.posts, g.state(), got)
	}
}

// Review r1 MAJOR: a YES that books while the model is thinking about an
// escalation is not undone — the hand-off saves the conversation as it is
// after the call, never the draft from before it.
func TestAgentEscalationAfterABookingKeepsTheBooking(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateDispute}, bagelT)
	g.r.Ops = &memOps{}
	g.r.Agent = nil
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	g.r.Agent = fa
	fa.during = func() {
		in := Inbound{Channel: IMessage, ProviderMessageID: "yes-during", From: me, Text: "YES", ReceivedAt: time.Now()}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Error(err)
		}
	}
	g.say(t, "he broke my vase last time and I want a refund")
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if g.booker.posts != 1 || c.State != StateHandoff || c.Pending != nil {
		t.Fatalf("posts %d state %s pending %v", g.booker.posts, c.State, c.Pending != nil)
	}
}

// Review r1: a hand-off with an approval armed tells ops the request is
// still pending in the app.
func TestAgentEscalationTellsOpsOfTheOpenRequest(t *testing.T) {
	g, chat, _, _ := agentRig(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}, bagelT)
	ops := &memOps{}
	g.r.Ops = ops
	g.arm(t, "ext-2")
	g.say(t, "I don't feel safe, he keeps asking me for more")
	if g.state() != StateHandoff || len(ops.notices) != 1 || ops.notices[0].OpenRequestID != "ext-2" || ops.notices[0].OpenRequestTaskID != "task-2" {
		t.Fatalf("state %s notices %+v", g.state(), ops.notices)
	}
	if len(g.booker.resolved) != 0 || len(chat.posted) != 0 {
		t.Fatalf("resolved %v posted %v", g.booker.resolved, chat.posted)
	}
}

// Review r1: mid-choice (a held relay), an escalation hands off; the
// words are not joined to the hold and sent to a supporter.
func TestAgentEscalationMidChoice(t *testing.T) {
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionAsk, Task: -1}, laundryT, bagelT)
	ops := &memOps{}
	g.r.Ops = ops
	g.say(t, "can you hurry up a bit")
	if g.state() != StateAwaitingTaskChoice {
		t.Fatalf("state %s", g.state())
	}
	fa.p = llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}
	g.say(t, "he grabbed my arm")
	if g.state() != StateHandoff || len(chat.posted) != 0 || len(ops.notices) != 1 {
		t.Fatalf("state %s posted %v notices %d", g.state(), chat.posted, len(ops.notices))
	}
}

// A-7: the agent's log lines carry the action and the reason, never the
// words, the supporter's line or a title.
func TestAgentLogsNoWords(t *testing.T) {
	var buf strings.Builder // the rig runs synchronously; no t.Parallel in this package
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	g, _, tasks, fa := agentRig(llm.Proposal{Action: llm.ActionRelay, Task: 0}, bagelT)
	g.r.Ops = &memOps{}
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: secret-supporter-line?", "task-2", time.Now().Add(-15*time.Minute)
	g.say(t, "secret-words-one")
	fa.raw = `{"action":"nonsense-from-model","task":"","escalation":""}`
	g.say(t, "secret-words-two")
	fa.raw, fa.p = "", llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateDispute}
	g.say(t, "secret-words-three")
	out := buf.String()
	for _, bad := range []string{"secret-words", "secret-supporter-line", "Bagel run", "nonsense-from-model"} {
		if strings.Contains(out, bad) {
			t.Fatalf("log carries %q:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "action=relay") || !strings.Contains(out, "reason=dispute") {
		t.Fatalf("log lacks the action / reason:\n%s", out)
	}
}

// I-AK edges, on the pure rule function.
func TestNoProblemNoWorriesEdges(t *testing.T) {
	for _, tc := range []struct {
		facts ApprovalFacts
		want  ApprovalVerdict
	}{
		{ApprovalFacts{Text: "no problem, go ahead", QuestionIsLast: true}, VerdictApprove},
		{ApprovalFacts{Text: "no worries, but max $5", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "nope, no worries", QuestionIsLast: true}, VerdictPassOnKeep},
		{ApprovalFacts{Text: "no worries", QuestionIsLast: true, SwipedElsewhere: true}, VerdictReask},
		{ApprovalFacts{Text: "no problem", QuestionIsLast: true, SupporterLive: true}, VerdictToSupporter},
		{ApprovalFacts{Text: "no problem", QuestionIsLast: true, Unconfirmed: true}, VerdictUnconfirmed},
		{ApprovalFacts{Text: "no worries", QuestionIsLast: true, SentBefore: true}, VerdictSentBefore},
		{ApprovalFacts{Text: "no worries", QuestionIsLast: false}, VerdictReask},
	} {
		if got := DecideApproval(tc.facts); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.facts, got, tc.want)
		}
	}
}

// The agent names the errand: the words go there even though the other
// supporter wrote last; a supporter named in the words still wins.
func TestAgentTargetsAnErrand(t *testing.T) {
	g, chat, tasks, fa := agentRig(llm.Proposal{Action: llm.ActionRelay, Task: 1}, laundryT, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt, tasks.lastAnswered = "Laundry: done soon", "task-1", time.Now().Add(-20*time.Minute), true
	g.say(t, "can you get sesame instead")
	if len(chat.posted) != 1 || !strings.HasPrefix(chat.posted[0], "task-2:") {
		t.Fatalf("posted %v", chat.posted)
	}
	if len(fa.calls) != 1 || len(fa.calls[0].Tasks) != 2 || fa.calls[0].Tasks[1].Title != "Bagel run" {
		t.Fatalf("context %+v", fa.calls)
	}
	fa.p = llm.Proposal{Action: llm.ActionRelay, Task: 1}
	g.say(t, "can Maya fold the towels too")
	if len(chat.posted) != 2 || !strings.HasPrefix(chat.posted[1], "task-1:") {
		t.Fatalf("a named supporter must win: posted %v", chat.posted)
	}
}

// "ask": with two errands and no named supporter, the list is shown even
// though one supporter wrote last.
func TestAgentAskSkipsTheGuess(t *testing.T) {
	g, chat, tasks, _ := agentRig(llm.Proposal{Action: llm.ActionAsk, Task: -1}, laundryT, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt, tasks.lastAnswered = "Bagel: here", "task-2", time.Now().Add(-30*time.Minute), true
	g.say(t, "can you hurry up a bit")
	if len(chat.posted) != 0 || g.state() != StateAwaitingTaskChoice {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
}

// "escalate" hands the thread to ops; the reason is logged, not the words.
func TestAgentEscalates(t *testing.T) {
	g, chat, _, _ := agentRig(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateDispute}, bagelT)
	ops := &memOps{}
	g.r.Ops = ops
	g.say(t, "he broke my vase and I want my money back")
	if g.state() != StateHandoff || len(ops.notices) != 1 || ops.notices[0].Reason != HandoffAgentEscalated || len(chat.posted) != 0 {
		t.Fatalf("state %s notices %+v posted %v", g.state(), ops.notices, chat.posted)
	}
}

// A model failure is the safe proposal: for the supporter, by the old
// target rules.
func TestAgentFailureRelays(t *testing.T) {
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionDraftTask, Task: -1}, bagelT)
	fa.err = errors.New("baseten 503")
	g.say(t, "the doorman's name is Joe")
	if len(chat.posted) != 1 || g.booker.posts != 0 || g.state() != StateIdle {
		t.Fatalf("posted %v state %s", chat.posted, g.state())
	}
}

// "draft_task" starts the usual draft → summary → YES flow; nothing is
// booked until the YES.
func TestAgentDraftsANewErrand(t *testing.T) {
	g, chat, _, _ := agentRig(llm.Proposal{Action: llm.ActionDraftTask, Task: -1}, bagelT)
	g.say(t, "also can someone walk my dog tomorrow at 9am in Chelsea")
	if len(chat.posted) != 0 || g.booker.posts != 0 {
		t.Fatalf("posted %v posts %d", chat.posted, g.booker.posts)
	}
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if c.Pending == nil {
		t.Fatalf("no draft: state %s last %q", c.State, g.ad.last())
	}
}

// The context shown to the agent carries no ids and no phone number.
func TestAgentContextHasNoIdentifiers(t *testing.T) {
	g, _, tasks, fa := agentRig(llm.Proposal{Action: llm.ActionRelay, Task: 0}, bagelT)
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: which one?", "task-2", time.Now().Add(-15*time.Minute)
	g.say(t, "the one with seeds")
	msg := llm.AgentUserMessage(fa.texts[0], fa.calls[0])
	for _, bad := range []string{"task-2", me, "user-1", "id-"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("context leaks %q:\n%s", bad, msg)
		}
	}
	if fa.calls[0].SupporterLine == nil || fa.calls[0].SupporterLine.Task != 0 {
		t.Fatalf("line %+v", fa.calls[0].SupporterLine)
	}
}

// Review r2: an escalation while the booking is posting does not take the
// thread over, but ops ARE told (with the reason); while already handed
// off it is the holding line.
func TestAgentEscalationWhilePostingOrHandedOff(t *testing.T) {
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}, bagelT)
	ops := &memOps{}
	g.r.Ops = ops
	fa.during = func() {
		c, err := g.convs.GetConversation(context.Background(), "id-"+me)
		if err != nil {
			t.Error(err)
			return
		}
		c.State = StatePosting
		g.convs.mu.Lock()
		g.convs.convs["id-"+me] = c
		g.convs.mu.Unlock()
	}
	got := g.say(t, "he grabbed my arm")
	if got != CopyBookingInFlight || g.state() != StatePosting || len(ops.notices) != 1 || ops.notices[0].Detail != "safety" || len(chat.posted) != 0 {
		t.Fatalf("reply %q state %s notices %+v posted %v", got, g.state(), ops.notices, chat.posted)
	}
	g, _, _, fa = agentRig(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateDispute}, bagelT)
	ops = &memOps{}
	g.r.Ops = ops
	fa.during = func() {
		c, err := g.convs.GetConversation(context.Background(), "id-"+me)
		if err != nil {
			t.Error(err)
			return
		}
		c.State = StateHandoff
		g.convs.mu.Lock()
		g.convs.convs["id-"+me] = c
		g.convs.mu.Unlock()
	}
	got = g.say(t, "I want a refund")
	if got != CopyHandoffHolding || g.state() != StateHandoff || len(ops.notices) != 1 || !ops.notices[0].Update {
		t.Fatalf("reply %q state %s notices %+v", got, g.state(), ops.notices)
	}
}

// Review r2: a hand-off over an unconfirmed claim says so — the hold
// survives and the question is asked again after release.
func TestAgentEscalationOverAnUnconfirmedClaim(t *testing.T) {
	g, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}, bagelT)
	ops := &memOps{}
	g.r.Ops = ops
	g.arm(t, "ext-2")
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	c.ApprovalArmedAt = &ArmedUnconfirmed
	g.convs.mu.Lock()
	g.convs.convs["id-"+me] = c
	g.convs.mu.Unlock()
	if !c.Unconfirmed() {
		t.Fatal("fixture: claim must read as unconfirmed")
	}
	if err := g.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: "esc", From: me, Text: "this is getting scary honestly", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if len(ops.notices) != 1 || ops.notices[0].OpenRequestID != "ext-2" || !ops.notices[0].OpenRequestUnconfirmed {
		t.Fatalf("notices %+v", ops.notices)
	}
}

// The runner's SAME grade rests on this: with one errand active, the
// agent's relay (with or without the label) and ask reach that errand's
// supporter — idle, at the summary and with an approval armed. (A reply is
// answered by HO:RA itself since v0.4.0 — TestAgentReplyIsAnsweredNotRelayed.)
func TestOneErrandRelayReplyAskAgree(t *testing.T) {
	props := []llm.Proposal{
		{Action: llm.ActionRelay, Task: 0}, {Action: llm.ActionRelay, Task: -1},
		{Action: llm.ActionAsk, Task: -1},
	}
	type result struct {
		reply, posted string
		state         State
		pending       bool
		posts         int
		resolved      int
	}
	for _, state := range []string{"idle", "confirm", "approval"} {
		var first *result
		for _, p := range props {
			g, chat, _, fa := agentRig(p, bagelT)
			switch state {
			case "confirm":
				g.r.Agent = nil
				g.say(t, "new task: pick up my laundry tomorrow at 3")
				g.r.Agent = fa
			case "approval":
				g.arm(t, "ext-2")
			}
			reply := g.say(t, "the doorman's name is Joe")
			c, err := g.convs.GetConversation(context.Background(), "id-"+me)
			if err != nil {
				t.Fatal(err)
			}
			if len(fa.calls) != 1 || len(chat.posted) != 1 {
				t.Fatalf("%s / %s: calls %d posted %v", state, p.Action, len(fa.calls), chat.posted)
			}
			got := result{reply, chat.posted[0], c.State, c.Pending != nil, g.booker.posts, len(g.booker.resolved)}
			if !strings.HasPrefix(got.posted, "task-2:") || got.posts != 0 || got.resolved != 0 {
				t.Errorf("%s / %s: %+v", state, p.Action, got)
			}
			if first == nil {
				first = &got
			} else if got != *first {
				t.Errorf("%s / %s: %+v differs from %+v", state, p.Action, got, *first)
			}
		}
	}
}

// Staging 2026-10-25: "Ok never mind" after "please cancel it in the app"
// was relayed to the supporter. A "reply" proposal is HO:RA's to answer:
// nothing is posted to the supporter, and the agent saw our last line.
func TestAgentReplyIsAnsweredNotRelayed(t *testing.T) {
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionReply, Task: -1}, bagelT)
	g.r.Thread = fixedThread{{Direction: "outbound", Text: "Please cancel it in the app, where you'll see the exact amount first.", At: time.Now().Add(-time.Minute)}}
	got := g.say(t, "Ok never mind")
	if len(chat.posted) != 0 || got != CopyAck {
		t.Fatalf("posted %v reply %q", chat.posted, got)
	}
	if len(fa.calls) != 1 || fa.calls[0].HoraLine == nil || !strings.Contains(fa.calls[0].HoraLine.Text, "cancel it in the app") {
		t.Fatalf("the agent must see our last line: %+v", fa.calls)
	}
}

// Arbiter 2026-10-30: a whereabouts question in any wording is answered
// from the live tracking. Staging that morning: "can you tell me where aura
// is" — lowercase name, indirect question — missed the rule and the agent
// relayed it to the supporter. Now the agent may label it "where"; the
// answer is still the tracking's, never the model's. A message that is
// nothing but the question is answered alone; anything else the model
// called "where" is answered AND relayed, so no words are lost (P7).
func TestAgentWhereIsAnsweredFromLiveTracking(t *testing.T) {
	aura := ActiveTask{ID: "task-2", Title: "Bagel run", SupporterName: "Aura"}
	bea := ActiveTask{ID: "task-3", Title: "Laundry pickup", SupporterName: "Bea"}
	will := ActiveTask{ID: "task-1", Title: "Grocery run", SupporterName: "Will"}
	m := 1300.0
	onTheWay := LiveStatus{State: LiveOnTheWay, DistanceMeters: &m, LastSeen: time.Now().Add(-2 * time.Minute)}
	atDoor := LiveStatus{State: LiveAtDoor}

	for _, tc := range []struct {
		name    string
		active  []ActiveTask
		task    int
		live    LiveStatus
		msg     string
		relayed string // the task the words go to; "" = answered alone
		want    []string
		absent  []string
	}{
		{"staging incident", []ActiveTask{aura}, 0, onTheWay, "can you tell me where aura is", "",
			[]string{"Aura is on the way — about 0.8 mi away, last seen 2 min ago."}, nil},
		{"greeting, lowercase name", []ActiveTask{aura}, 0, atDoor, "hey, is aura close", "",
			[]string{"Aura is at the door."}, nil},
		{"label picks, more than the question", []ActiveTask{aura, bea}, 1, atDoor, "has she made it yet", "task-3",
			[]string{"Bea is at the door."}, []string{"Aura"}},
		{"lowercase name beats the label", []ActiveTask{aura, bea}, 0, atDoor, "any idea if bea is close", "task-3",
			[]string{"Bea is at the door."}, []string{"Aura"}},
		{"two names: both", []ActiveTask{aura, bea}, 0, atDoor, "who is closer aura or bea", "task-2",
			[]string{"Aura is at the door.", "Bea is at the door."}, nil},
		{"a name that is a word, no label: everyone, then which one", []ActiveTask{will, aura}, -1, atDoor, "Will she be here soon", "",
			[]string{"Will is at the door.", "Aura is at the door."}, nil}, // review r5 #3: the status first, then the list
		{"a name that is a word: the words follow the answer", []ActiveTask{will, aura}, 1, atDoor, "Will she be here soon, tell her to ring", "task-2",
			[]string{"Aura is at the door."}, []string{"Will is"}}, // review r5 #2: not to Will
		{"silent phone, more than the question", []ActiveTask{aura}, 0, LiveStatus{State: LiveUnavailable, LastSeen: time.Now().Add(-7 * time.Minute)}, "has she made it yet", "task-2",
			[]string{"Aura's phone hasn't reported a location for a bit, last seen 7 min ago"}, []string{"I've asked"}}, // review r5 #4
		{"chinese the rule does not know", []ActiveTask{aura}, -1, atDoor, "她到底到哪了", "task-2",
			[]string{"Aura is at the door."}, nil},
		{"instruction rides along", []ActiveTask{aura}, 0, onTheWay, "is aura close get here now", "task-2",
			[]string{"Aura is on the way"}, nil},
		{"news rides along", []ActiveTask{aura}, 0, onTheWay, "will be late where is she", "task-2",
			[]string{"Aura is on the way"}, nil},
		{"chinese news rides along", []ActiveTask{aura}, 0, onTheWay, "我走了 她到哪了", "task-2",
			[]string{"Aura is on the way"}, nil},
		{"code rides along", []ActiveTask{aura}, 0, onTheWay, "can you tell me where aura is, the code is 4521", "task-2",
			[]string{"Aura is on the way"}, nil},
	} {
		if IsWhereQuestion(tc.msg) {
			t.Fatalf("%s: %q matches the rule — the case no longer covers the agent's path", tc.name, tc.msg)
		}
		g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionWhere, Task: tc.task}, tc.active...)
		g.r.Live = &fakeLive{st: tc.live}
		sent := len(g.ad.sent)
		g.say(t, tc.msg)
		var replies []string
		for _, o := range g.ad.sent[sent:] {
			replies = append(replies, o.Text)
		}
		got := strings.Join(replies, "\n")
		if len(fa.calls) != 1 {
			t.Errorf("%s: model calls %d", tc.name, len(fa.calls))
			continue
		}
		if tc.relayed == "" && len(chat.posted) != 0 {
			t.Errorf("%s: nothing but the question must not be relayed: %v", tc.name, chat.posted)
		}
		if tc.relayed != "" && (len(chat.posted) != 1 || chat.posted[0] != tc.relayed+":"+tc.msg) {
			t.Errorf("%s: the words must reach %s whole: %v", tc.name, tc.relayed, chat.posted)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: replies %q lack %q", tc.name, got, w)
			}
		}
		for _, a := range tc.absent {
			if strings.Contains(got, a) {
				t.Errorf("%s: replies %q name %q", tc.name, got, a)
			}
		}
	}

	// The tracking cannot answer: the words reach the supporter, as before.
	for _, tc := range []struct {
		name string
		live LiveLookup
		msg  string
	}{
		{"no tracking, pure", nil, "can you tell me where aura is"},
		{"lookup failed, pure", &fakeLive{err: errors.New("db")}, "can you tell me where aura is"},
		{"no tracking, more", nil, "is aura close get here now"},
		{"lookup failed, more", &fakeLive{err: errors.New("db")}, "is aura close get here now"},
	} {
		g, chat, _, _ := agentRig(llm.Proposal{Action: llm.ActionWhere, Task: 0}, aura)
		if tc.live != nil {
			g.r.Live = tc.live
		}
		g.say(t, tc.msg)
		if len(chat.posted) != 1 {
			t.Errorf("%s: must reach the supporter: posted %v", tc.name, chat.posted)
		}
	}

	// A cancel is the cancel rule's, before any model.
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionWhere, Task: 0}, aura)
	g.r.Live = &fakeLive{st: onTheWay}
	if got := g.say(t, "where is she? cancel it"); len(fa.calls) != 0 || len(chat.posted) != 0 || strings.Contains(got, "on the way") {
		t.Fatalf("cancel: model calls %d posted %v reply %q", len(fa.calls), chat.posted, got)
	}

	// A relay stays a relay: only the model's "where" takes the new path.
	g, chat, _, _ = agentRig(llm.Proposal{Action: llm.ActionRelay, Task: 0}, aura)
	g.r.Live = &fakeLive{st: onTheWay}
	if got := g.say(t, "can you tell me where aura is"); len(chat.posted) != 1 || strings.Contains(got, "on the way") {
		t.Fatalf("relay: posted %v reply %q", chat.posted, got)
	}
}

// Review r1 #6: a "where" while an approval question is open answers the
// whereabouts, leaves the question armed and says it still waits; the next
// plain YES approves as it always did.
func TestAgentWhereKeepsAnOpenApproval(t *testing.T) {
	const msg = "can you tell me where sam is"
	if IsWhereQuestion(msg) {
		t.Fatalf("%q matches the rule — the test no longer covers the agent's path", msg)
	}
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionWhere, Task: 0}, bagelT)
	g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
	g.arm(t, "ext-2")
	before, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	sent := len(g.ad.sent)
	g.say(t, msg)
	var replies []string
	for _, o := range g.ad.sent[sent:] {
		replies = append(replies, o.Text)
	}
	all := strings.Join(replies, "\n")
	if len(fa.calls) != 1 || len(chat.posted) != 0 || len(g.booker.resolved) != 0 {
		t.Fatalf("model calls %d posted %v resolved %v", len(fa.calls), chat.posted, g.booker.resolved)
	}
	if !strings.Contains(all, "Sam is at the door.") || len(replies) != 2 {
		t.Fatalf("want the location, then the open question again: %q", replies)
	}
	after, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != StateAwaitingApproval || after.PendingExtensionID != before.PendingExtensionID {
		t.Fatalf("the question must stay armed: %s %q → %s %q", before.State, before.PendingExtensionID, after.State, after.PendingExtensionID)
	}
	g.say(t, "YES")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":true") {
		t.Fatalf("a plain YES after the answer must approve: %v", g.booker.resolved)
	}
}

// Arbiter 2026-10-30 (was review r1 #3, pinned the other way): when the
// supporter has just asked something and the person has not answered, a
// short message used to be relayed before any model (step 7a). Now the
// agent is asked first: "where" is answered from the tracking, "escalate"
// hands off; anything else is still the supporter's answer.
func TestWhereAfterASupporterQuestionIsHORAs(t *testing.T) {
	aura := ActiveTask{ID: "task-2", Title: "Bagel run", SupporterName: "Aura"}
	open := func(p llm.Proposal) (*rig, *fakeChat, *fakeAgent) {
		g, chat, tasks, fa := agentRig(p, aura)
		g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
		tasks.lastText, tasks.lastTask, tasks.lastAt = "Aura · Bagel run: which entrance?", "task-2", time.Now().Add(-time.Minute)
		return g, chat, fa
	}
	// The incident's words: answered, not relayed, one model call.
	g, chat, fa := open(llm.Proposal{Action: llm.ActionWhere, Task: 0})
	if got := g.say(t, "can you tell me where aura is"); got != "Aura is at the door." || len(chat.posted) != 0 || len(fa.calls) != 1 {
		t.Fatalf("where: reply %q posted %v calls %d", got, chat.posted, len(fa.calls))
	}
	// An answer to the supporter: relayed whole, as before (one call).
	g, chat, fa = open(llm.Proposal{Action: llm.ActionRelay, Task: 0})
	g.say(t, "the side one")
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:the side one" || len(fa.calls) != 1 {
		t.Fatalf("answer: posted %v calls %d", chat.posted, len(fa.calls))
	}
	// A safety report: handed off, not sent to the supporter.
	g, chat, fa = open(llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety})
	g.r.Ops = &memOps{}
	g.say(t, "he is yelling at me")
	if len(chat.posted) != 0 || g.state() != StateHandoff || len(fa.calls) != 1 {
		t.Fatalf("escalate: posted %v state %s calls %d", chat.posted, g.state(), len(fa.calls))
	}
	// Without the agent: the old rule, untouched.
	g, chat, fa = open(llm.Proposal{Action: llm.ActionWhere, Task: 0})
	g.r.Agent = nil
	g.say(t, "can you tell me where aura is")
	if len(chat.posted) != 1 || len(fa.calls) != 0 {
		t.Fatalf("no agent: posted %v calls %d", chat.posted, len(fa.calls))
	}
}

// whereAgentPure: a closed grammar of whole messages (the rule's own,
// with the ways of asking us in front). Review rounds 2–4 found messages
// built from allowed words that still carried an instruction; every one of
// them is here as not pure — and not pure now means answered AND relayed.
func TestWhereAgentPure(t *testing.T) {
	active := []ActiveTask{{ID: "task-2", Title: "Bagel run", SupporterName: "Aura"}, {ID: "task-3", Title: "Laundry pickup", SupporterName: "Bea"},
		{ID: "task-1", Title: "Grocery run", SupporterName: "Will"}}
	pure := []string{
		"can you tell me where aura is", "Can you tell me where Aura is?", "could you please tell me where bea is now",
		"do you know where my supporter is", "let me know where she is please", "any idea where aura is", "hey, is aura close",
		"where's bea", "where is bea now?", "how far is aura", "is Will there yet", "where is aura, thanks", "請問她在哪", "告訴我她在哪",
		// Arbiter 2026-10-30: the second person is answered too
		"where are you?", "are you close?", "how far are you", "where r u", "你在哪",
		// Arbiter 2026-10-30: other languages — a question mark AND a person
		"¿dónde está Aura?", "dónde está aura?", "¿dónde está ella?", "¿donde estas?", "¿ya llegaste?", "¿cuánto te falta?", "¿estás cerca?",
		"où es-tu ?", "tu es où ?", "où est Aura ?", "tu arrives quand ?", "dov'è Aura?", "dov\u2019è lei?", "dove sei?", "quanto ti manca?", "sei arrivata?",
		"彼女は今どこにいますか？", "今どこにいるの？", "どこにいますか？", "지금 어디세요?", "어디쯤이에요?", "어디 계세요?", "도착하셨어요?",
		"where\u2019s bea", // iMessage's curly apostrophe
	}
	notPure := []string{
		// not the rule's shape (answered and relayed)
		"is anyone actually coming??", "has she made it yet", "any idea if bea is close", "她到底到哪了", "where is will",
		// a place, not a supporter
		"where is Starbucks", "Where is Starbucks?", "can you tell me where Starbucks is", "can you tell me where the keys are",
		"Keys在哪", "Package在哪裡", "Cancel在哪", // review r5 #1: the rule's name slot is a supporter's only
		// review r2–r4: an instruction, news, a code or a report rides along
		"is aura close get here now", "where is she get here now", "is she close make it here", "is she close set off now",
		"will be late where is she", "Will be late, where is she?", "still on my way where is she", "she's late where is she",
		"she's here", "is aura close come in", "is aura close let me in", "tell her to hurry", "where is he dont knock",
		"can you tell me where aura is, tell her to ring twice", "can you tell me where aura is, the code is 4521",
		"where is she? the code is 4521", "where is aura i feel unsafe", "where is he i was charged twice",
		"我走了 她到哪了", "我已經到了 她到哪了", "她到哪了 走吧", "她到哪了 快", "她到哪了 不要了", "她到哪了 請她等一下",
		"她到哪了 門沒鎖", "跟我朋友說一下她在哪", "where is she 🔑",
		// other languages: a place, not the supporter, or more than the question
		"dónde está la llave", "¿dónde está Starbucks?", "dov'è la chiave", "où est la clé", "どこに置けばいい？", "어디에 둘까요",
		"dónde está aura? el código es 4521", "dove sei? sono fuori",
		// review: without a question mark these are statements, or "where" about a thing
		"着きました", "도착했어요", "llegó", "está cerca", "è arrivato", "e vicino", "il est loin", "dove", "Dove?", "どこ", "어디",
		"dónde está aura", "cuánto falta",
		// review r2: no person in it — may be about the parcel, the keys, or the task's length
		"¿Dónde está?", "dov'è?", "Où est-il ?", "どこ？", "어디?", "어디예요?", "어디야?", "¿Está cerca?", "Il est loin ?",
		"¿Cuánto tarda?", "quanto ci mette?", "얼마나 걸려요?", "¿Llegó?", "もう着いた？", "도착했어요?", "quanto manca?",
		// review r2: fullwidth Latin is the same letters
		"Ｋｅｙｓ在哪", "Where is Ｓｔａｒｂｕｃｋｓ?",
		// review: "are you there?" asks whether they are answering
		"are you there?", "are you here?",
		// review r3: no ask in front — the answer to "where should I wait?"
		"where you are", "where she is",
	}
	for _, s := range pure {
		if !whereAgentPure(s, active) {
			t.Errorf("%q is nothing but a whereabouts question: should be answered alone", s)
		}
	}
	for _, s := range notPure {
		if whereAgentPure(s, active) {
			t.Errorf("%q is not only a whereabouts question: must also be relayed", s)
		}
	}
}

// Review r2 #3: a supporter whose name is an everyday word. "will she be
// here soon" with Will and Bea on the job must not pick Will; a name that
// disagrees with the model's errand answers for everyone, never a guess.
func TestWhereScope(t *testing.T) {
	will := ActiveTask{ID: "task-1", Title: "Grocery run", SupporterName: "Will"}
	bea := ActiveTask{ID: "task-3", Title: "Laundry pickup", SupporterName: "Bea"}
	both := []ActiveTask{will, bea}
	ids := func(ts []ActiveTask) string {
		var out []string
		for _, x := range ts {
			out = append(out, x.ID)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		msg  string
		task int
		want string
	}{
		{"will she be here soon", 1, "task-3"},         // the verb: the model's errand
		{"Will she be here soon", 1, "task-3"},         // capitalised, but the first word
		{"will she be here soon", -1, "task-1,task-3"}, // no errand: everyone
		{"is Will close", 1, "task-1"},                 // the words beat the model's errand (review r3 #2)
		{"is Will close", 0, "task-1"},
		{"is Will close", -1, "task-1"},
		{"is bea close", 0, "task-3"}, // a lowercase name beats the errand too
		{"is bea close", -1, "task-3"},
		{"who is closer Will or bea", 0, "task-1,task-3"},
	} {
		if got := ids(whereScope(both, tc.msg, tc.task)); got != tc.want {
			t.Errorf("%q task %d: %s, want %s", tc.msg, tc.task, got, tc.want)
		}
	}
}

// Review r5 #1, the rule's own path: its name slot took any capitalised
// word, so "Keys在哪" was answered with the supporter's location. Only a
// supporter's name fills it now; anything else is relayed as before.
func TestWhereRuleNameSlotIsASupporter(t *testing.T) {
	for _, msg := range []string{"Keys在哪", "Package在哪裡", "Where is Starbucks?"} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
		if got := g.say(t, msg); len(chat.posted) != 1 || strings.Contains(got, "at the door") {
			t.Errorf("%q: posted %v reply %q", msg, chat.posted, got)
		}
	}
	for _, msg := range []string{"Where is Sam?", "Sam在哪", "Where is he now?"} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
		if got := g.say(t, msg); len(chat.posted) != 0 || got != "Sam is at the door." {
			t.Errorf("%q: posted %v reply %q", msg, chat.posted, got)
		}
	}
}

// Review r5 #5: a model "where" with more in it while an approval question
// is open — the status first, the words to the supporter, the open
// question said again LAST, the question still armed, and a later plain
// YES approves.
func TestAgentWhereWithMoreKeepsAnOpenApproval(t *testing.T) {
	const msg = "is sam close get here now"
	g, chat, _, fa := agentRig(llm.Proposal{Action: llm.ActionWhere, Task: 0}, bagelT)
	g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
	g.arm(t, "ext-2")
	before, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	sent := len(g.ad.sent)
	g.say(t, msg)
	var replies []string
	for _, o := range g.ad.sent[sent:] {
		replies = append(replies, o.Text)
	}
	if len(fa.calls) != 1 || len(chat.posted) != 1 || chat.posted[0] != "task-2:"+msg || len(g.booker.resolved) != 0 {
		t.Fatalf("model calls %d posted %v resolved %v", len(fa.calls), chat.posted, g.booker.resolved)
	}
	if len(replies) != 2 || replies[0] != "Sam is at the door." || replies[1] != approvalStillPending(before) {
		t.Fatalf("want the status, then the open question last: %q", replies)
	}
	after, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != StateAwaitingApproval || after.PendingExtensionID != before.PendingExtensionID {
		t.Fatalf("the question must stay armed: %s %q → %s %q", before.State, before.PendingExtensionID, after.State, after.PendingExtensionID)
	}
	g.say(t, "YES")
	if len(g.booker.resolved) != 1 || !strings.HasSuffix(g.booker.resolved[0], ":true") {
		t.Fatalf("a plain YES after the answer must approve: %v", g.booker.resolved)
	}
}

// Review r2 #1: fullwidth Latin from an IME ("Ｋｅｙｓ在哪") is read as the
// same letters as ASCII, so the rule's name guard holds on the rule path too.
func TestWhereRuleNormalisesBeforeTheNameGuard(t *testing.T) {
	for _, msg := range []string{"Ｋｅｙｓ在哪", "Where is Ｓｔａｒｂｕｃｋｓ?"} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
		if got := g.say(t, msg); len(chat.posted) != 1 || strings.Contains(got, "at the door") {
			t.Errorf("%q: posted %v reply %q", msg, chat.posted, got)
		}
	}
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Live = &fakeLive{st: LiveStatus{State: LiveAtDoor}}
	if got := g.say(t, "Ｓａｍ在哪"); len(chat.posted) != 0 || got != "Sam is at the door." {
		t.Errorf("fullwidth supporter name: posted %v reply %q", chat.posted, got)
	}
}

// Arbiter 2026-10-30 13:04: while clocked in the supporter is locatable, as
// on the app's map — staging: a delivery answered "Aura is there and clocked
// in" while she was on her way to the drop-off. The store sets Lat/Lng (and
// a distance) only from a fresh fix.
func TestWhereWhileWorkingGivesTheFreshPosition(t *testing.T) {
	lat, lng := 40.75801, -73.98551
	far, near := 1300.0, 40.0
	seen := time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name   string
		st     LiveStatus
		want   string
		posted int
	}{
		{"fresh fix, no task coordinates", LiveStatus{State: LiveWorking, WorkedMinutes: 13, Lat: &lat, Lng: &lng, LastSeen: seen},
			"Sam is working on “Bagel run” (13 min so far), last seen 1 min ago. Map: https://www.google.com/maps?q=40.758,-73.986", 0},
		{"fresh fix, away from the address", LiveStatus{State: LiveWorking, WorkedMinutes: 13, Lat: &lat, Lng: &lng, DistanceMeters: &far, LastSeen: seen},
			"Sam is working on “Bagel run” (13 min so far) — about 0.8 mi from the task's address, last seen 1 min ago. Map: https://www.google.com/maps?q=40.758,-73.986", 0},
		{"fresh fix, at the address (GPS jitter)", LiveStatus{State: LiveWorking, WorkedMinutes: 13, Lat: &lat, Lng: &lng, DistanceMeters: &near, LastSeen: seen},
			"Sam is at the task's address, clocked in on “Bagel run” — 13 min so far.", 0},
		{"stale fix: said, not asked of a working supporter", LiveStatus{State: LiveWorking, WorkedMinutes: 13, LastSeen: time.Now().Add(-7 * time.Minute)},
			"Sam is clocked in on “Bagel run” (13 min so far); no fresh location from their phone, last seen 7 min ago.", 0},
		{"stale for hours", LiveStatus{State: LiveWorking, WorkedMinutes: 13, LastSeen: time.Now().Add(-3 * time.Hour)},
			"Sam is clocked in on “Bagel run” (13 min so far); no fresh location from their phone, last seen 3 h ago.", 0},
		{"never a fix: the clock alone, not \"there\"", LiveStatus{State: LiveWorking, WorkedMinutes: 13},
			"Sam is clocked in on “Bagel run” — 13 min so far.", 0},
	} {
		g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
		g.r.Live = &fakeLive{st: tc.st}
		if got := g.say(t, "where is he?"); got != tc.want || len(chat.posted) != tc.posted {
			t.Errorf("%s: got %q posted %v, want %q", tc.name, got, chat.posted, tc.want)
		}
	}
	// On the way: the same map link while the fix is fresh.
	g, _, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.r.Live = &fakeLive{st: LiveStatus{State: LiveOnTheWay, Lat: &lat, Lng: &lng, LastSeen: seen}}
	if got := g.say(t, "where is he?"); !strings.HasSuffix(got, "Map: https://www.google.com/maps?q=40.758,-73.986") {
		t.Errorf("on the way: %q", got)
	}
}

// Staging 2026-10-30: "Sorry no I don't need that" to a waiting draft was
// re-drafted as a correction, twice. A no in words now drops the draft.
func TestNoInWordsDropsTheDraft(t *testing.T) {
	for _, s := range []string{"Sorry no I don’t need that", "Ok no ‘", "never mind", "forget it", "算了", "不用了謝謝", "actually I don't want it", "No never mind I don’t need that", "never mind, forget it"} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.say(t, "pick up my laundry at 12 Bedford Ave")
		if g.state() != StateAwaitingConfirm {
			t.Fatalf("state %s", g.state())
		}
		if got := g.say(t, s); got != CopyDropped || g.state() != StateIdle {
			t.Errorf("%q: reply %q state %s, want the draft dropped", s, got, g.state())
		}
	}
	for _, s := range []string{"no I don't need the extra time", "no, make it 4pm", "I don't need a car", "not now, tomorrow", "I don't need it today", "no problem, go ahead"} {
		if IsDraftDecline(s) {
			t.Errorf("%q must not drop the draft", s)
		}
	}
}

// Anything the rule does not know: with no supporter on the job, the agent
// is asked with the booking question open; its "decline" drops the draft,
// its "approve" re-asks for a plain YES, anything else is a correction.
func TestAgentDeclineDropsTheDraftWithNoActiveTask(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionDecline, Task: -1})
	g.ids.link(me, "user-1")
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	if got := g.say(t, "honestly I'll just do it myself"); got != CopyDropped || g.state() != StateIdle {
		t.Fatalf("reply %q state %s", got, g.state())
	}
	if n := len(fa.calls); n == 0 || fa.calls[n-1].OpenQuestion != "booking" {
		t.Fatalf("agent not asked with the booking question open: %+v", fa.calls)
	}

	g2, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionApprove, Task: -1})
	g2.ids.link(me, "user-1")
	g2.say(t, "pick up my laundry at 12 Bedford Ave")
	if got := g2.say(t, "sounds good go for it"); got != CopyConfirmReask || g2.state() != StateAwaitingConfirm {
		t.Fatalf("approve: reply %q state %s", got, g2.state())
	}

	g3, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionCorrection, Task: -1})
	g3.ids.link(me, "user-1")
	g3.say(t, "pick up my laundry at 12 Bedford Ave")
	g3.say(t, "make it 4pm")
	if c, _ := g3.convs.GetConversation(context.Background(), "id-"+me); g3.state() != StateAwaitingConfirm || c.Pending == nil || !strings.Contains(c.Pending.Source, "make it 4pm") {
		t.Fatalf("correction: state %s, source kept? %+v", g3.state(), c.Pending)
	}

	// Review r1: an agent "decline" on a no that carries a change is the
	// correction, never a dropped draft.
	for _, s := range []string{"no, I need it at 5", "nope, 2 bags not 1", "forget it, make it laundry instead"} {
		g4, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionDecline, Task: -1})
		g4.ids.link(me, "user-1")
		g4.say(t, "pick up my laundry at 12 Bedford Ave")
		g4.say(t, s)
		c, _ := g4.convs.GetConversation(context.Background(), "id-"+me)
		if g4.state() != StateAwaitingConfirm || c.Pending == nil || !strings.Contains(c.Pending.Source, s) {
			t.Errorf("%q: state %s — the correction was lost", s, g4.state())
		}
	}
}
