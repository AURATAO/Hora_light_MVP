package channel

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// memPilotReplies is PGStore's PilotReplyStore over the rig's sends: a
// closed line counts from the moment it went out, on the rig's clock.
type memPilotReplies struct {
	mu    sync.Mutex
	ad    *fakeSendAdapter
	clock func() time.Time
	seen  int                    // sends already stamped
	at    map[string][]time.Time // address → closed-line send times
}

func (m *memPilotReplies) sync() {
	m.ad.mu.Lock()
	defer m.ad.mu.Unlock()
	for ; m.seen < len(m.ad.sent); m.seen++ {
		if o := m.ad.sent[m.seen]; o.Text == CopyPilotClosed {
			m.at[o.To] = append(m.at[o.To], m.clock())
		}
	}
}

func (m *memPilotReplies) PilotRepliedSince(_ context.Context, ch Channel, addr string, since time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sync()
	for _, t := range m.at[addr] {
		if t.After(since) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memPilotReplies) GatedSenders(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}

// pilotRig: the gate on with nobody allowlisted, every model counted, ops
// reachable, opt-outs kept, and a clock the test moves.
type pilotRig struct {
	*rig
	ops   *memOps
	fa    *fakeAgent
	cc    *countingClassifier
	oo    *memOptOuts
	clock time.Time
}

func newPilotRig(t *testing.T, allow ...string) *pilotRig {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	p := &pilotRig{rig: g, clock: time.Now()}
	g.r.Now = func() time.Time { return p.clock }
	p.ops = &memOps{}
	g.r.Ops = p.ops
	g.r.Thread = memThread{st: g.st}
	p.fa = &fakeAgent{p: llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}}
	g.r.Agent = p.fa
	p.cc = &countingClassifier{}
	g.r.Classifier = p.cc
	p.oo = &memOptOuts{ids: g.ids, marked: map[string]bool{}}
	g.r.OptOuts = p.oo
	replies := &memPilotReplies{ad: g.ad, clock: func() time.Time { return p.clock }, at: map[string][]time.Time{}}
	g.r.Pilot = NewPilotGate(replies, allow...)
	return p
}

func (p *pilotRig) noModel(t *testing.T) {
	t.Helper()
	noModel(t, p.rig, p.fa, p.cc)
}

// A stranger: one closed line, nothing else — no draft, no booking, no
// setup link, no hand-off, no model. Again within 24 hours: silence. After
// 24 hours: the line once more.
func TestPilotGateStrangerGetsOneReply(t *testing.T) {
	p := newPilotRig(t)
	if got := p.say(t, "laundry pickup at 12 Bedford Ave tomorrow 3pm"); got != CopyPilotClosed {
		t.Fatalf("got %q, want the closed line", got)
	}
	if n := p.sentCount(); n != 1 {
		t.Fatalf("%d sends, want 1", n)
	}
	p.noModel(t)
	c := p.conv(t)
	if c.Pending != nil || c.State != StateIdle || p.booker.posts != 0 || len(p.ids.tokens) != 0 || len(p.ops.notices) != 0 {
		t.Fatalf("pending=%v state=%s posts=%d tokens=%d notices=%d", c.Pending, c.State, p.booker.posts, len(p.ids.tokens), len(p.ops.notices))
	}

	// Within 24 hours, whatever it says: no reply, still nothing else.
	for i, s := range []string{"hello?", "talk to a person", "help me", "yes", "can I drive for you?"} {
		p.clock = p.clock.Add(time.Hour)
		p.say(t, s)
		if n := p.sentCount(); n != 1 {
			t.Fatalf("message %d %q: %d sends, want still 1", i, s, n)
		}
	}
	p.noModel(t)
	if len(p.ops.notices) != 0 || p.booker.posts != 0 {
		t.Fatalf("notices=%d posts=%d", len(p.ops.notices), p.booker.posts)
	}

	// A day after the line: once more.
	p.clock = p.clock.Add(20 * time.Hour)
	if got := p.say(t, "are you open yet?"); got != CopyPilotClosed || p.sentCount() != 2 {
		t.Fatalf("after 24h: got %q, %d sends", got, p.sentCount())
	}
	p.noModel(t)
}

// A photo from a stranger is not taken, and the parser never sees it.
func TestPilotGateStrangerPhotoNotKept(t *testing.T) {
	p := newPilotRig(t)
	ph := withPhotoRig(p.rig)
	if got := ph.sendPhoto(t, []byte("\xff\xd8\xff jpeg"), "what is this?"); got != CopyPilotClosed {
		t.Fatalf("got %q", got)
	}
	if n := len(ph.photos.rows); n != 0 {
		t.Fatalf("%d photos saved", n)
	}
	if n := len(ph.blobs.objs); n != 0 {
		t.Fatalf("%d objects stored", n)
	}
	p.noModel(t)
}

// STOP / START from a gated stranger work as without the gate.
func TestPilotGateStopStartAsBefore(t *testing.T) {
	p := newPilotRig(t)
	p.say(t, "STOP")
	if !p.oo.is(me) {
		t.Fatal("STOP did not opt the number out")
	}
	if n := p.sentCount(); n != 0 {
		t.Fatalf("%d sends after STOP, want 0", n)
	}
	// While stopped: nothing is sent, the closed line included.
	p.say(t, "pick up my laundry")
	if n := p.sentCount(); n != 0 {
		t.Fatalf("%d sends to a stopped number", n)
	}
	if got := p.say(t, "START"); got != CopyOptInWelcome {
		t.Fatalf("START: got %q", got)
	}
	if p.oo.is(me) {
		t.Fatal("START did not clear the mark")
	}
	// Then gated as any stranger.
	if got := p.say(t, "pick up my laundry"); got != CopyPilotClosed {
		t.Fatalf("after START: got %q", got)
	}
	p.noModel(t)

	// The same words without the gate, side by side.
	g, oo, _ := optOutRig(t)
	g.say(t, "STOP")
	if !oo.is(me) || g.sentCount() != 0 {
		t.Fatalf("ungated STOP: opted=%t sends=%d", oo.is(me), g.sentCount())
	}
	if got := g.say(t, "START"); got != CopyOptInWelcome {
		t.Fatalf("ungated START: got %q", got)
	}
}

// A bare HELP / INFO from a stranger gets the info line, as today, and does
// not use up the day's closed line.
func TestPilotGateHelpAsBefore(t *testing.T) {
	p := newPilotRig(t)
	if got := p.say(t, "HELP"); got != CopyHelpInfo {
		t.Fatalf("HELP: got %q", got)
	}
	if got := p.say(t, "info"); got != CopyHelpInfo {
		t.Fatalf("INFO: got %q", got)
	}
	if got := p.say(t, "laundry pickup"); got != CopyPilotClosed {
		t.Fatalf("after HELP: got %q", got)
	}
	// HELP again inside the day: still the info line.
	if got := p.say(t, "help"); got != CopyHelpInfo {
		t.Fatalf("HELP again: got %q", got)
	}
	p.noModel(t)
	if len(p.ops.notices) != 0 {
		t.Fatalf("%d ops notices", len(p.ops.notices))
	}
}

// An allowlisted number books as it would with no gate.
func TestPilotGateAllowlistedBooks(t *testing.T) {
	p := newPilotRig(t, "(555) 123-4567") // me, written loosely
	p.r.Classifier = fakeClassifier{intent: llm.IntentNewTask}
	p.r.Agent = nil
	p.ids.link(me, "user-1")
	if got := p.say(t, "laundry pickup"); !strings.Contains(got, "Reply YES") {
		t.Fatalf("got %q, want the summary", got)
	}
	if got := p.say(t, "yes"); got != CopyBooked {
		t.Fatalf("got %q", got)
	}
	if p.booker.posts != 1 || p.state() != StateIdle {
		t.Fatalf("posts=%d state=%s", p.booker.posts, p.state())
	}
	for _, o := range p.ad.sent {
		if o.Text == CopyPilotClosed {
			t.Fatal("an allowlisted number got the closed line")
		}
	}
}

// An unlinked allowlisted number gets the setup link, as without the gate.
func TestPilotGateAllowlistedNewNumberGetsSetup(t *testing.T) {
	p := newPilotRig(t, me)
	p.r.Classifier = fakeClassifier{intent: llm.IntentNewTask}
	p.r.Agent = nil
	p.say(t, "laundry pickup")
	if len(p.parser.sources) == 0 {
		t.Fatal("the parser never read an allowlisted number's errand")
	}
	for _, o := range p.ad.sent {
		if o.Text == CopyPilotClosed {
			t.Fatal("an allowlisted number got the closed line")
		}
	}
}

// An approved supporter is routed as today (the "already a supporter" line
// for a job question); a supporter still applying is gated.
func TestPilotGateSupporterUnaffected(t *testing.T) {
	p := newPilotRig(t)
	p.ids.link(me, "user-1")
	p.r.Supporters = fakeSupporters{status: map[string]string{"user-1": "approved"}}
	if got := p.say(t, "can I work for you?"); got != CopySupporterAlready {
		t.Fatalf("approved supporter: got %q", got)
	}

	q := newPilotRig(t)
	q.ids.link(me, "user-1")
	q.r.Supporters = fakeSupporters{status: map[string]string{"user-1": "pending"}}
	if got := q.say(t, "can I work for you?"); got != CopyPilotClosed {
		t.Fatalf("pending applicant: got %q", got)
	}
}

// Ops numbers (CHANNEL_OPS_NUMBERS) and the allowlist come from the
// environment; with the gate unset there is no gate at all.
func TestPilotGateFromEnv(t *testing.T) {
	t.Setenv("CHANNEL_PILOT_ALLOWLIST", "+1 212 555 0100, not-a-number")
	t.Setenv("CHANNEL_OPS_NUMBERS", "+15551234567")
	for _, v := range []string{"", "0", "true", "yes"} {
		t.Setenv("CHANNEL_PILOT_GATE", v)
		if g := PilotGateFromEnv(nil); g != nil {
			t.Fatalf("CHANNEL_PILOT_GATE=%q built a gate", v)
		}
	}
	t.Setenv("CHANNEL_PILOT_GATE", "1")
	g := PilotGateFromEnv(nil)
	if g == nil {
		t.Fatal("CHANNEL_PILOT_GATE=1: no gate")
	}
	if !g.Allowed("+12125550100") || !g.Allowed(me) || g.Allowed("+19175550199") {
		t.Fatalf("allowed: list %t ops %t stranger %t", g.Allowed("+12125550100"), g.Allowed(me), g.Allowed("+19175550199"))
	}
}

// An ops number texting a request is routed as today.
func TestPilotGateOpsNumberUnaffected(t *testing.T) {
	t.Setenv("CHANNEL_PILOT_GATE", "1")
	t.Setenv("CHANNEL_PILOT_ALLOWLIST", "")
	t.Setenv("CHANNEL_OPS_NUMBERS", me)
	p := newPilotRig(t)
	p.r.Pilot = PilotGateFromEnv(p.r.Pilot.Replies)
	p.ids.link(me, "user-1")
	if got := p.say(t, "talk to a person"); got != CopyHandoffStart {
		t.Fatalf("ops number: got %q, want a hand-off", got)
	}
	if len(p.ops.notices) != 1 {
		t.Fatalf("%d ops notices", len(p.ops.notices))
	}
}

// With no gate (CHANNEL_PILOT_GATE unset: Router.Pilot nil) a stranger's
// errand is read exactly as before.
func TestPilotGateOffChangesNothing(t *testing.T) {
	t.Setenv("CHANNEL_PILOT_GATE", "")
	g := newRig(llm.IntentNewTask)
	g.r.Pilot = PilotGateFromEnv(nil)
	if g.r.Pilot != nil {
		t.Fatal("gate built while unset")
	}
	g.say(t, "laundry pickup")
	if len(g.parser.sources) == 0 {
		t.Fatal("parser not asked")
	}
	for _, o := range g.ad.sent {
		if o.Text == CopyPilotClosed {
			t.Fatal("closed line sent with the gate off")
		}
	}
}

// The closed line: plain words, under the length that splits on SMS.
func TestPilotClosedCopy(t *testing.T) {
	if len(CopyPilotClosed) > 160 {
		t.Fatalf("CopyPilotClosed is %d characters", len(CopyPilotClosed))
	}
}
