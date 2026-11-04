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

// countingClassifier fails the "no model call" checks by counting.
type countingClassifier struct {
	mu    sync.Mutex
	calls int
}

func (c *countingClassifier) Classify(context.Context, string) (llm.Intent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return llm.IntentNewTask, nil
}

// helpRig: a linked number with ops reachable and every model counted.
func helpRig(t *testing.T) (*rig, *memOps, *fakeAgent, *countingClassifier) {
	t.Helper()
	g, ops := handoffRig(t, llm.IntentNewTask)
	fa := &fakeAgent{p: llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}}
	g.r.Agent = fa
	cc := &countingClassifier{}
	g.r.Classifier = cc
	return g, ops, fa, cc
}

func noModel(t *testing.T, g *rig, fa *fakeAgent, cc *countingClassifier) {
	t.Helper()
	if len(fa.calls) != 0 || cc.calls != 0 || len(g.parser.sources) != 0 {
		t.Fatalf("a model was asked: agent %d classifier %d parser %d", len(fa.calls), cc.calls, len(g.parser.sources))
	}
}

func TestHelpWords(t *testing.T) {
	for _, s := range []string{"HELP", "help", "Help.", " help? ", "\"HELP\"", "hElP..."} {
		if !IsHelpKeyword(s) || IsHelpPlea(s) {
			t.Errorf("%q: keyword %t plea %t, want a bare keyword", s, IsHelpKeyword(s), IsHelpPlea(s))
		}
	}
	for _, s := range []string{"INFO", "info", "Info.", "info?", "INFO!"} {
		if !IsInfoKeyword(s) {
			t.Errorf("%q should be INFO", s)
		}
	}
	for _, s := range []string{"help!", "help!!", "HELP!!!", "help me", "Help me!", "I need help", "i need some help", "please help", "help please",
		"can someone help me", "somebody help", "SOS help", "help help", "I'm in trouble, help", "help 911", "救命"} {
		if !IsHelpPlea(s) || !IsHumanRequest(s) {
			t.Errorf("%q should be a plea for help", s)
		}
		if IsHelpKeyword(s) {
			t.Errorf("%q must not be the bare keyword", s)
		}
	}
	for _, s := range []string{"I need help moving a couch", "help me pick up groceries", "can you help with laundry", "information", "helpful, thanks", "info about pricing", "yes", ""} {
		if IsHelpPlea(s) || IsHelpKeyword(s) || IsInfoKeyword(s) {
			t.Errorf("%q is an ordinary message", s)
		}
	}
	// "talk to a person" is what the info line teaches: it must hand off.
	if !IsHumanRequest("talk to a person") || !IsHumanRequest("Talk to a person.") {
		t.Error(`"talk to a person" must be a human request`)
	}
}

// One SMS segment on Sendblue's fallback: ≤160 characters of GSM-7.
func TestHelpInfoFitsOneSMS(t *testing.T) {
	if n := len(CopyHelpInfo); n > 160 {
		t.Fatalf("CopyHelpInfo is %d characters", n)
	}
	for _, c := range CopyHelpInfo {
		if c > 0x7e || c == '`' || c == '^' || c == '{' || c == '}' || c == '[' || c == ']' || c == '~' || c == '|' || c == '\\' {
			t.Fatalf("%q is not plain GSM-7", c)
		}
	}
	for _, must := range []string{"HO:RA", "info@my-hora.com", "STOP", `"cancel task"`, `"talk to a person"`, "Msg&data rates may apply"} {
		if !strings.Contains(CopyHelpInfo, must) {
			t.Errorf("CopyHelpInfo lacks %q", must)
		}
	}
}

// Bare HELP / INFO with nothing under way: the info line, by rule.
func TestBareHelpWithNothingUnderWayGetsTheInfoLine(t *testing.T) {
	for _, s := range []string{"HELP", "help", "Help.", "INFO", "info?"} {
		g, ops, fa, cc := helpRig(t)
		if got := g.say(t, s); got != CopyHelpInfo {
			t.Errorf("%q: got %q", s, got)
		}
		if len(ops.notices) != 0 || g.state() != StateIdle {
			t.Errorf("%q: notices %d state %s", s, len(ops.notices), g.state())
		}
		noModel(t, g, fa, cc)
	}
}

// A number with no account has no task: the info line too.
func TestBareHelpFromAnUnknownNumber(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	ops := &memOps{}
	g.r.Ops = ops
	if got := g.say(t, "HELP"); got != CopyHelpInfo || len(ops.notices) != 0 {
		t.Fatalf("got %q notices %d", got, len(ops.notices))
	}
}

// Bare HELP with a task under way may be someone in trouble: ops.
func TestBareHelpWithATaskUnderWayHandsOff(t *testing.T) {
	g, ops, fa, cc := helpRig(t)
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	if got := g.say(t, "HELP"); got != CopyHandoffStart || g.state() != StateHandoff {
		t.Fatalf("got %q state %s", got, g.state())
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffHumanRequested {
		t.Fatalf("notices %+v", ops.notices)
	}
	noModel(t, g, fa, cc)

	// INFO is never trouble: the info line, task or not.
	g, ops, _, _ = helpRig(t)
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	if got := g.say(t, "INFO"); got != CopyHelpInfo || len(ops.notices) != 0 {
		t.Fatalf("INFO: got %q notices %d", got, len(ops.notices))
	}
}

// Tasks that cannot be read count as under way.
func TestBareHelpWhenTasksCannotBeReadHandsOff(t *testing.T) {
	g, ops, _, _ := helpRig(t)
	g.booker.openErr = errors.New("db down")
	if got := g.say(t, "help"); got != CopyHandoffStart || len(ops.notices) != 1 {
		t.Fatalf("got %q notices %d", got, len(ops.notices))
	}
}

// A supporter's question waiting on them: a task is on.
func TestBareHelpWithARequestOpenHandsOff(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	g.arm(t, "ext-b1")
	if got := g.say(t, "HELP"); got != CopyHandoffStart || len(ops.notices) != 1 || ops.notices[0].OpenRequestID != "ext-b1" {
		t.Fatalf("got %q notices %+v", got, ops.notices)
	}
}

// "help" with anything to it: ops, by rule, with or without a task.
func TestHelpPleaHandsOffByRule(t *testing.T) {
	for _, s := range []string{"help me", "help!!", "Help!", "I need help", "can someone help me please"} {
		g, ops, fa, cc := helpRig(t)
		if got := g.say(t, s); got != CopyHandoffStart || g.state() != StateHandoff {
			t.Errorf("%q: got %q state %s", s, got, g.state())
		}
		if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffHumanRequested {
			t.Errorf("%q: notices %+v", s, ops.notices)
		}
		noModel(t, g, fa, cc)
	}
}

// A help word inside an errand is an errand: read as usual.
func TestHelpInAnErrandIsReadAsUsual(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	got := g.say(t, "I need help moving a couch")
	if got == CopyHelpInfo || len(ops.notices) != 0 || g.state() == StateHandoff {
		t.Fatalf("got %q notices %d state %s", got, len(ops.notices), g.state())
	}
	if len(g.parser.sources) == 0 {
		t.Fatal("the errand was not read")
	}
}

func TestTalkToAPersonHandsOffByRule(t *testing.T) {
	g, ops, fa, cc := helpRig(t)
	if got := g.say(t, "talk to a person"); got != CopyHandoffStart || g.state() != StateHandoff {
		t.Fatalf("got %q state %s", got, g.state())
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffHumanRequested {
		t.Fatalf("notices %+v", ops.notices)
	}
	noModel(t, g, fa, cc)
}

// While a booking is being made, a bare HELP is a plea.
func TestBareHelpWhilePostingReachesOps(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.sayDuringPost(t, "HELP")
	g.say(t, "YES")
	if len(ops.notices) < 1 || ops.notices[0].Reason != HandoffHumanRequested {
		t.Fatalf("notices %+v", ops.notices)
	}
	if contains(g.sentTexts(), CopyHelpInfo) {
		t.Fatal("the info line was sent while booking")
	}
}

// A stopped number's HELP: forwarded to ops as urgent, nothing sent back
// (forwardRig.say fails on any send).
func TestOptedOutHelpIsForwardedNotAnswered(t *testing.T) {
	for _, s := range []string{"HELP", "help me", "INFO"} {
		f := newForwardRig(t)
		f.say(t, s)
		fw := f.forwards()
		if len(fw) != 1 {
			t.Fatalf("%q: forwards %d", s, len(fw))
		}
		if want := s != "INFO"; fw[0].Urgent != want {
			t.Errorf("%q: urgent %t, want %t", s, fw[0].Urgent, want)
		}
	}
}

// WhatsApp has no STOP: no info line there; HELP with a task is still ops'.
func TestHelpOnWhatsApp(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	wa := &fakeSendAdapter{}
	g.r.Adapters[WhatsApp] = wa
	g.ids.mu.Lock()
	g.ids.ids["whatsapp|"+me] = Identity{ID: "id-wa", Channel: WhatsApp, Address: me, UserID: "user-1"}
	g.ids.mu.Unlock()
	in := Inbound{Channel: WhatsApp, ProviderMessageID: "wa-1", From: me, Text: "HELP", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if wa.last() == CopyHelpInfo || len(ops.notices) != 0 {
		t.Fatalf("got %q notices %d", wa.last(), len(ops.notices))
	}
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	in.ProviderMessageID, in.ReceivedAt = "wa-2", time.Now()
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffHumanRequested {
		t.Fatalf("notices %+v", ops.notices)
	}
}
