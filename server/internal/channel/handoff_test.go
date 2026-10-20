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

type memOps struct {
	mu      sync.Mutex
	notices []HandoffNotice
	err     error
}

func (m *memOps) NotifyOps(_ context.Context, n HandoffNotice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.notices = append(m.notices, n)
	return nil
}

// memThread serves the fake store's outbound bodies as a thread, newest last.
type memThread struct{ st *fakeStore }

func (m memThread) RecentMessages(_ context.Context, _ Channel, _ string, limit int) ([]ThreadMessage, error) {
	bodies := m.st.storedBodies()
	var out []ThreadMessage
	for _, b := range bodies {
		out = append(out, ThreadMessage{Direction: "outbound", Text: b, At: time.Now()})
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func handoffRig(t *testing.T, intent llm.Intent) (*rig, *memOps) {
	t.Helper()
	g := newRig(intent)
	g.ids.link(me, "user-1")
	ops := &memOps{}
	g.r.Ops = ops
	g.r.Thread = memThread{st: g.st}
	return g, ops
}

func TestIsHumanRequest(t *testing.T) {
	for _, yes := range []string{"can I talk to a human?", "I want to speak with someone", "is this a bot", "customer service please", "我要真人", "客服"} {
		if !IsHumanRequest(yes) {
			t.Errorf("%q should be a human request", yes)
		}
	}
	for _, no := range []string{"pick up my laundry", "I need help moving a couch", "someone to wait in line for me", "yes", "can a person bring it to my office"} {
		if IsHumanRequest(no) {
			t.Errorf("%q must not be a human request", no)
		}
	}
}

func TestHumanRequestHandsOffAndOpsGetTheThread(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	g.say(t, "laundry pickup")
	got := g.say(t, "can I talk to a real person?")
	if got != CopyHandoffStart || g.state() != StateHandoff {
		t.Fatalf("%q state %s", got, g.state())
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffHumanRequested || ops.notices[0].IdentityID != "id-"+me || ops.notices[0].Update {
		t.Fatalf("notices %+v", ops.notices)
	}
	n := ops.notices[0]
	if n.PhoneHint != "•••• 4567" || strings.Contains(n.PhoneHint, "+1555") {
		t.Fatalf("ops get a hint, not the number: %q", n.PhoneHint)
	}
	if len(n.Thread) == 0 {
		t.Fatal("ops need the thread")
	}
	if g.booker.posts != 0 {
		t.Fatal("nothing is booked by a handoff")
	}
}

// While ops hold the thread: every message is forwarded, nothing is decided,
// and a YES is just text.
func TestWhileHandedOffNothingIsDecided(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	g.say(t, "laundry pickup")
	g.say(t, "talk to a human")
	got := g.say(t, "YES")
	if got != CopyHandoffHolding || g.booker.posts != 0 || g.state() != StateHandoff {
		t.Fatalf("%q posts %d state %s", got, g.booker.posts, g.state())
	}
	if len(ops.notices) != 2 || !ops.notices[1].Update {
		t.Fatalf("the message must be forwarded as an update: %+v", ops.notices)
	}
}

func TestReleaseResumesTheDraft(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.say(t, "laundry pickup")
	g.say(t, "I want customer service")
	if err := g.r.Release(context.Background(), Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	last := g.ad.last()
	if !strings.HasPrefix(last, CopyHandoffReleased) || !strings.Contains(last, "Reply YES to book") || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", last, g.state())
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatal("after release the draft books normally")
	}
}

func TestOpsSendOnlyWhileHandedOff(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentOther)
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}
	if err := g.r.OpsSend(context.Background(), id, "hello from ops"); !errors.Is(err, ErrNotInHandoff) {
		t.Fatalf("ops must not speak over the bot: %v", err)
	}
	g.say(t, "are you a bot?")
	if err := g.r.OpsSend(context.Background(), id, "hello from ops"); err != nil || g.ad.last() != "hello from ops" {
		t.Fatalf("%v %q", err, g.ad.last())
	}
}

func TestTwoParseFailuresInARowHandOff(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	g.parser.err = errors.New("cannot parse")
	if got := g.say(t, "asdf qwer"); got != CopyParseFailed || g.state() != StateIdle {
		t.Fatalf("first: %q %s", got, g.state())
	}
	if got := g.say(t, "zxcv"); got != CopyHandoffStart || g.state() != StateHandoff {
		t.Fatalf("second: %q %s", got, g.state())
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffNotUnderstood {
		t.Fatalf("%+v", ops.notices)
	}
}

// If ops cannot be told, the person is not told "a teammate is coming": the
// route fails so the message is retried, and the state stays as it was.
func TestHandoffFailsLoudlyWhenOpsUnreachable(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentOther)
	ops.err = errors.New("mail down")
	in := Inbound{Channel: IMessage, ProviderMessageID: "h1", From: me, Text: "talk to a human", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err == nil {
		t.Fatal("expected an error")
	}
	if last := g.ad.last(); last == CopyHandoffStart {
		t.Fatal("must not promise a teammate ops never heard about")
	}
}

func TestHumanRequestWinsOverAnApprovalQuestion(t *testing.T) {
	g := approvalRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	askTime(t, g)
	got := g.say(t, "I'd rather talk to a person about this")
	if got != CopyHandoffStart || g.state() != StateHandoff || len(g.booker.resolved) != 0 {
		t.Fatalf("%q state %s resolved %v", got, g.state(), g.booker.resolved)
	}
}
