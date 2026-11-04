package channel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"hora-auth/internal/tracing"
)

// memForwards mirrors PGStore's forward batching on top of memOptOuts. The
// inbound log is what RecordInbound would hold (fed by forwardRig.say).
type memForwards struct {
	*memOptOuts
	mu        sync.Mutex
	now       func() time.Time
	inbound   []ThreadMessage
	forwarded map[string]*time.Time
	through   map[string]int // index into inbound already forwarded
	pending   map[string]bool
	takeErr   error
}

func (m *memForwards) TakeOptedOutBatch(_ context.Context, id string, urgent bool, window time.Duration) (OptedOutBatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := OptedOutBatch{IdentityID: id}
	if m.takeErr != nil {
		return b, m.takeErr
	}
	if !m.marked[string(IMessage)+"|"+me] {
		return b, nil
	}
	now := m.now()
	if prev := m.forwarded[id]; !urgent && prev != nil && now.Sub(*prev) < window {
		m.pending[id] = true
		return b, nil
	}
	for _, msg := range m.inbound[m.through[id]:] {
		if !IsOptOutKeyword(msg.Text) && !IsOptInKeyword(msg.Text) {
			b.Messages = append(b.Messages, msg)
		}
	}
	prevThrough := m.through[id]
	m.through[id] = len(m.inbound)
	m.pending[id] = false
	if len(b.Messages) == 0 {
		return b, nil
	}
	b.Due, b.ForwardedAt, b.PrevForwardedAt = true, now, m.forwarded[id]
	pt := time.Unix(int64(prevThrough), 0)
	b.PrevThrough = &pt
	m.forwarded[id] = &now
	return b, nil
}

func (m *memForwards) RequeueOptedOutBatch(_ context.Context, b OptedOutBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.forwarded[b.IdentityID]; f == nil || !f.Equal(b.ForwardedAt) {
		return nil
	}
	m.forwarded[b.IdentityID] = b.PrevForwardedAt
	if b.Urgent {
		m.forwarded[b.IdentityID] = nil
	}
	m.through[b.IdentityID] = int(b.PrevThrough.Unix())
	m.pending[b.IdentityID] = true
	return nil
}

func (m *memForwards) WaitingOptedOutForwards(_ context.Context, window time.Duration) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id, p := range m.pending {
		if f := m.forwarded[id]; p && (f == nil || m.now().Sub(*f) >= window) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type forwardRig struct {
	*rig
	fw    *memForwards
	ops   *memOps
	clock time.Time
}

func newForwardRig(t *testing.T) *forwardRig {
	t.Helper()
	g, oo, ops := optOutRig(t)
	f := &forwardRig{rig: g, ops: ops, clock: time.Date(2026, 11, 4, 16, 0, 0, 0, time.UTC)}
	f.fw = &memForwards{memOptOuts: oo, now: func() time.Time { return f.clock }, forwarded: map[string]*time.Time{},
		through: map[string]int{}, pending: map[string]bool{}}
	g.r.OptOuts = f.fw
	g.r.Now = func() time.Time { return f.clock }
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	f.say(t, "STOP")
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffOptedOut {
		t.Fatalf("opt-out notice: %+v", ops.notices)
	}
	return f
}

// say records the message (as the webhook does) and routes it; it fails the
// test if anything is sent to the number.
func (f *forwardRig) say(t *testing.T, text string) {
	t.Helper()
	before := f.ad.calls
	f.fw.mu.Lock()
	f.fw.inbound = append(f.fw.inbound, ThreadMessage{Direction: "inbound", Text: text, At: f.clock})
	f.fw.mu.Unlock()
	in := Inbound{Channel: IMessage, ProviderMessageID: text + f.clock.String(), From: me, Text: text, ReceivedAt: f.clock}
	if err := f.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatalf("say %q: %v", text, err)
	}
	if f.ad.calls != before {
		t.Fatalf("%q: %d message(s) sent to a number that opted out", text, f.ad.calls-before)
	}
}

func (f *forwardRig) forwards() []HandoffNotice {
	var out []HandoffNotice
	for _, n := range f.ops.notices {
		if n.Reason == HandoffOptedOutMessage {
			out = append(out, n)
		}
	}
	return out
}

func threadTexts(n HandoffNotice) []string {
	var out []string
	for _, m := range n.Thread {
		out = append(out, m.Text)
	}
	return out
}

func TestOptedOutUrgency(t *testing.T) {
	urgent := map[string]string{
		"I feel unsafe":                         "safety",
		"the supporter is harassing me":         "safety",
		"he hurt my arm":                        "safety",
		"someone is following me":               "safety",
		"my wallet was stolen":                  "theft or damage",
		"she broke my vase":                     "theft or damage",
		"I was charged twice":                   "charge dispute",
		"I want a refund":                       "charge dispute",
		"I'll dispute this with my bank":        "charge dispute",
		"can I talk to a human":                 "asked for a person",
		"please call me":                        "asked for a person",
		"HELP":                                  "asked for a person",
		"email me at the address on my account": "asked for a person",
		"I'm calling my lawyer":                 "legal or medical",
	}
	for s, why := range urgent {
		if u, got := OptedOutUrgency(s); !u || got != why {
			t.Errorf("%q: urgent=%t reason=%q, want %q", s, u, got, why)
		}
	}
	for _, s := range []string{"thanks", "where is my laundry?", "ok", "can you bring it at 5 instead", "YES", "is she close?"} {
		if u, why := OptedOutUrgency(s); u {
			t.Errorf("%q must not be urgent (%s)", s, why)
		}
	}
}

// The person stopped texts, then wrote: ops get the words, the last four
// digits, the account and their open tasks. Nothing goes back to the number.
func TestOptedOutMessageReachesOps(t *testing.T) {
	f := newForwardRig(t)
	f.say(t, "tell Sam I'm running late")
	fw := f.forwards()
	if len(fw) != 1 {
		t.Fatalf("forwards %d", len(fw))
	}
	n := fw[0]
	if !n.OptedOut || n.Urgent || n.UserID != "user-1" || n.PhoneHint != "•••• 4567" {
		t.Fatalf("notice %+v", n)
	}
	if got := threadTexts(n); len(got) != 1 || got[0] != "tell Sam I'm running late" {
		t.Fatalf("thread %v", got)
	}
	if len(n.OpenTasks) != 1 || n.OpenTasks[0].ID != "task-2" {
		t.Fatalf("open tasks %+v", n.OpenTasks)
	}
	if strings.Contains(n.PhoneHint, "555123") {
		t.Fatal("full number in the notice")
	}
	// Not acted on: no relay, no draft, no state change.
	if len(f.booker.resolved) != 0 || f.booker.posts != 0 || f.state() != StateIdle {
		t.Fatalf("acted on: resolved=%v posts=%d state=%s", f.booker.resolved, f.booker.posts, f.state())
	}
	// START still turns texts back on, and is not forwarded.
	f.ad.calls = 0
	if err := f.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: "start-1", From: me, Text: "START", ReceivedAt: f.clock}); err != nil {
		t.Fatal(err)
	}
	if f.ad.calls != 1 || len(f.forwards()) != 1 {
		t.Fatalf("START: sends %d forwards %d", f.ad.calls, len(f.forwards()))
	}
}

// At most one ordinary alert per 10 minutes; the rest wait and go together.
func TestOptedOutForwardsAreRateLimited(t *testing.T) {
	f := newForwardRig(t)
	f.say(t, "hello?")
	f.clock = f.clock.Add(2 * time.Minute)
	f.say(t, "is anyone there")
	f.clock = f.clock.Add(3 * time.Minute)
	f.say(t, "where is my laundry")
	if n := len(f.forwards()); n != 1 {
		t.Fatalf("forwards %d within the window, want 1", n)
	}
	// The sweep before the window passes: nothing.
	f.clock = f.clock.Add(4 * time.Minute) // 9 min after the first
	sweep(t, f)
	if n := len(f.forwards()); n != 1 {
		t.Fatalf("swept early: %d", n)
	}
	// After it: one alert with both waiting messages.
	f.clock = f.clock.Add(2 * time.Minute)
	sweep(t, f)
	fw := f.forwards()
	if len(fw) != 2 || strings.Join(threadTexts(fw[1]), "|") != "is anyone there|where is my laundry" {
		t.Fatalf("batch %+v", fw)
	}
	// Nothing left: the sweep sends nothing more.
	f.clock = f.clock.Add(20 * time.Minute)
	sweep(t, f)
	if n := len(f.forwards()); n != 2 {
		t.Fatalf("forwards %d", n)
	}
	// A message after the window goes at once.
	f.say(t, "thanks")
	if n := len(f.forwards()); n != 3 {
		t.Fatalf("forwards %d", n)
	}
}

// Urgent always goes through, inside the window, marked, with what waited.
func TestOptedOutUrgentSkipsTheWindow(t *testing.T) {
	f := newForwardRig(t)
	f.say(t, "hi")
	f.clock = f.clock.Add(time.Minute)
	f.say(t, "the bag is by the door")
	f.clock = f.clock.Add(time.Minute)
	f.say(t, "I feel unsafe, he won't leave")
	fw := f.forwards()
	if len(fw) != 2 {
		t.Fatalf("forwards %d, want the urgent one at once", len(fw))
	}
	u := fw[1]
	if !u.Urgent || u.UrgentReason != "safety" {
		t.Fatalf("urgent notice %+v", u)
	}
	if strings.Join(threadTexts(u), "|") != "the bag is by the door|I feel unsafe, he won't leave" {
		t.Fatalf("urgent thread %v", threadTexts(u))
	}
	f.clock = f.clock.Add(time.Minute)
	f.say(t, "can someone call me")
	if fw := f.forwards(); len(fw) != 3 || !fw[2].Urgent || fw[2].UrgentReason != "asked for a person" {
		t.Fatalf("human request: %+v", fw)
	}
}

// Ops unreachable: the messages wait, and the sweep sends them later.
func TestOptedOutForwardRetriedWhenOpsUnreachable(t *testing.T) {
	f := newForwardRig(t)
	f.ops.err = errors.New("postmark down")
	f.say(t, "my order never arrived")
	f.ops.err = nil
	if n := len(f.forwards()); n != 0 {
		t.Fatalf("forwards %d", n)
	}
	sweep(t, f)
	fw := f.forwards()
	if len(fw) != 1 || !fw[0].Urgent || threadTexts(fw[0])[0] != "my order never arrived" {
		t.Fatalf("retry %+v", fw)
	}
}

// Without the batching store every message is still forwarded.
func TestOptedOutForwardWithoutBatchingStore(t *testing.T) {
	g, _, ops := optOutRig(t)
	g.say(t, "STOP")
	before := g.ad.calls
	g.say(t, "hello")
	g.say(t, "hello again")
	if g.ad.calls != before {
		t.Fatal("sent to an opted-out number")
	}
	var n int
	for _, x := range ops.notices {
		if x.Reason == HandoffOptedOutMessage {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("forwards %d", n)
	}
}

func sweep(t *testing.T, f *forwardRig) {
	t.Helper()
	ids, err := f.fw.WaitingOptedOutForwards(context.Background(), OptedOutForwardWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, iid := range ids {
		if err := f.r.FlushOptedOut(context.Background(), Identity{ID: iid, Channel: IMessage, Address: me, UserID: "user-1"}); err != nil {
			t.Fatal(err)
		}
	}
}

// Phoenix: one span per forward, counts and labels, never the number or
// the words.
func TestOptedOutForwardSpan(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	stop := tracing.StartWithExporter(exp)
	defer func() { _ = stop(context.Background()) }()
	f := newForwardRig(t)
	f.say(t, "I was charged twice, call me at 212-555-0199")
	f.clock = f.clock.Add(time.Minute)
	f.say(t, "ok")
	var got []map[string]string
	for _, s := range exp.GetSpans() {
		if s.Name != "channel.opted_out_forward" {
			continue
		}
		a := map[string]string{}
		for _, kv := range s.Attributes {
			v := kv.Value.Emit()
			a[string(kv.Key)] = v
			if kv.Key == "session.id" {
				continue // the in-memory store spells identity ids "id-<number>"; production's are uuids
			}
			for _, bad := range []string{strings.Trim(me, "+"), "4567", "555-0199", "charged twice"} {
				if strings.Contains(v, bad) {
					t.Errorf("span attribute %s carries %q: %s", kv.Key, bad, v)
				}
			}
		}
		got = append(got, a)
	}
	if len(got) != 2 {
		t.Fatalf("forward spans %d", len(got))
	}
	if got[0]["hora.forward.outcome"] != "forwarded" || got[0]["hora.forward.urgent"] != "true" || got[0]["hora.forward.reason"] != "charge dispute" {
		t.Fatalf("first span %v", got[0])
	}
	if got[1]["hora.forward.outcome"] != "batched" {
		t.Fatalf("second span %v", got[1])
	}
	// The sweep's forward, with no message above it, is its own root span.
	f.clock = f.clock.Add(OptedOutForwardWindow)
	sweep(t, f)
	var swept int
	for _, s := range exp.GetSpans() {
		if s.Name == "channel.opted_out_forward" && !s.Parent.IsValid() {
			for _, kv := range s.Attributes {
				if kv.Key == "hora.forward.sweep" && kv.Value.AsBool() {
					swept++
				}
			}
		}
	}
	if swept != 1 {
		t.Fatalf("sweep forward spans %d", swept)
	}
}

// Against Postgres: the window, the batch, keyword messages left out, the
// requeue, the sweep's list, and the reset at START.
func TestPGOptedOutForwards(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	record := func(pid, text string) {
		t.Helper()
		if _, err := s.RecordInbound(ctx, Inbound{Channel: IMessage, ProviderMessageID: pid, From: me, Text: text, ReceivedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	record("p0", "STOP")
	id, newly, err := s.MarkOptedOut(ctx, IMessage, me)
	if err != nil || !newly {
		t.Fatalf("mark: %v %t", err, newly)
	}
	record("p1", "stop")
	record("p2", "hello?")
	b, err := s.TakeOptedOutBatch(ctx, id.ID, false, OptedOutForwardWindow)
	if err != nil || !b.Due || len(b.Messages) != 1 || b.Messages[0].Text != "hello?" {
		t.Fatalf("first: %+v %v", b, err)
	}
	record("p3", "anyone?")
	if b2, err := s.TakeOptedOutBatch(ctx, id.ID, false, OptedOutForwardWindow); err != nil || b2.Due {
		t.Fatalf("inside the window: %+v %v", b2, err)
	}
	if ids, _ := s.WaitingOptedOutForwards(ctx, OptedOutForwardWindow); len(ids) != 0 {
		t.Fatalf("waiting before the window passed: %v", ids)
	}
	if ids, _ := s.WaitingOptedOutForwards(ctx, 0); len(ids) != 1 || ids[0] != id.ID {
		t.Fatalf("waiting with the window passed: %v", ids)
	}
	// Urgent: at once, with what waited.
	record("p4", "I feel unsafe")
	u, err := s.TakeOptedOutBatch(ctx, id.ID, true, OptedOutForwardWindow)
	if err != nil || !u.Due || len(u.Messages) != 2 || u.Messages[0].Text != "anyone?" || u.Messages[1].Text != "I feel unsafe" {
		t.Fatalf("urgent: %+v %v", u, err)
	}
	if ids, _ := s.WaitingOptedOutForwards(ctx, 0); len(ids) != 0 {
		t.Fatalf("still waiting after the urgent forward: %v", ids)
	}
	// It reached no one: put back, due at the sweep's next pass (urgent),
	// and taken again whole.
	u.Urgent = true
	if err := s.RequeueOptedOutBatch(ctx, u); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.WaitingOptedOutForwards(ctx, OptedOutForwardWindow); len(ids) != 1 {
		t.Fatalf("requeued but not waiting: %v", ids)
	}
	again, err := s.TakeOptedOutBatch(ctx, id.ID, true, OptedOutForwardWindow)
	if err != nil || len(again.Messages) != 2 {
		t.Fatalf("after requeue: %+v %v", again, err)
	}
	// START clears it all; a later opt-out starts fresh.
	if cleared, err := s.ClearOptedOut(ctx, IMessage, me); err != nil || !cleared {
		t.Fatalf("clear: %v", err)
	}
	if b, err := s.TakeOptedOutBatch(ctx, id.ID, true, OptedOutForwardWindow); err != nil || b.Due {
		t.Fatalf("after START: %+v %v", b, err)
	}
	var f, th, p *time.Time
	if err := db.QueryRow(`SELECT opted_out_forwarded_at, opted_out_forwarded_through, opted_out_pending_since FROM channel_identities WHERE id = $1`, id.ID).Scan(&f, &th, &p); err != nil {
		t.Fatal(err)
	}
	if f != nil || th != nil || p != nil {
		t.Fatalf("not reset at START: %v %v %v", f, th, p)
	}
}

// A failed urgent forward inside a window is retried at the sweep's next
// pass, not 10 minutes later.
func TestOptedOutUrgentRetryIgnoresTheWindow(t *testing.T) {
	f := newForwardRig(t)
	f.say(t, "hi")
	f.clock = f.clock.Add(time.Minute)
	f.ops.err = errors.New("postmark down")
	f.say(t, "he hit me")
	f.ops.err = nil
	f.clock = f.clock.Add(time.Minute) // the next sweep pass
	sweep(t, f)
	fw := f.forwards()
	if len(fw) != 2 || !fw[1].Urgent || threadTexts(fw[1])[0] != "he hit me" {
		t.Fatalf("urgent retry: %+v", fw)
	}
}
