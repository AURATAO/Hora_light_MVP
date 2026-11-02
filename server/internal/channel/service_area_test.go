package channel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

const jerseyCity = "30 Hudson St, Jersey City"

func outsideIf(place string) func(llm.TaskDraft) AreaCheck {
	return func(d llm.TaskDraft) AreaCheck {
		if d.Location1 == place || d.Location2 == place {
			return AreaCheck{Outcome: AreaOutside, Stop: place}
		}
		return AreaCheck{Outcome: AreaInside}
	}
}

func (g *rig) pending(t *testing.T) *PendingDraft {
	t.Helper()
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	return c.Pending
}

func TestWelcomeSaysFiveBoroughs(t *testing.T) {
	for _, want := range []string{"five boroughs", "Manhattan", "Brooklyn", "Queens", "the Bronx", "Staten Island"} {
		if !strings.Contains(CopyWelcome, want) {
			t.Errorf("CopyWelcome lacks %q", want)
		}
	}
}

func TestOutOfAreaGetsFixedCopyAndNoDraft(t *testing.T) {
	for _, draft := range []llm.TaskDraft{
		{Title: "Parcel", Category: "delivery", Location1: jerseyCity},
		{Title: "Parcel", Category: "delivery", Location1: "12 Bedford Ave", Location2: jerseyCity}, // one stop of two
	} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.booker.area = outsideIf(jerseyCity)
		g.parser.draft = draft
		if got := g.say(t, "take a parcel to Jersey City"); got != CopyOutOfArea {
			t.Fatalf("got %q", got)
		}
		if g.state() != StateIdle || g.pending(t) != nil {
			t.Fatalf("state %s pending %+v", g.state(), g.pending(t))
		}
		// A YES has nothing to book.
		g.say(t, "yes")
		if g.booker.posts != 0 {
			t.Fatalf("posted %d", g.booker.posts)
		}
	}
}

// An out-of-area errand from a stranger never reaches the name question, the
// setup link or a card.
func TestOutOfAreaStrangerGetsNoLink(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.booker.area = outsideIf(laundry.Location1)
	if got := g.say(t, "pick up my laundry"); got != CopyOutOfArea {
		t.Fatalf("got %q", got)
	}
	g.say(t, "YES")
	if len(g.ids.tokens) != 0 || g.booker.posts != 0 {
		t.Fatalf("tokens %d posts %d", len(g.ids.tokens), g.booker.posts)
	}
}

// A correction that moves the errand out ends the draft shown before it.
func TestCorrectionOutOfAreaDropsTheDraft(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.area = outsideIf("Hoboken")
	if got := g.say(t, "laundry pickup"); !strings.Contains(got, "Reply YES") {
		t.Fatalf("got %q", got)
	}
	g.parser.draft.Location1 = "Hoboken"
	if got := g.say(t, "actually make it Hoboken"); got != CopyOutOfArea {
		t.Fatalf("got %q", got)
	}
	if g.pending(t) != nil || g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
}

func TestUnclearAddressAsksAndBooksOnlyOnceClear(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1 = "the market"
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		if d.Location1 == "the market" {
			return AreaCheck{Outcome: AreaUnclear, Stop: d.Location1}
		}
		return AreaCheck{Outcome: AreaInside}
	}
	want := "I couldn't find \"the market\" exactly"
	if got := g.say(t, "pick up laundry at the market"); !strings.Contains(got, want) {
		t.Fatalf("got %q", got)
	}
	// No YES books it.
	if got := g.say(t, "yes"); !strings.Contains(got, want) || g.booker.posts != 0 {
		t.Fatalf("yes: %q, posts %d", got, g.booker.posts)
	}
	g.parser.draft.Location1 = "12 Bedford Ave, Brooklyn"
	if got := g.say(t, "12 Bedford Ave, Brooklyn"); !strings.Contains(got, "Reply YES") {
		t.Fatalf("got %q", got)
	}
	if got := g.say(t, "yes"); got != CopyBooked || g.booker.posts != 1 {
		t.Fatalf("got %q, posts %d", got, g.booker.posts)
	}
}

func TestAreaCheckDownBooksNothing(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.area = func(llm.TaskDraft) AreaCheck { return AreaCheck{Outcome: AreaUnavailable} }
	if got := g.say(t, "laundry pickup"); got != CopyAreaCheckDown {
		t.Fatalf("got %q", got)
	}
	g.say(t, "yes")
	if g.booker.posts != 0 || g.pending(t) != nil {
		t.Fatalf("posts %d", g.booker.posts)
	}
}

// A draft that names no place yet is asked where, not refused.
func TestNoPlaceYetIsNotChecked(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1 = ""
	g.booker.area = func(llm.TaskDraft) AreaCheck { t.Fatal("nothing to check"); return AreaCheck{} }
	if got := g.say(t, "laundry pickup"); !strings.Contains(got, "Where should the supporter go") {
		t.Fatalf("got %q", got)
	}
}

func TestCheckAreaAcrossErrands(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	items := []DraftItem{{Draft: llm.TaskDraft{Location1: "unclear"}}, {Draft: llm.TaskDraft{Location1: "down"}}, {Draft: llm.TaskDraft{Location1: "out"}}}
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		switch d.Location1 {
		case "unclear":
			return AreaCheck{Outcome: AreaUnclear, Stop: "unclear"}
		case "down":
			return AreaCheck{Outcome: AreaUnavailable}
		case "out":
			return AreaCheck{Outcome: AreaOutside, Stop: "out"}
		}
		return AreaCheck{Outcome: AreaInside}
	}
	ctx := context.Background()
	if got, _ := g.r.checkArea(ctx, items, nil); got.Outcome != AreaOutside {
		t.Errorf("%+v", got)
	}
	if got, _ := g.r.checkArea(ctx, items[:2], nil); got.Outcome != AreaUnavailable {
		t.Errorf("%+v", got)
	}
	if got, _ := g.r.checkArea(ctx, items[:1], nil); got.Outcome != AreaUnclear || got.Stop != "unclear" {
		t.Errorf("%+v", got)
	}
}

// The second lock: a draft that passed (or predates the check) refused by
// createTask at the post. No retry YES.
func TestPostRefusedOutOfAreaDropsTheDraft(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.result = PostResult{Outcome: PostOutOfArea}
	g.say(t, "laundry pickup")
	if got := g.say(t, "yes"); got != CopyOutOfArea {
		t.Fatalf("got %q", got)
	}
	if g.state() != StateIdle || g.pending(t) != nil {
		t.Fatalf("state %s", g.state())
	}
}

func TestPostRefusedUnclearAsksForTheAddress(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.result = PostResult{Outcome: PostAddressUnclear, Message: "12 Bedford Ave"}
	g.say(t, "laundry pickup")
	if got := g.say(t, "yes"); !strings.Contains(got, "\"12 Bedford Ave\"") {
		t.Fatalf("got %q", got)
	}
	if got := g.say(t, "yes"); !strings.Contains(got, "couldn't find") || g.booker.posts != 1 {
		t.Fatalf("got %q, posts %d", got, g.booker.posts)
	}
}

// An out-of-area errand typed while a supporter's question waits does not
// disturb the question.
func TestOutOfAreaKeepsAnArmedApproval(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.arm(t, "ext-1")
	conv, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	in := Inbound{Channel: IMessage, From: me}
	if err := g.r.refuseOutOfArea(context.Background(), in, conv); err != nil {
		t.Fatal(err)
	}
	got := g.ad.last()
	if !strings.Contains(got, CopyOutOfArea) || !strings.Contains(got, approvalStillPending(conv)) || g.state() != StateAwaitingApproval {
		t.Fatalf("got %q state %s", got, g.state())
	}
}

func TestClearerAddressCopy(t *testing.T) {
	p := &PendingDraft{UnclearStop: "asdf"}
	want := `I couldn't find "asdf" exactly. Could you send the full address with the borough or ZIP code? (We run errands in all five NYC boroughs.)`
	if got := detailsQuestion(p); got != want {
		t.Fatalf("%q", got)
	}
}

// The summary shows the address the check picked, the YES books that, and a
// re-draft is checked from the person's words again.
func TestSummaryShowsTheResolvedAddress(t *testing.T) {
	const picked = "100 Washington St, New York, NY 10006"
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1 = "100 Washington St"
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		return AreaCheck{Outcome: AreaInside, Resolved: []string{picked}}
	}
	got := g.say(t, "laundry pickup at 100 Washington St")
	if !strings.Contains(got, picked) || !strings.Contains(got, "Reply YES") {
		t.Fatalf("summary: %q", got)
	}
	if loc := g.pending(t).Draft.Location1; loc != picked {
		t.Fatalf("draft keeps %q", loc)
	}
}

func TestSummaryShowsBothResolvedStops(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1, g.parser.draft.Location2 = "100 Washington St", "1 Bay St"
	g.booker.area = func(llm.TaskDraft) AreaCheck {
		return AreaCheck{Outcome: AreaInside, Resolved: []string{"100 Washington St, New York, NY 10006", "1 Bay St, Staten Island, NY 10301"}}
	}
	if got := g.say(t, "laundry from A to B"); !strings.Contains(got, "NY 10006 → 1 Bay St, Staten Island, NY 10301") {
		t.Fatalf("summary: %q", got)
	}
}

func TestWhichBoroughIsAskedThenBooked(t *testing.T) {
	const picked = "100 Broadway, Brooklyn, NY 11249"
	for _, answer := range []string{"Brooklyn", "in brooklyn please"} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.parser.draft.Location1 = "Broadway 100" // the parser keeps the old stop
		g.booker.area = func(d llm.TaskDraft) AreaCheck {
			if d.Location1 == "Broadway 100" {
				return AreaCheck{Outcome: AreaWhichBorough, Stop: d.Location1}
			}
			return AreaCheck{Outcome: AreaInside, Resolved: []string{picked}}
		}
		if got := g.say(t, "laundry pickup at Broadway 100"); got != "Which borough is Broadway 100 in?" {
			t.Fatalf("got %q", got)
		}
		// Nothing to say yes to yet.
		if got := g.say(t, "yes"); got != "Which borough is Broadway 100 in?" || g.booker.posts != 0 {
			t.Fatalf("yes: %q, posts %d", got, g.booker.posts)
		}
		if got := g.say(t, answer); !strings.Contains(got, picked) || !strings.Contains(got, "Reply YES") {
			t.Fatalf("%q: got %q", answer, got)
		}
		if got := g.say(t, "yes"); got != CopyBooked || g.booker.posts != 1 {
			t.Fatalf("got %q, posts %d", got, g.booker.posts)
		}
	}
}

// An answer the parser read as the place itself still joins the address.
func TestApplyBorough(t *testing.T) {
	items := []DraftItem{{Draft: llm.TaskDraft{Location1: "Queens", Location2: "1 Bay St"}}}
	applyBorough(items, "Broadway 100", "Queens")
	if d := items[0].Draft; d.Location1 != "Broadway 100, Queens" || d.Location2 != "1 Bay St" {
		t.Fatalf("%+v", d)
	}
	items = []DraftItem{{Draft: llm.TaskDraft{Location1: "Broadway 100"}}}
	applyBorough(items, "Broadway 100", "not sure")
	if items[0].Draft.Location1 != "Broadway 100" {
		t.Fatalf("%+v", items[0].Draft)
	}
}

// resolvingBooker answers every stop inside, resolved, and records what it was
// asked.
func resolvingBooker(g *rig) *[]llm.TaskDraft {
	var asked []llm.TaskDraft
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		asked = append(asked, d)
		var resolved []string
		for _, s := range draftStops(&d) {
			resolved = append(resolved, *s+", New York, NY 10006")
		}
		return AreaCheck{Outcome: AreaInside, Resolved: resolved}
	}
	return &asked
}

func TestRedraftKeepsACheckedStop(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1 = "100 Washington St"
	asked := resolvingBooker(g)
	g.say(t, "laundry pickup at 100 Washington St")
	// A correction that leaves the address alone: no lookup, same address shown.
	if got := g.say(t, "make it 4pm"); !strings.Contains(got, "100 Washington St, New York, NY 10006") {
		t.Fatalf("got %q", got)
	}
	// The same words, spaced and cased differently.
	g.parser.draft.Location1 = "100  washington st"
	g.say(t, "make it 5pm")
	if len(*asked) != 1 {
		t.Fatalf("%d checks: %+v", len(*asked), *asked)
	}
	if g.pending(t).Draft.Location1 != "100 Washington St, New York, NY 10006" {
		t.Fatalf("%+v", g.pending(t).Draft)
	}
	// A new address is checked.
	g.parser.draft.Location1 = "1 Bay St"
	g.say(t, "actually 1 Bay St")
	if len(*asked) != 2 {
		t.Fatalf("%d checks", len(*asked))
	}
	if got := g.say(t, "yes"); got != CopyBooked {
		t.Fatalf("got %q", got)
	}
}

func TestRedraftChecksOnlyTheStopThatChanged(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1, g.parser.draft.Location2 = "100 Washington St", "1 Bay St"
	asked := resolvingBooker(g)
	g.say(t, "laundry from A to B")
	g.parser.draft.Location2 = "12 Bedford Ave"
	got := g.say(t, "drop it at 12 Bedford Ave instead")
	if len(*asked) != 2 || (*asked)[1].Location1 != "" || (*asked)[1].Location2 != "12 Bedford Ave" {
		t.Fatalf("%+v", *asked)
	}
	if !strings.Contains(got, "100 Washington St, New York, NY 10006 → 12 Bedford Ave, New York, NY 10006") {
		t.Fatalf("got %q", got)
	}
}

// Two errands at one address in one message: one lookup.
func TestSameStopInTwoErrandsIsCheckedOnce(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	asked := resolvingBooker(g)
	items := []DraftItem{{Draft: llm.TaskDraft{Location1: "1 Bay St"}}, {Draft: llm.TaskDraft{Location1: "1 Bay St"}}}
	g.r.checkArea(context.Background(), items, nil)
	if len(*asked) != 1 || items[1].Draft.Location1 != "1 Bay St, New York, NY 10006" {
		t.Fatalf("%+v %+v", *asked, items)
	}
}

// Only a pass is kept: a stop that was unclear is asked about again.
func TestUnclearStopIsCheckedAgain(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft.Location1 = "the market"
	n := 0
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		n++
		return AreaCheck{Outcome: AreaUnclear, Stop: d.Location1}
	}
	g.say(t, "laundry at the market")
	g.say(t, "tomorrow please")
	if n != 2 {
		t.Fatalf("%d checks", n)
	}
}

// createTask refusing at the post outranks what the draft remembered.
func TestRefusalAtThePostForgetsTheStop(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	asked := resolvingBooker(g)
	g.booker.result = PostResult{Outcome: PostAddressUnclear, Message: "12 Bedford Ave"}
	g.say(t, "laundry pickup")
	g.say(t, "yes")
	if g.pending(t).AreaStops != nil {
		t.Fatalf("%+v", g.pending(t).AreaStops)
	}
	g.say(t, "12 Bedford Ave please")
	if len(*asked) != 2 {
		t.Fatalf("%d checks", len(*asked))
	}
}

// The cache rides in the stored draft, or the next message would not have it.
func TestAreaStopsSurviveStorage(t *testing.T) {
	b, err := json.Marshal(PendingDraft{AreaStops: map[string]string{"1 bay st": "1 Bay St, Staten Island, NY 10301"}})
	if err != nil {
		t.Fatal(err)
	}
	var p PendingDraft
	if err := json.Unmarshal(b, &p); err != nil || p.AreaStops["1 bay st"] == "" {
		t.Fatalf("%+v %v", p, err)
	}
}
