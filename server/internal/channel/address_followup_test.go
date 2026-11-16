package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"hora-auth/internal/channel/llm"
)

// Production 2026-11-12 ~21:56 UTC (identity 4bf78a05…): four messages, and
// the request was lost, re-asked about "x" and handed off. These tests
// replay that transcript with the parser's production outputs.

const (
	efMsg1 = "I’m in east village I need medicine"
	efMsg2 = "335 e 3 ave"
	efMsg3 = "6 street"
	efMsg4 = "335 6 st 3 ave"
	// What the address model proposes from the whole conversation, and
	// what Google makes of it (the fake area check below).
	efGuess = "335 E 6th St, New York, NY 10003"
)

// funcParser answers each source with fn (the production model's outputs).
type funcParser struct {
	mu      sync.Mutex
	fn      func(text string) (llm.TaskDraft, error)
	sources []string
}

func (f *funcParser) ParseTask(_ context.Context, _ string, text string) (llm.TaskDraft, llm.Usage, error) {
	f.mu.Lock()
	f.sources = append(f.sources, text)
	f.mu.Unlock()
	d, err := f.fn(text)
	return d, llm.Usage{Model: "fake"}, err
}

// productionParse: what Sonnet returned on 2026-11-12 for each step — an
// empty title for the address, "x" for "6 street" — and a good parse of
// the last line (any source ending in it).
func productionParse(text string) (llm.TaskDraft, error) {
	last := latestLine(text)
	switch {
	case text == efMsg1:
		return llm.TaskDraft{Title: "Pick up medicine and deliver to East Village", Category: "delivery", Location2: "East Village"}, nil
	case last == efMsg2:
		return llm.TaskDraft{}, errors.New("ParseTask: decodeDraft: draft: empty title")
	case last == efMsg3:
		return llm.TaskDraft{Title: "x", Category: "delivery", Location2: efMsg3}, nil
	case last == efMsg4:
		return llm.TaskDraft{Title: "Pick up and deliver medicine", Category: "delivery", Location2: efMsg4}, nil
	case last == efGuess:
		return llm.TaskDraft{Title: "Pick up and deliver medicine", Category: "delivery", Location2: efGuess}, nil
	}
	return llm.TaskDraft{}, errors.New("cannot parse")
}

// eastVillageArea is Google as it answered: "East Village" a neighbourhood,
// the typed addresses not placeable, the full guess placed.
func eastVillageArea(d llm.TaskDraft) AreaCheck {
	for _, s := range []string{d.Location1, d.Location2} {
		switch strings.TrimSpace(s) {
		case "":
		case "East Village":
			return AreaCheck{Outcome: AreaUnclear, Stop: s, Area: "East Village"}
		case efGuess:
		default:
			return AreaCheck{Outcome: AreaUnclear, Stop: s}
		}
	}
	var resolved []string
	for _, s := range []string{d.Location1, d.Location2} {
		if s != "" {
			resolved = append(resolved, s)
		}
	}
	return AreaCheck{Outcome: AreaInside, Resolved: resolved}
}

type fakeGuesser struct {
	mu    sync.Mutex
	fn    func(said []string, unclear, area string) string
	calls int
}

func (f *fakeGuesser) GuessAddress(_ context.Context, said []string, unclear, area string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.fn(said, unclear, area), nil
}

// sonnetLike: a guess once a street and a building number are both in the
// words (as Sonnet did in the live replay), none before.
func sonnetLike(said []string, unclear, area string) string {
	all := strings.ToLower(strings.Join(said, " "))
	if area == "East Village" && strings.Contains(all, "335") && (strings.Contains(all, "6 st") || strings.Contains(all, "6 street")) {
		return efGuess
	}
	return ""
}

type yesPurchases struct{}

func (yesPurchases) NeedsPurchase(context.Context, string) (bool, error) { return true, nil }

func newAddressRig(t *testing.T) (*rig, *funcParser, *fakeGuesser) {
	t.Helper()
	g := newRig(llm.IntentNewTask)
	fp := &funcParser{fn: productionParse}
	fg := &fakeGuesser{fn: sonnetLike}
	g.r.Parser, g.r.Addresses, g.r.Purchases = fp, fg, yesPurchases{}
	g.booker.area = eastVillageArea
	return g, fp, fg
}

func withPlaybooks(t *testing.T, g *rig) {
	t.Helper()
	pbs, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	g.r.Playbooks = pbs
}

// The four messages, with the type's questions on (production runs with
// the playbooks): medicine is asked about first, the neighbourhood is the
// area, every address answer fills the open request, "x" never shows, a
// placed guess is put to the person, and nothing hands off.
func TestProductionTranscriptAddressFollowup(t *testing.T) {
	g, fp, _ := newAddressRig(t)
	withPlaybooks(t, g)

	r1 := g.say(t, efMsg1)
	if !strings.Contains(r1, "What do you need from the pharmacy?") || !strings.Contains(r1, "can't pick up prescriptions") {
		t.Fatalf("1: %q", r1)
	}
	p := g.pending(t)
	if p == nil || p.Area != "East Village" || p.Playbook != "pharmacy" || p.Draft.Location2 != "" {
		t.Fatalf("1: pending %+v", p)
	}

	r2 := g.say(t, efMsg2)
	if want := `I couldn't find "335 e 3 ave" exactly`; !strings.Contains(r2, want) {
		t.Fatalf("2: %q", r2)
	}
	p = g.pending(t)
	if p.Draft.Title != "Pick up medicine and deliver to East Village" || p.Draft.Location2 != efMsg2 || !strings.Contains(p.Source, efMsg2) {
		t.Fatalf("2: the answer did not fill the request: %+v", p)
	}
	if p.DetailsTries != 0 {
		t.Fatalf("2: tries %d (the first answer was to the medicine question)", p.DetailsTries)
	}

	r3 := g.say(t, efMsg3)
	if strings.Contains(r3, "“x”") || r3 != "Did you mean "+efGuess+"? Reply YES or send the right address." {
		t.Fatalf("3: %q", r3)
	}
	p = g.pending(t)
	if p.Draft.Title != "Pick up medicine and deliver to East Village" || p.AddressGuess != efGuess {
		t.Fatalf("3: %+v", p)
	}

	r4 := g.say(t, efMsg4)
	if r4 != "Did you mean "+efGuess+"? Reply YES or send the right address." {
		t.Fatalf("4: %q", r4)
	}
	if g.state() == StateHandoff {
		t.Fatal("4: handed off")
	}
	if p = g.pending(t); p.DetailsTries >= MaxDetailsTries {
		t.Fatalf("4: tries %d", p.DetailsTries)
	}
	// Every answer reached the parser together with the request.
	for _, s := range fp.sources[1:] {
		if !strings.HasPrefix(s, efMsg1+"\n") {
			t.Fatalf("parsed on its own: %q", s)
		}
	}

	// YES to the guess: the request goes on at that address, bought in
	// the East Village, with no "where should they buy it?".
	r5 := g.say(t, "YES")
	p = g.pending(t)
	if p.DetailsAsked || p.Draft.Location2 != efGuess || p.Draft.Location1 != "" {
		t.Fatalf("5: %q %+v", r5, p)
	}
	if !strings.Contains(p.Draft.Description, "Where to buy: anywhere in or near the East Village") {
		t.Fatalf("5: description %q", p.Draft.Description)
	}
	if !strings.Contains(r5, "first name") {
		t.Fatalf("5: %q", r5)
	}
}

// The same four messages without the playbooks: the neighbourhood is
// still the area ("what's the exact address?", not "I couldn't find").
func TestProductionTranscriptWithoutPlaybooks(t *testing.T) {
	g, _, _ := newAddressRig(t)
	if r := g.say(t, efMsg1); r != "Got it, the East Village. What's the exact address? A building number and street is enough, like “120 E 7th St”." {
		t.Fatalf("1: %q", r)
	}
	if r := g.say(t, efMsg2); !strings.Contains(r, `I couldn't find "335 e 3 ave" exactly`) {
		t.Fatalf("2: %q", r)
	}
	if p := g.pending(t); p.Draft.Location2 != efMsg2 || p.DetailsTries != 1 {
		t.Fatalf("2: %+v (the neighbourhood was not a failed answer; this one is)", p)
	}
	if r := g.say(t, efMsg3); !strings.HasPrefix(r, "Did you mean "+efGuess) {
		t.Fatalf("3: %q", r)
	}
	if r := g.say(t, efMsg4); !strings.HasPrefix(r, "Did you mean "+efGuess) || g.state() == StateHandoff {
		t.Fatalf("4: %q state %s", r, g.state())
	}
}

// A: an answer whose parse fails fills the pending request by rule — the
// production failure at step 2, with an address question open.
func TestAddressAnswerFillsPendingWhenParseFails(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		if d.Location1 == "the market" {
			return AreaCheck{Outcome: AreaUnclear, Stop: d.Location1}
		}
		return AreaCheck{Outcome: AreaInside}
	}
	g.parser.draft.Location1 = "the market"
	if got := g.say(t, "pick up laundry at the market"); !strings.Contains(got, `I couldn't find "the market"`) {
		t.Fatalf("got %q", got)
	}
	g.parser.err = errors.New("ParseTask: decodeDraft: draft: empty title")
	got := g.say(t, "12 Bedford Ave, Brooklyn")
	p := g.pending(t)
	if !strings.Contains(got, "Reply YES") || p.Draft.Location1 != "12 Bedford Ave, Brooklyn" || p.Draft.Title != laundry.Title {
		t.Fatalf("got %q pending %+v", got, p)
	}
	if !strings.HasSuffix(p.Source, "\n12 Bedford Ave, Brooklyn") {
		t.Fatalf("source %q", p.Source)
	}
}

// A, on a summary: a correction whose parse fails keeps the request and
// says so — never "say what you need, where, and when".
func TestCorrectionParseFailureKeepsTheRequest(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	if got := g.say(t, "laundry pickup"); !strings.Contains(got, "Reply YES") {
		t.Fatalf("got %q", got)
	}
	g.parser.err = errors.New("ParseTask: decodeDraft: draft: empty title")
	got := g.say(t, "hmm make it the other one")
	if got == CopyParseFailed || !strings.Contains(got, "“"+laundry.Title+"” is still open") {
		t.Fatalf("got %q", got)
	}
	if p := g.pending(t); p == nil || p.Draft.Title != laundry.Title || g.state() != StateAwaitingConfirm {
		t.Fatalf("pending %+v state %s", p, g.state())
	}
}

// B: a placeholder title never shows; the pending title stands.
func TestPlaceholderTitleKeepsThePendingTitle(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Buy cough drops", Category: "delivery"}
	if got := g.say(t, "buy me cough drops"); !strings.Contains(got, "“Buy cough drops”") {
		t.Fatalf("got %q", got)
	}
	// The production step: "x" to the where question.
	g.parser.draft = llm.TaskDraft{Title: "x", Category: "delivery"}
	if got := g.say(t, "6 street"); strings.Contains(got, "“x”") || !strings.Contains(got, "“Buy cough drops”") {
		t.Fatalf("x: got %q", got)
	}
	// On a summary too.
	g.parser.draft = llm.TaskDraft{Title: "Buy cough drops", Category: "delivery", Location1: "12 Bedford Ave, Brooklyn"}
	if got := g.say(t, "12 Bedford Ave, Brooklyn"); !strings.Contains(got, "set aside") {
		t.Fatalf("got %q", got)
	}
	if got := g.say(t, "$10"); !strings.Contains(got, "Reply YES") {
		t.Fatalf("got %q", got)
	}
	for _, ph := range []string{"x", "-", "task", "", "N/A"} {
		g.parser.draft = llm.TaskDraft{Title: ph, Category: "delivery", Location1: "12 Bedford Ave, Brooklyn"}
		got := g.say(t, "make it 4pm")
		if p := g.pending(t); p.Draft.Title != "Buy cough drops" || !strings.Contains(got, "Buy cough drops") {
			t.Fatalf("%q: got %q, title %q", ph, got, p.Draft.Title)
		}
	}
}

// B: with nothing pending, a placeholder title is no draft — what they
// need is asked.
func TestPlaceholderTitleWithNothingPendingAsks(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "x", Category: "delivery", Location1: "6 street"}
	if got := g.say(t, "6 street"); got != CopyParseFailed {
		t.Fatalf("got %q", got)
	}
	if g.pending(t) != nil {
		t.Fatal("a draft titled x")
	}
}

func TestPlaceholderTitle(t *testing.T) {
	for _, s := range []string{"", " ", "x", "X", "xx", "-", "--", "?", "task", "Task", "N/A", "na", "none", "null", "untitled", "\"x\"", "ok"} {
		if !PlaceholderTitle(s) {
			t.Errorf("%q is a placeholder", s)
		}
	}
	for _, s := range []string{"Pick up medicine", "Buy Tylenol", "Errand", "Laundry pickup", "Get CVS items"} {
		if PlaceholderTitle(s) {
			t.Errorf("%q is a title", s)
		}
	}
}

// C: NO to the guess asks for the right address, is not a failed answer,
// and the guess is never proposed again.
func TestAddressGuessNoAsksForTheRightOne(t *testing.T) {
	g, _, fg := newAddressRig(t)
	g.say(t, efMsg1)
	g.say(t, efMsg2)
	if r := g.say(t, efMsg3); !strings.HasPrefix(r, "Did you mean") {
		t.Fatalf("3: %q", r)
	}
	tries := g.pending(t).DetailsTries
	if r := g.say(t, "no"); r != CopyAskRightAddress {
		t.Fatalf("no: %q", r)
	}
	p := g.pending(t)
	if p == nil || p.AddressGuess != "" || p.DetailsTries != tries || len(p.RejectedGuesses) != 1 {
		t.Fatalf("no: %+v", p)
	}
	before := fg.calls
	r := g.say(t, efMsg4)
	if strings.HasPrefix(r, "Did you mean") || fg.calls == before {
		t.Fatalf("re-proposed a rejected guess: %q", r)
	}
}

// C: a guess the map cannot place either is never put to the person.
func TestAddressGuessUnplacedIsNotAsked(t *testing.T) {
	g, _, fg := newAddressRig(t)
	fg.fn = func([]string, string, string) string { return "335 Nowhere St" }
	g.say(t, efMsg1)
	if r := g.say(t, efMsg2); strings.HasPrefix(r, "Did you mean") || !strings.Contains(r, "I couldn't find") {
		t.Fatalf("%q", r)
	}
}

// D: a neighbourhood is an area — not a failed answer, and a purchase
// with a drop-off in it is not asked where to buy.
func TestNeighbourhoodIsTheArea(t *testing.T) {
	g, _, _ := newAddressRig(t)
	g.r.Addresses = nil
	g.ids.link(me, "user-1")
	g.r.Parser = &funcParser{fn: func(text string) (llm.TaskDraft, error) {
		d := llm.TaskDraft{Title: "Buy Tylenol", Category: "delivery", Location2: "East Village"}
		if strings.Contains(text, "120 E 7th St") {
			d.Location2 = "120 E 7th St, New York, NY 10009"
		}
		return d, nil
	}}
	g.booker.area = func(d llm.TaskDraft) AreaCheck {
		if d.Location2 == "East Village" {
			return AreaCheck{Outcome: AreaUnclear, Stop: "East Village", Area: "East Village"}
		}
		return AreaCheck{Outcome: AreaInside, Resolved: []string{d.Location2}}
	}
	if r := g.say(t, "I'm in East Village, bring me Tylenol"); !strings.HasPrefix(r, "Got it, the East Village. What's the exact address?") {
		t.Fatalf("%q", r)
	}
	if p := g.pending(t); p.DetailsTries != 0 || p.UnclearStop != "" {
		t.Fatalf("%+v", p)
	}
	r := g.say(t, "120 E 7th St")
	if p := g.pending(t); strings.Contains(r, "Where should the supporter go") || p.DetailsAsked || !strings.Contains(r, "set aside") {
		t.Fatalf("%q %+v", r, p)
	}
}

// E: "I need medicine" is asked what first; a prescription is declined by
// the scope rule; an item named up front is not asked about.
func TestMedicineAsksWhatFirst(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	withPlaybooks(t, g)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Pick up medicine", Category: "delivery", Location2: "120 E 7th St, New York, NY 10009"}
	if r := g.say(t, "I need medicine"); !strings.Contains(r, "What do you need from the pharmacy?") {
		t.Fatalf("%q", r)
	}
	if r := g.say(t, "my prescription from the CVS"); !strings.Contains(r, "handle prescriptions") {
		t.Fatalf("prescription: %q", r)
	}

	g = newRig(llm.IntentNewTask)
	withPlaybooks(t, g)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Buy Tylenol", Category: "delivery", Location1: "CVS, 2nd Ave", Location2: "120 E 7th St, New York, NY 10009"}
	if r := g.say(t, "get me Tylenol from the CVS on 2nd Ave to 120 E 7th St"); strings.Contains(r, "What do you need from the pharmacy?") {
		t.Fatalf("asked about a named item: %q", r)
	}
}

// F: after a hand-off over the address times out, the question is asked a
// simpler way — never the one that failed — and the next answer gets one
// more try before another hand-off.
func TestHandoffTimeoutAsksTheAddressSimpler(t *testing.T) {
	g, _, _ := newAddressRig(t)
	g.r.Addresses = nil
	g.r.Ops = &memOps{}
	g.say(t, efMsg1)
	g.say(t, efMsg2)
	// No address model: two failed answers still hand off.
	if r := g.say(t, efMsg3); r != CopyHandoffStart || g.state() != StateHandoff {
		t.Fatalf("no guesser: the limit still hands off: %q %s", r, g.state())
	}
	stale := g.r.now().Add(-HandoffTimeout - 1)
	c, _ := g.convs.GetConversation(context.Background(), "id-"+me)
	c.HandoffActivityAt = &stale
	g.convs.convs["id-"+me] = c
	if ok, err := g.r.AutoRelease(context.Background(), Identity{ID: "id-" + me, Channel: IMessage, Address: me}, false); err != nil || !ok {
		t.Fatalf("release %v %v", ok, err)
	}
	r := g.ad.last()
	if !strings.HasPrefix(r, CopyHandoffTimedOut) || !strings.HasSuffix(r, CopyBackToDraft+"\n"+CopyAskAddressSimpler) || strings.Contains(r, "I couldn't find") {
		t.Fatalf("timeout: %q", r)
	}
	p := g.pending(t)
	if !p.SimplerAsk || p.DetailsTries != MaxDetailsTries-1 {
		t.Fatalf("%+v", p)
	}
	if r := g.say(t, "6th and 3rd"); r != CopyHandoffStart {
		// one more unplaceable answer: a person again
		t.Fatalf("after: %q", r)
	}
}

// AddressFollowups is testdata/address_followups.json.
type addressFollowups struct {
	Transcripts []struct {
		ID       string   `json:"id"`
		Area     string   `json:"area"`
		Messages []string `json:"messages"`
	} `json:"transcripts"`
}

// Every follow-up in the transcripts reaches the open request: no rule
// reads it as a search, a decline, a cancel or a withdrawal first.
func TestAddressFollowupsReachTheRequest(t *testing.T) {
	b, err := os.ReadFile("testdata/address_followups.json")
	if err != nil {
		t.Fatal(err)
	}
	var f addressFollowups
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Transcripts) == 0 {
		t.Fatal("no transcripts")
	}
	for _, tr := range f.Transcripts {
		if LooksLikeDiscovery(tr.Messages[0]) {
			t.Errorf("%s: the request reads as a search: %q", tr.ID, tr.Messages[0])
		}
		for _, m := range tr.Messages[1:] {
			if IsClearSearch(m) || LooksLikeDiscovery(m) || IsDraftDecline(m) || IsCancelRequest(m) || IsWithdrawal(m) || IsNo(m) || IsYes(m) {
				t.Errorf("%s: %q is read by a rule before the open request", tr.ID, m)
			}
		}
	}
}

// E: SKIP to "what do you need from the pharmacy?" goes on to where —
// never to a summary with no address.
func TestMedicineSkipGoesToTheAddress(t *testing.T) {
	g, _, _ := newAddressRig(t)
	withPlaybooks(t, g)
	g.r.Addresses = nil
	g.say(t, efMsg1)
	r := g.say(t, "skip")
	if !strings.HasPrefix(r, "Got it, the East Village. What's the exact address?") {
		t.Fatalf("%q", r)
	}
	if p := g.pending(t); !p.DetailsAsked || p.DetailsTries != 0 {
		t.Fatalf("%+v", p)
	}
}

// C: the parser kept both ways the address was written, one per stop
// (live replay 2026-11-13): a YES places both, as one drop-off — never the
// same "Did you mean …?" again.
func TestAddressGuessYesPlacesEveryVariant(t *testing.T) {
	g, _, _ := newAddressRig(t)
	g.r.Parser = &funcParser{fn: func(text string) (llm.TaskDraft, error) {
		d := llm.TaskDraft{Title: "Pick up medicine in the East Village", Category: "delivery", Location2: "East Village"}
		if strings.Contains(text, efMsg4) {
			d.Location1, d.Location2 = efMsg2, efMsg4
		}
		return d, nil
	}}
	g.say(t, efMsg1)
	if r := g.say(t, efMsg4); !strings.HasPrefix(r, "Did you mean "+efGuess) {
		t.Fatalf("%q", r)
	}
	r := g.say(t, "YES")
	p := g.pending(t)
	if strings.HasPrefix(r, "Did you mean") || p.DetailsAsked || p.Draft.Location1 != "" || p.Draft.Location2 != efGuess {
		t.Fatalf("%q %+v", r, p)
	}
}

func TestSameBuilding(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"335 e 3 ave", efGuess, true}, {"335 6 st 3 ave", efGuess, true}, {"CVS on 3rd Ave", efGuess, false}, {"12-14 Main St", "12-14 Main St, Queens", true}, {"33 E 6th St", efGuess, false}} {
		if got := sameBuilding(c.a, c.b); got != c.want {
			t.Errorf("%q %q: %v", c.a, c.b, got)
		}
	}
}

// C: guesses are free only up to MaxFreeGuesses — an answer that never
// places the address still reaches a person.
func TestAddressGuessesAreBounded(t *testing.T) {
	g, _, _ := newAddressRig(t)
	g.r.Ops = &memOps{}
	g.r.Parser = &funcParser{fn: func(text string) (llm.TaskDraft, error) {
		return llm.TaskDraft{Title: "Pick up medicine", Category: "delivery", Location2: latestLine(text)}, nil
	}}
	g.r.Addresses = &fakeGuesser{fn: func([]string, string, string) string { return efGuess }}
	n := 0
	for ; n < 10 && g.state() != StateHandoff; n++ {
		g.say(t, fmt.Sprintf("33%d 6 st", n))
	}
	if g.state() != StateHandoff || n != MaxFreeGuesses+MaxDetailsTries {
		t.Fatalf("state %s after %d answers", g.state(), n)
	}
}

// A: when the answer to "what do you need from the pharmacy?" cannot be
// parsed, it is never put in as an address.
func TestFirstAskAnswerIsNeverAnAddress(t *testing.T) {
	g, fp, _ := newAddressRig(t)
	withPlaybooks(t, g)
	g.say(t, efMsg1)
	fp.fn = func(string) (llm.TaskDraft, error) { return llm.TaskDraft{}, errors.New("empty title") }
	r := g.say(t, "the usual stuff")
	if p := g.pending(t); p.Draft.Location1 != "" || p.Draft.Location2 != "" || !strings.HasPrefix(r, CopyDidntCatch) {
		t.Fatalf("%q %+v", r, p)
	}
}
