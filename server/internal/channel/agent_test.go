package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// fakeAgent returns a fixed proposal and records what it was shown.
type fakeAgent struct {
	p     llm.Proposal
	err   error
	calls []llm.AgentContext
	texts []string
}

func (f *fakeAgent) Propose(_ context.Context, text string, c llm.AgentContext) (llm.Proposal, error) {
	f.calls, f.texts = append(f.calls, c), append(f.texts, text)
	if f.err != nil {
		return llm.SafeProposal(), f.err
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

// D-18: a booking is never made by the agent — "approve" on a sentence at
// the summary books nothing.
func TestAgentCannotBook(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionApprove, Task: 0}, bagelT)
	g.r.Agent = nil
	g.say(t, "new task: pick up my laundry tomorrow at 3") // drafted without the agent
	g.r.Agent = fa
	g.say(t, "that all sounds wonderful to me")
	if g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posts %d state %s", g.booker.posts, g.state())
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
	if c, _ := g.convs.GetConversation(context.Background(), "id-"+me); c.Pending == nil {
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
