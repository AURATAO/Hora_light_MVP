package channel

// fix/scope-ambiguous-words (2026-11-14): the production transcripts of
// 2026-11-13/14, on WhatsApp and iMessage (one router), the clarification
// after a refusal, and the welcome only at the start of a conversation.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// threadScope is a thread-reading model: verdicts by message (and, after a
// refusal, by "refused||message"), with what it was shown.
type threadScope struct {
	mu        sync.Mutex
	verdict   map[string]llm.ScopeReading
	asked     []llm.ScopeQuery
	plainCall int
}

func (s *threadScope) CheckScope(_ context.Context, text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plainCall++
	if v, ok := s.verdict[text]; ok {
		return v.Verdict, nil
	}
	return llm.ScopeAllowID, nil
}

func (s *threadScope) CheckScopeIn(_ context.Context, q llm.ScopeQuery) (llm.ScopeReading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, q)
	key := q.Text
	if q.Refused != "" {
		key = q.Refused + "||" + q.Text
	}
	if v, ok := s.verdict[key]; ok {
		return v, nil
	}
	return llm.ScopeReading{Verdict: llm.ScopeAllowID}, nil
}

// textClassifier labels by message (default new_task).
type textClassifier map[string]llm.Intent

func (c textClassifier) Classify(_ context.Context, text string) (llm.Intent, error) {
	if i, ok := c[text]; ok {
		return i, nil
	}
	return llm.IntentNewTask, nil
}

// logThread is the recorded thread: every inbound as it arrives, every
// send after it (what channel_messages holds, in order).
type logThread struct {
	mu   sync.Mutex
	msgs []ThreadMessage
}

func (l *logThread) RecentMessages(_ context.Context, _ Channel, _ string, limit int) ([]ThreadMessage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]ThreadMessage(nil), l.msgs...)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

type chatRig struct {
	*rig
	ch     Channel
	addr   string
	wa     *fakeSendAdapter
	thread *logThread
	scope  *threadScope
}

// chatRigOn: an unlinked number on ch, with a thread and the thread-reading
// model.
func chatRigOn(t *testing.T, ch Channel, cls textClassifier, verdicts map[string]llm.ScopeReading) *chatRig {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	wa := &fakeSendAdapter{}
	g.r.Adapters[WhatsApp] = wa
	g.r.Classifier = cls
	g.r.Ops = &memOps{}
	th := &logThread{}
	g.r.Thread = th
	sc := &threadScope{verdict: verdicts}
	g.r.Scope = sc
	addr := me
	if ch == WhatsApp {
		addr = "+15557654321"
	}
	return &chatRig{rig: g, ch: ch, addr: addr, wa: wa, thread: th, scope: sc}
}

func (c *chatRig) convOf(t *testing.T) Conversation {
	t.Helper()
	cv, err := c.convs.GetConversation(context.Background(), "id-"+c.addr)
	if err != nil {
		t.Fatal(err)
	}
	return cv
}

func (c *chatRig) adapter() *fakeSendAdapter {
	if c.ch == WhatsApp {
		return c.wa
	}
	return c.ad
}

// send: the message is recorded first (as the webhook does), then routed;
// our replies are recorded after it. Returns the replies.
func (c *chatRig) send(t *testing.T, text string) []string {
	t.Helper()
	ad := c.adapter()
	before := len(ad.sent)
	in := Inbound{Channel: c.ch, ProviderMessageID: text + time.Now().String(), From: c.addr, Text: text, ReceivedAt: time.Now()}
	c.thread.mu.Lock()
	c.thread.msgs = append(c.thread.msgs, ThreadMessage{Direction: "inbound", Text: text, At: time.Now(), ProviderID: in.ProviderMessageID})
	c.thread.mu.Unlock()
	if err := c.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
	var out []string
	for _, o := range ad.sent[before:] {
		out = append(out, o.Text)
		c.thread.mu.Lock()
		c.thread.msgs = append(c.thread.msgs, ThreadMessage{Direction: "outbound", Text: o.Text, At: time.Now()})
		c.thread.mu.Unlock()
	}
	return out
}

var bothChannels = []Channel{WhatsApp, IMessage}

const (
	heyTea1 = "Hi I need someone to grab a drink from Hay Tea Noho nyc"
	heyTea2 = "Hay tea is a bubble tea"
	perfect = "Hi — I need help with finding a perfect man"
)

func noWelcome(t *testing.T, ch Channel, msg string, replies []string) {
	t.Helper()
	for _, r := range replies {
		if r == CopyWelcome || strings.Contains(r, "I'm your HO:RA concierge") {
			t.Errorf("%s %q: welcomed: %q", ch, msg, r)
		}
	}
}

// Production 2026-11-14 (WhatsApp): the HeyTea pickup is not refused by
// rule — Sonnet reads it — and goes on to a draft. No welcome.
func TestProdHeyTeaIsAPickup(t *testing.T) {
	for _, ch := range bothChannels {
		c := chatRigOn(t, ch, textClassifier{heyTea2: llm.IntentOther}, nil)
		if d := scopeSet(t).Rule(heyTea1); d.Decided {
			t.Fatalf("%q decided by rule (%s): the model must read it", heyTea1, d.By)
		}
		replies := c.send(t, heyTea1)
		noWelcome(t, ch, heyTea1, replies)
		for _, r := range replies {
			if scopeSet(t).RefusalByReply(r) != nil {
				t.Fatalf("%s: refused: %q", ch, r)
			}
		}
		if len(c.scope.asked) != 1 || len(c.parser.sources) != 1 || c.parser.sources[0] != heyTea1 {
			t.Fatalf("%s: model asked %d, parses %q", ch, len(c.scope.asked), c.parser.sources)
		}
	}
}

// Production 2026-11-14 as it happened (a model that did say alcohol): the
// correction is read with the refused message; allowed together, the
// errand goes on as if the first had been allowed — never the welcome.
func TestProdHeyTeaCorrectionAfterRefusal(t *testing.T) {
	for _, ch := range bothChannels {
		c := chatRigOn(t, ch, textClassifier{heyTea2: llm.IntentOther}, map[string]llm.ScopeReading{
			heyTea1:                  {Verdict: "alcohol"},
			heyTea1 + "||" + heyTea2: {Verdict: llm.ScopeAllowID, Clarifies: true},
		})
		first := c.send(t, heyTea1)
		alc := scopeSet(t).Refusal("alcohol")
		if len(first) != 1 || first[0] != alc.Reply {
			t.Fatalf("%s: first reply %q", ch, first)
		}
		second := c.send(t, heyTea2)
		noWelcome(t, ch, heyTea2, second)
		if len(c.scope.asked) != 2 {
			t.Fatalf("%s: model asked %d times", ch, len(c.scope.asked))
		}
		q := c.scope.asked[1]
		if q.Refused != heyTea1 || q.Text != heyTea2 || len(q.Thread) != 2 || q.Thread[0] != "Customer: "+heyTea1 || q.Thread[1] != "HO:RA: "+alc.Reply {
			t.Fatalf("%s: model shown %+v", ch, q)
		}
		want := llm.JoinClarified(heyTea1, heyTea2)
		if len(c.parser.sources) != 1 || c.parser.sources[0] != want {
			t.Fatalf("%s: drafted %q, want the two together", ch, c.parser.sources)
		}
		if cv := c.convOf(t); cv.Pending == nil && cv.State != StateAwaitingName {
			t.Fatalf("%s: no draft; state %s replies %q", ch, cv.State, second)
		}
	}
}

// Production 2026-11-13 (iMessage): matchmaking is a short, kind decline,
// not the welcome; ops are not alerted (not a request of a supporter).
func TestProdPerfectManIsDeclined(t *testing.T) {
	for _, ch := range bothChannels {
		c := chatRigOn(t, ch, textClassifier{perfect: llm.IntentOther}, map[string]llm.ScopeReading{perfect: {Verdict: "dating"}})
		if d := scopeSet(t).Rule(perfect); d.Decided {
			t.Fatalf("%q decided by rule (%s)", perfect, d.By)
		}
		replies := c.send(t, perfect)
		dating := scopeSet(t).Refusal("dating")
		if len(replies) != 1 || replies[0] != dating.Reply || dating.Alert {
			t.Fatalf("%s: replies %q", ch, replies)
		}
		if len(c.parser.sources) != 0 || len(c.r.Ops.(*memOps).notices) != 0 {
			t.Fatalf("%s: parses %d notices %d", ch, len(c.parser.sources), len(c.r.Ops.(*memOps).notices))
		}
	}
	// By rule: the unambiguous matchmaking words, no model call.
	for _, m := range []string{"find me a husband", "can you set me up with someone", "I want to find a boyfriend", "be my matchmaker"} {
		if d := scopeSet(t).Rule(m); d.ID() != "dating" {
			t.Errorf("%q: %s", m, d.By)
		}
	}
}

// After a refusal, a new request is read as itself, and the refused one
// is not drafted with it.
func TestNewRequestAfterRefusalIsItsOwn(t *testing.T) {
	c := chatRigOn(t, WhatsApp, nil, map[string]llm.ScopeReading{})
	c.send(t, "buy me a bottle of wine")
	next := "ok then can you pick up my dry cleaning at 5th Ave Cleaners"
	c.send(t, next)
	if len(c.parser.sources) != 1 || c.parser.sources[0] != next {
		t.Fatalf("drafted %q", c.parser.sources)
	}
	// A "clarification" that keeps the unambiguous word is still refused,
	// by rule on the two together, without the model.
	// Even when the model would let it through.
	c = chatRigOn(t, IMessage, textClassifier{"it's for a gift": llm.IntentOther}, map[string]llm.ScopeReading{
		"buy me a bottle of wine||it's for a gift": {Verdict: llm.ScopeAllowID, Clarifies: true},
	})
	c.send(t, "buy me a bottle of wine")
	got := c.send(t, "it's for a gift")
	if len(got) != 1 || got[0] != scopeSet(t).Refusal("alcohol").Reply || len(c.parser.sources) != 0 {
		t.Fatalf("replies %q parses %q", got, c.parser.sources)
	}
	// Past the window, the earlier refusal is not read with it.
	c = chatRigOn(t, IMessage, nil, map[string]llm.ScopeReading{})
	c.send(t, "buy me a bottle of wine")
	c.thread.mu.Lock()
	for i := range c.thread.msgs {
		c.thread.msgs[i].At = time.Now().Add(-ScopeFollowupWindow - time.Minute)
	}
	c.thread.mu.Unlock()
	c.send(t, "a bubble tea from Gong Cha please")
	if len(c.parser.sources) != 1 || c.parser.sources[0] != "a bubble tea from Gong Cha please" {
		t.Fatalf("after the window: drafted %q", c.parser.sources)
	}
}

// The welcome: a greeting with no request, at the start of a conversation
// only. Mid-conversation, a request, a clarification, or anything else
// gets a short line instead.
func TestWelcomeOnlyAtTheStart(t *testing.T) {
	for _, ch := range bothChannels {
		c := chatRigOn(t, ch, textClassifier{"hi": llm.IntentGreeting, "hello!": llm.IntentGreeting, "Hay tea is a bubble tea": llm.IntentOther, "Hi — I need help with something": llm.IntentGreeting, "hmm": llm.IntentOther}, nil)
		if got := c.send(t, "hi"); len(got) != 1 || got[0] != CopyWelcome {
			t.Fatalf("%s: first hello %q", ch, got)
		}
		if got := c.send(t, "hello!"); len(got) != 1 || got[0] != CopyHelloAgain {
			t.Fatalf("%s: second hello %q", ch, got)
		}
		if got := c.send(t, "Hay tea is a bubble tea"); len(got) != 1 || got[0] != CopyNotSure {
			t.Fatalf("%s: mid-conversation other %q", ch, got)
		}
		// A greeting with a request, at the start: not the welcome.
		c = chatRigOn(t, ch, textClassifier{"Hi — I need help with something": llm.IntentGreeting}, nil)
		if got := c.send(t, "Hi — I need help with something"); len(got) != 1 || got[0] != CopyNotSure {
			t.Fatalf("%s: greeting with a request %q", ch, got)
		}
		// Anything else at the start: not the welcome either.
		c = chatRigOn(t, ch, textClassifier{"hmm": llm.IntentOther}, nil)
		if got := c.send(t, "hmm"); len(got) != 1 || got[0] != CopyNotSure {
			t.Fatalf("%s: other at the start %q", ch, got)
		}
		// A new conversation, WelcomeGap later: welcomed again.
		c = chatRigOn(t, ch, textClassifier{"hi": llm.IntentGreeting}, nil)
		c.send(t, "hi")
		cv := c.convOf(t)
		old := time.Now().Add(-WelcomeGap - time.Hour)
		cv.LastOutboundAt = &old
		c.convs.mu.Lock()
		c.convs.convs[cv.IdentityID] = cv
		c.convs.mu.Unlock()
		if got := c.send(t, "hi"); len(got) != 1 || got[0] != CopyWelcome {
			t.Fatalf("%s: hello after the gap %q", ch, got)
		}
	}
}

// No refusal reply offers to meet someone (companionship paused,
// 2026-11-14): each ends with everyday errands.
func TestRefusalCopyOffersErrandsOnly(t *testing.T) {
	for _, r := range scopeSet(t).Refusals {
		for _, bad := range []string{"coffee", "a walk", "walk in the park", "company for", "spend time with you", "somewhere public", "public instead"} {
			if strings.Contains(strings.ToLower(r.Reply), bad) {
				t.Errorf("%s reply offers meeting (%q): %s", r.ID, bad, r.Reply)
			}
		}
	}
}

func TestGreetingOnly(t *testing.T) {
	for _, s := range []string{"hi", "Hello!", "hey there", "good morning", "is this HO:RA?"} {
		if !GreetingOnly(s) {
			t.Errorf("%q: not a greeting alone", s)
		}
	}
	for _, s := range []string{perfect, "hi, can you pick up my laundry", "hey I need a favor", "hello, how much is it?"} {
		if GreetingOnly(s) {
			t.Errorf("%q: read as a greeting alone", s)
		}
	}
}
