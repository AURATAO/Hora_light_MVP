package channel

import (
	"context"
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
	want := "I couldn't pin down “the market”"
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
	if got := g.r.checkArea(ctx, items); got.Outcome != AreaOutside {
		t.Errorf("%+v", got)
	}
	if got := g.r.checkArea(ctx, items[:2]); got.Outcome != AreaUnavailable {
		t.Errorf("%+v", got)
	}
	if got := g.r.checkArea(ctx, items[:1]); got.Outcome != AreaUnclear || got.Stop != "unclear" {
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
	if got := g.say(t, "yes"); !strings.Contains(got, "“12 Bedford Ave”") {
		t.Fatalf("got %q", got)
	}
	if got := g.say(t, "yes"); !strings.Contains(got, "pin down") || g.booker.posts != 1 {
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
