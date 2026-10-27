package channel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

var (
	laundryD = llm.TaskDraft{Title: "Laundry pickup", Category: "laundry", Location1: "12 Bedford Ave", DurationMinutes: 30}
	groceryD = llm.TaskDraft{Title: "Grocery run", Category: "grocery", Location1: "Whole Foods on 3rd", DurationMinutes: 60}
	returnD  = llm.TaskDraft{Title: "Return a package", Category: "delivery", Location1: "UPS on 5th", DurationMinutes: 20}
)

func multiRig(t *testing.T) *rig {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Splitter = fakeSplitter{}
	g.parser.byText = map[string]llm.TaskDraft{
		"pick up my laundry": laundryD, "get groceries with $40": groceryD, "return a package": returnD,
	}
	return g
}

func TestOneMessageSeveralErrandsOneSummaryOneYes(t *testing.T) {
	g := multiRig(t)
	got := g.say(t, "pick up my laundry and then get groceries with $40 and then return a package")
	for _, want := range []string{"1. Laundry pickup", "2. Grocery run", "3. Return a package", "Total estimate: $76", "Reply YES to book all 3"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if g.state() != StateAwaitingConfirm || g.booker.posts != 0 {
		t.Fatalf("state %s posts %d", g.state(), g.booker.posts)
	}
	got = g.say(t, "YES")
	if g.booker.posts != 3 || strings.Join(g.booker.posted, ",") != "Laundry pickup,Grocery run,Return a package" {
		t.Fatalf("posted %v", g.booker.posted)
	}
	if !strings.Contains(got, "Booked all 3") || g.state() != StateIdle {
		t.Fatalf("%q state %s", got, g.state())
	}
}

func TestSingleErrandSummaryUnchanged(t *testing.T) {
	g := multiRig(t)
	got := g.say(t, "pick up my laundry")
	if strings.Contains(got, "1. ") || strings.Contains(got, "Total") || !strings.Contains(got, "Reply YES to book,") {
		t.Fatalf("single errand must keep the plain summary:\n%s", got)
	}
}

// The first errand that does not book stops the run; it and the ones after
// it stay pending — never the ones already booked — and the reply says how
// far it got.
func TestPartialFailureKeepsOnlyTheUnbooked(t *testing.T) {
	g := multiRig(t)
	g.say(t, "pick up my laundry and then get groceries with $40 and then return a package")
	g.booker.results = []PostResult{
		{Outcome: PostBooked, TaskID: "t1"},
		{Outcome: PostDeclined, Message: "Your card was declined."},
	}
	got := g.say(t, "YES")
	if g.booker.posts != 2 {
		t.Fatalf("must stop at the first failure; posted %v", g.booker.posted)
	}
	if !strings.HasPrefix(got, "Booked 1 of 3 so far. Your card was declined.") || g.state() != StateAwaitingConfirm {
		t.Fatalf("%q state %s", got, g.state())
	}
	// A later YES retries exactly the two that did not book.
	g.booker.results = nil
	g.booker.result = PostResult{Outcome: PostBooked, TaskID: "t"}
	got = g.say(t, "YES")
	if strings.Join(g.booker.posted, ",") != "Laundry pickup,Grocery run,Grocery run,Return a package" {
		t.Fatalf("posted %v", g.booker.posted)
	}
	if !strings.Contains(got, "Booked all 2") || g.state() != StateIdle {
		t.Fatalf("%q state %s", got, g.state())
	}
}

func TestUnparsedErrandIsSaidNotDropped(t *testing.T) {
	g := multiRig(t)
	got := g.say(t, "pick up my laundry and then do the thing with the stuff")
	if !strings.Contains(got, "I couldn't work out \u201cdo the thing with the stuff\u201d") || !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("%s", got)
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("only the parsed errand books; posts %d", g.booker.posts)
	}
}

func TestSplitterFailureStillDrafts(t *testing.T) {
	g := multiRig(t)
	g.r.Splitter = fakeSplitter{err: errors.New("model down")}
	g.parser.byText = nil
	g.parser.draft = laundryD
	if got := g.say(t, "pick up my laundry"); !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("%s", got)
	}
}

func TestTooManyErrandsIsRefusedBeforeParsing(t *testing.T) {
	g := multiRig(t)
	got := g.say(t, strings.Repeat("pick up my laundry and then ", llm.MaxErrands)+"get groceries")
	if !strings.Contains(got, "more than I can book in one go") || len(g.parser.sources) != 0 || g.state() != StateIdle {
		t.Fatalf("%q parsed %d state %s", got, len(g.parser.sources), g.state())
	}
}

// A first-timer with several errands is asked their name once, for all.
func TestFirstTimerSeveralErrands(t *testing.T) {
	g := multiRig(t)
	g.ids = newMemIdentities()
	g.r.Identities = g.ids
	g.convs.ids = g.ids
	got := g.say(t, "pick up my laundry and then get groceries with $40")
	if !strings.Contains(got, "Got it: 2 errands.") || g.state() != StateAwaitingName {
		t.Fatalf("%q state %s", got, g.state())
	}
}

// Older stored drafts (one errand, no "more") still decode and still book.
func TestPendingDraftBackwardCompatible(t *testing.T) {
	p := &PendingDraft{Draft: laundryD, QuoteCents: 1200, QuoteMinutes: 30}
	if len(p.Items()) != 1 || p.TotalCents() != 1200 || p.Headline() != "Laundry pickup" {
		t.Fatalf("%+v", p)
	}
	p.SetItems([]DraftItem{{Draft: groceryD, QuoteCents: 1}, {Draft: returnD, QuoteCents: 2}})
	if p.Draft.Title != "Grocery run" || len(p.More) != 1 || p.TotalCents() != 3 || p.Headline() != "2 errands" {
		t.Fatalf("%+v", p)
	}
}

// A 3DS outcome means the task already exists, parked until the bank
// confirms. That item must never be re-booked: only the ones after it stay
// pending, and a single 3DS item leaves nothing pending.
func TestThreeDSItemIsSpentNotRetried(t *testing.T) {
	g := multiRig(t)
	g.say(t, "pick up my laundry and then get groceries with $40")
	g.booker.results = []PostResult{{Outcome: PostNeeds3DS, TaskID: "t1", Message: "bank check"}}
	g.say(t, "YES")
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != StateHandoff || c.Pending == nil || len(c.Pending.Items()) != 1 || c.Pending.Draft.Title != "Grocery run" {
		t.Fatalf("state %s pending %+v", c.State, c.Pending)
	}

	g = multiRig(t)
	g.say(t, "pick up my laundry")
	g.booker.results = []PostResult{{Outcome: PostNeeds3DS, TaskID: "t1"}}
	g.say(t, "YES")
	c, err = g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != StateHandoff || c.Pending != nil {
		t.Fatalf("a single 3DS item must leave nothing to re-book: %+v", c.Pending)
	}
	if err := g.r.Release(context.Background(), Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(g.ad.last(), "Reply YES to book") {
		t.Fatal("release must not offer to book the task that already exists")
	}
}
