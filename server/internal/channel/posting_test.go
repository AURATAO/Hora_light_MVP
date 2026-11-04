package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Audit 2026-10-30: gap A (a stale YES books a price nobody re-agreed to)
// and gap B (a booking in flight strands the thread and blocks safety
// messages). posting.go.

func (g *rig) conv(t *testing.T) Conversation {
	t.Helper()
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// edit changes the stored conversation in place (a row edited under the
// router, as time passing or a crash would leave it).
func (g *rig) edit(f func(*Conversation)) {
	g.convs.mu.Lock()
	defer g.convs.mu.Unlock()
	c := g.convs.convs["id-"+me]
	f(&c)
	g.convs.convs["id-"+me] = c
}

func (g *rig) ageSummary(d time.Duration) {
	g.edit(func(c *Conversation) {
		p := *c.Pending
		p.SummaryShownAtUnix = time.Now().Add(-d).Unix()
		c.Pending = &p
	})
}

func linkedRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	if got := g.say(t, "laundry pickup"); !strings.Contains(got, "Estimate: $12") {
		t.Fatalf("summary %q", got)
	}
	return g
}

// ── Gap A ──────────────────────────────────────────────────────────────────

func TestYesWithinSummaryTTLBooksAtTheQuote(t *testing.T) {
	g := linkedRig(t)
	g.ageSummary(29 * time.Minute)
	if got := g.say(t, "YES"); got != CopyBooked {
		t.Fatalf("got %q", got)
	}
	if g.booker.posts != 1 || g.booker.keys[0] == "" || g.booker.ceilings[0] != 1200 {
		t.Fatalf("posts %d keys %v ceilings %v: the YES must carry its key and the quoted $12", g.booker.posts, g.booker.keys, g.booker.ceilings)
	}
}

func TestYesAfterSummaryTTLRequotes(t *testing.T) {
	g := linkedRig(t)
	key := g.conv(t).Pending.BookingKey
	g.ageSummary(31 * time.Minute)
	g.booker.price = 1500 // the rate moved in the meantime
	got := g.say(t, "YES")
	if !strings.HasPrefix(got, CopySummaryStale) || !strings.Contains(got, "Estimate: $15") || !strings.Contains(got, "Reply YES") {
		t.Fatalf("expected a fresh summary at today's price, got %q", got)
	}
	c := g.conv(t)
	if g.booker.posts != 0 || c.State != StateAwaitingConfirm || c.Pending.QuoteCents != 1500 || c.Pending.BookingKey != key {
		t.Fatalf("posts %d state %s draft %+v (key kept: the same errand)", g.booker.posts, c.State, c.Pending)
	}
	if got := g.say(t, "YES"); got != CopyBooked || g.booker.ceilings[0] != 1500 || g.booker.keys[0] != key {
		t.Fatalf("second YES: %q ceilings %v keys %v", got, g.booker.ceilings, g.booker.keys)
	}
}

func TestSummaryTTLIsConfigurable(t *testing.T) {
	g := linkedRig(t)
	g.r.SummaryTTL = time.Hour
	g.ageSummary(45 * time.Minute)
	if got := g.say(t, "YES"); got != CopyBooked {
		t.Fatalf("a 45-minute-old summary under a 1h TTL: %q", got)
	}
}

func TestDraftFromBeforeTheStampIsShownAgain(t *testing.T) {
	g := linkedRig(t)
	g.edit(func(c *Conversation) { // a draft saved by the previous build
		p := *c.Pending
		p.SummaryShownAtUnix, p.BookingKey = 0, ""
		c.Pending = &p
	})
	if got := g.say(t, "YES"); !strings.HasPrefix(got, CopySummaryStale) || g.booker.posts != 0 {
		t.Fatalf("got %q posts %d", got, g.booker.posts)
	}
	if g.conv(t).Pending.BookingKey == "" {
		t.Fatal("the summary shown again must give the errand its key")
	}
}

func TestSurgeSinceTheSummaryIsAskedAgain(t *testing.T) {
	g := linkedRig(t)
	g.booker.price = 2000 // surge started after the $12 summary
	got := g.say(t, "YES")
	if !strings.HasPrefix(got, CopyPriceChanged) || !strings.Contains(got, "Estimate: $20") || g.booker.posts != 0 {
		t.Fatalf("got %q posts %d", got, g.booker.posts)
	}
	if got := g.say(t, "YES"); got != CopyBooked || g.booker.ceilings[0] != 2000 {
		t.Fatalf("YES to the new price: %q ceilings %v", got, g.booker.ceilings)
	}
}

func TestLowerPriceAtBookingBooks(t *testing.T) {
	g := linkedRig(t)
	g.booker.price = 1000
	if got := g.say(t, "YES"); got != CopyBooked || g.booker.ceilings[0] != 1200 {
		t.Fatalf("got %q ceilings %v", got, g.booker.ceilings)
	}
}

// The price check before the post and createTask's own resolution can
// straddle the moment a surge window opens: createTask refuses, and the
// person is asked with createTask's figure.
func TestPostRefusedAboveTheQuoteIsAskedAgain(t *testing.T) {
	g := linkedRig(t)
	g.booker.results = []PostResult{{Outcome: PostPriceChanged, QuoteCents: 1800}, {Outcome: PostBooked, TaskID: "task-1"}}
	got := g.say(t, "YES")
	if !strings.HasPrefix(got, CopyPriceChanged) || !strings.Contains(got, "Estimate: $18") || g.state() != StateAwaitingConfirm {
		t.Fatalf("got %q state %s", got, g.state())
	}
	if got := g.say(t, "YES"); got != CopyBooked || len(g.booker.ceilings) != 2 || g.booker.ceilings[1] != 1800 || g.booker.keys[0] != g.booker.keys[1] {
		t.Fatalf("got %q ceilings %v keys %v", got, g.booker.ceilings, g.booker.keys)
	}
}

func TestPastScheduledTimeAsksForANewTime(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Laundry pickup", Category: "laundry", Location1: "12 Bedford Ave",
		Scheduled: true, ScheduledTime: time.Now().Add(2 * time.Hour).Format(time.RFC3339)}
	g.say(t, "laundry pickup at 3pm")
	g.edit(func(c *Conversation) { // 3pm came and went before the YES
		p := *c.Pending
		p.Draft.ScheduledTime = time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
		c.Pending = &p
	})
	got := g.say(t, "YES")
	if !strings.Contains(got, "that time has passed") || !strings.Contains(got, "Laundry pickup") || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("got %q posts %d state %s", got, g.booker.posts, g.state())
	}
	if got := g.say(t, "YES"); !strings.Contains(got, "that time has passed") || g.booker.posts != 0 {
		t.Fatalf("a second YES on a past time: %q posts %d", got, g.booker.posts)
	}
}

// Same rules for the first booking through the setup page.
func TestSetupPageFinishRequotesAStaleSummary(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	g.say(t, "Aura")
	if got := g.say(t, "YES"); !strings.Contains(got, "https://example.test/channel/setup#t=") {
		t.Fatalf("expected the link, got %q", got)
	}
	g.ageSummary(40 * time.Minute) // the page took a while
	g.ids.link(me, "user-1")
	booked, last, err := g.r.FinishSetup(context.Background(), "user-1")
	if err != nil || booked != 0 || last.Outcome != PostReconfirm || g.booker.posts != 0 {
		t.Fatalf("booked %d last %+v posts %d err %v", booked, last, g.booker.posts, err)
	}
	if got := g.ad.last(); !strings.HasPrefix(got, CopySummaryStale) || !strings.Contains(got, "Reply YES") || g.state() != StateAwaitingConfirm {
		t.Fatalf("chat %q state %s", got, g.state())
	}
	if got := g.say(t, "YES"); got != CopyBooked || g.booker.posts != 1 {
		t.Fatalf("YES after the fresh summary: %q posts %d", got, g.booker.posts)
	}
}

func TestSetupPageFinishWithAPastTimeAsksForANewOne(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.parser.draft = llm.TaskDraft{Title: "Laundry pickup", Category: "laundry", Location1: "12 Bedford Ave",
		Scheduled: true, ScheduledTime: time.Now().Add(time.Hour).Format(time.RFC3339)}
	g.say(t, "pick up my laundry at 12 Bedford Ave at 3pm")
	g.say(t, "Aura")
	g.say(t, "YES")
	g.edit(func(c *Conversation) {
		p := *c.Pending
		p.Draft.ScheduledTime = time.Now().Add(-time.Minute).Format(time.RFC3339)
		c.Pending = &p
	})
	g.ids.link(me, "user-1")
	if booked, last, err := g.r.FinishSetup(context.Background(), "user-1"); err != nil || booked != 0 || last.Outcome != PostReconfirm || g.booker.posts != 0 {
		t.Fatalf("booked %d last %+v posts %d err %v", booked, last, g.booker.posts, err)
	}
	if got := g.ad.last(); !strings.Contains(got, "that time has passed") {
		t.Fatalf("chat %q", got)
	}
}

// ── Gap B ──────────────────────────────────────────────────────────────────

// sayDuringPost sends text while the YES's post is in flight.
func (g *rig) sayDuringPost(t *testing.T, text string) {
	g.booker.during = func() {
		g.booker.during = nil
		in := Inbound{Channel: IMessage, ProviderMessageID: "mid-" + text, From: me, Text: text, ReceivedAt: time.Now()}
		if err := g.r.HandleInbound(context.Background(), in); err != nil {
			t.Error(err)
		}
	}
}

func TestUnsafeWhilePostingReachesOps(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.r.Agent = &fakeAgent{p: llm.Proposal{Action: llm.ActionEscalate, Task: -1, Escalation: llm.EscalateSafety}}
	g.sayDuringPost(t, "I feel unsafe")
	got := g.say(t, "YES")
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffAgentEscalated || ops.notices[0].Detail != "safety" {
		t.Fatalf("notices %+v", ops.notices)
	}
	texts := g.sentTexts()
	if !contains(texts, CopyHandoffWhileBooking) {
		t.Fatalf("the person was not told a teammate is coming: %q", texts)
	}
	// The booking finished into the handed-off thread: still ops', and said.
	c := g.conv(t)
	if got != CopyBookedHandedOff || c.State != StateHandoff || c.Pending != nil || g.booker.posts != 1 {
		t.Fatalf("last %q state %s pending %+v posts %d", got, c.State, c.Pending, g.booker.posts)
	}
	if err := g.r.OpsSend(context.Background(), Identity{ID: "id-" + me, Channel: IMessage, Address: me}, "I'm here, are you OK?"); err != nil {
		t.Fatalf("ops cannot answer the thread: %v", err)
	}
}

func TestHumanRequestWhilePostingReachesOps(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.booker.result = PostResult{Outcome: PostFailed}
	g.sayDuringPost(t, "I want to talk to a human")
	got := g.say(t, "YES")
	if len(ops.notices) < 1 || ops.notices[0].Reason != HandoffHumanRequested {
		t.Fatalf("notices %+v", ops.notices)
	}
	c := g.conv(t)
	if got != CopyPostNotDoneHandedOff || c.State != StateHandoff || c.Pending == nil {
		t.Fatalf("last %q state %s pending %v — a failed post keeps the draft for ops", got, c.State, c.Pending)
	}
}

func TestOtherMessagesWhilePostingWait(t *testing.T) {
	g := linkedRig(t)
	g.sayDuringPost(t, "thanks!")
	g.say(t, "YES")
	if texts := g.sentTexts(); !contains(texts, CopyBookingInFlight) || g.state() != StateIdle {
		t.Fatalf("texts %q state %s", texts, g.state())
	}
}

func TestSafetyWhilePostingWithOpsDownIsSaid(t *testing.T) {
	g := linkedRig(t)
	g.r.Ops = &memOps{err: errors.New("smtp down")}
	g.sayDuringPost(t, "I want to talk to a human")
	g.say(t, "YES")
	if texts := g.sentTexts(); !contains(texts, CopyBookingInFlight+" "+CopyHandoffFailed) || g.state() != StateIdle {
		t.Fatalf("texts %q state %s", texts, g.state())
	}
}

// stuck leaves the thread as a crash mid-post would: claimed long ago.
func (g *rig) stuck(t *testing.T, key string) {
	t.Helper()
	g.edit(func(c *Conversation) {
		p := *c.Pending
		p.BookingKey = key
		c.Pending = &p
		c.State = StatePosting
		at := time.Now().Add(-5 * time.Minute)
		c.PostingStartedAt = &at
	})
}

func (g *rig) holdApproval(t *testing.T) {
	t.Helper()
	d := DeferredApproval{UserID: "user-1", TaskID: "task-2", TaskTitle: "Bagel run", SupporterName: "Sam", ExtensionID: "ext-9",
		Type: evTimeRequested, Approval: PendingApproval{ID: "ext-9", Kind: "time", Minutes: 15}, TimeoutMinutes: 10, AtUnix: time.Now().Unix()}
	if err := g.convs.HoldApproval(context.Background(), "id-"+me, d); err != nil {
		t.Fatal(err)
	}
}

func TestStuckPostingWithTaskCreatedRecoveredByMessage(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.stuck(t, "key-1")
	g.holdApproval(t)
	g.booker.booked = map[string]string{"key-1": "task-9"}
	g.say(t, "YES") // "did it go through?" — in the only words they have
	texts := g.sentTexts()
	if !contains(texts, CopyBooked) || g.booker.posts != 0 {
		t.Fatalf("texts %q posts %d — found booked, never posted again", texts, g.booker.posts)
	}
	c := g.conv(t)
	if c.Pending != nil || c.State != StateAwaitingApproval || c.PostingStartedAt != nil {
		t.Fatalf("state %s pending %+v: the held approval question goes out next", c.State, c.Pending)
	}
	if len(ops.notices) != 0 {
		t.Fatalf("a recovered booking needs no ops: %+v", ops.notices)
	}
}

func TestStuckPostingWithNoTaskRecoveredByMessage(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.stuck(t, "key-1")
	g.holdApproval(t)
	g.say(t, "hello??")
	texts := g.sentTexts()
	if !contains(texts, CopyPostingStuck) || g.booker.posts != 0 {
		t.Fatalf("texts %q posts %d", texts, g.booker.posts)
	}
	c := g.conv(t)
	if c.State != StateAwaitingApproval || c.Pending == nil || c.Pending.BookingKey != "key-1" {
		t.Fatalf("state %s pending %+v: the draft waits (same key), the held question is released", c.State, c.Pending)
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffBookingFailed || ops.notices[0].PhoneHint != "•••• 4567" {
		t.Fatalf("notices %+v", ops.notices)
	}
}

func TestStuckPostingRetryReusesTheKey(t *testing.T) {
	g := linkedRig(t)
	g.r.Ops = &memOps{}
	g.stuck(t, "key-1")
	g.say(t, "YES") // recovered: "Reply YES to try again"
	if g.state() != StateAwaitingConfirm || g.booker.posts != 0 {
		t.Fatalf("state %s posts %d: the YES that found it stuck is not the retry", g.state(), g.booker.posts)
	}
	if got := g.say(t, "YES"); got != CopyBooked || g.booker.keys[0] != "key-1" {
		t.Fatalf("retry %q keys %v", got, g.booker.keys)
	}
}

func TestStuckPostingWithASafetyMessage(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.stuck(t, "key-1")
	got := g.say(t, "can I speak to a real person")
	if got != CopyHandoffStart || g.state() != StateHandoff {
		t.Fatalf("got %q state %s", got, g.state())
	}
	if !contains(g.sentTexts(), CopyPostingStuck) {
		t.Fatalf("the stuck booking's outcome was not said: %q", g.sentTexts())
	}
	reasons := []HandoffReason{}
	for _, n := range ops.notices {
		reasons = append(reasons, n.Reason)
	}
	if len(reasons) != 2 || reasons[0] != HandoffBookingFailed || reasons[1] != HandoffHumanRequested {
		t.Fatalf("notices %v", reasons)
	}
}

func TestSweeperRecoversAStuckPosting(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	id := Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}

	// A live post (claimed a moment ago) is left alone.
	g.stuck(t, "key-1")
	g.edit(func(c *Conversation) { at := time.Now(); c.PostingStartedAt = &at })
	if ok, err := g.r.RecoverStuckPosting(context.Background(), id, false); ok || err != nil {
		t.Fatalf("recovered a live post: %v %v", ok, err)
	}

	// Stuck, task made.
	g.edit(func(c *Conversation) { at := time.Now().Add(-4 * time.Minute); c.PostingStartedAt = &at })
	g.booker.booked = map[string]string{"key-1": "task-9"}
	if ok, err := g.r.RecoverStuckPosting(context.Background(), id, false); !ok || err != nil {
		t.Fatalf("not recovered: %v %v", ok, err)
	}
	if g.ad.last() != CopyBooked || g.state() != StateIdle || g.booker.posts != 0 || len(ops.notices) != 0 {
		t.Fatalf("last %q state %s posts %d notices %d", g.ad.last(), g.state(), g.booker.posts, len(ops.notices))
	}
	// A second sweep finds nothing.
	if ok, _ := g.r.RecoverStuckPosting(context.Background(), id, false); ok {
		t.Fatal("recovered twice")
	}

	// Stuck, no task.
	g.say(t, "laundry pickup")
	g.stuck(t, "key-2")
	if ok, err := g.r.RecoverStuckPosting(context.Background(), id, false); !ok || err != nil {
		t.Fatalf("not recovered: %v %v", ok, err)
	}
	if g.ad.last() != CopyPostingStuck || g.state() != StateAwaitingConfirm || len(ops.notices) != 1 || ops.notices[0].Reason != HandoffBookingFailed {
		t.Fatalf("last %q state %s notices %+v", g.ad.last(), g.state(), ops.notices)
	}

	// The lookup failing decides nothing: no message, the claim stays.
	g.stuck(t, "key-3")
	g.booker.bookedErr = errors.New("db down")
	sent := len(g.ad.sent)
	if ok, err := g.r.RecoverStuckPosting(context.Background(), id, false); ok || err == nil || len(g.ad.sent) != sent || g.state() != StatePosting {
		t.Fatalf("ok %v err %v sent %d state %s", ok, err, len(g.ad.sent)-sent, g.state())
	}
}

// Gap B.3/B.4 at the router: a failed post keeps the errand's key, so the
// YES that retries it is the same booking to createTask; ops are told.
func TestFailedPostRetryKeepsTheKeyAndTellsOps(t *testing.T) {
	g := linkedRig(t)
	ops := &memOps{}
	g.r.Ops = ops
	g.booker.results = []PostResult{{Outcome: PostFailed}, {Outcome: PostBooked, TaskID: "task-1"}}
	if got := g.say(t, "YES"); got != CopyPostFailed {
		t.Fatalf("got %q", got)
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffBookingFailed {
		t.Fatalf("notices %+v", ops.notices)
	}
	if got := g.say(t, "YES"); got != CopyBooked || len(g.booker.keys) != 2 || g.booker.keys[0] == "" || g.booker.keys[0] != g.booker.keys[1] {
		t.Fatalf("got %q keys %v", got, g.booker.keys)
	}
}

// A correction is a new errand: a new key.
func TestRedraftGetsANewKey(t *testing.T) {
	g := linkedRig(t)
	first := g.conv(t).Pending.BookingKey
	g.say(t, "make it 4pm")
	if second := g.conv(t).Pending.BookingKey; second == "" || second == first {
		t.Fatalf("keys %q → %q", first, second)
	}
}

func (g *rig) sentTexts() []string {
	g.ad.mu.Lock()
	defer g.ad.mu.Unlock()
	var out []string
	for _, o := range g.ad.sent {
		out = append(out, o.Text)
	}
	return out
}

// A summary put again after an approval question is a fresh showing: the
// YES to it books, however old the first showing was.
func TestSummaryPutAgainAfterAnApprovalIsFresh(t *testing.T) {
	g := linkedRig(t)
	g.arm(t, "ext-2")
	g.ageSummary(40 * time.Minute)
	if got := g.say(t, "YES"); !strings.Contains(got, CopyBackToDraft) || !strings.Contains(got, "Reply YES") {
		t.Fatalf("approval answer %q", got)
	}
	if got := g.say(t, "YES"); got != CopyBooked {
		t.Fatalf("YES to the re-shown summary: %q", got)
	}
}
