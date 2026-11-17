package channel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Production pilot 2026-11-17 18:40–18:56 UTC: the transcripts these tests
// replay are from that run (identity 6abfaec9…).

// coffeeD is the request ops prepared in the pilot.
var coffeeD = llm.TaskDraft{Title: "Pick up an American coffee at Blue Bottle", Category: "delivery",
	Location1: "54 W 40th St, New York, NY 10018", Location2: "2 Bond St, New York, NY 10012"}

// pastThread is memThread with every line stamped at a fixed time (a claim
// after it is "since the opening line").
type pastThread struct {
	st *fakeStore
	at time.Time
}

func (m pastThread) RecentMessages(_ context.Context, _ Channel, _ string, limit int) ([]ThreadMessage, error) {
	var out []ThreadMessage
	for _, b := range m.st.storedBodies() {
		out = append(out, ThreadMessage{Direction: "outbound", Text: b, At: m.at})
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (g *rig) sentCount() int {
	g.ad.mu.Lock()
	defer g.ad.mu.Unlock()
	return len(g.ad.sent)
}

var pilotID = Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}

// Item 3: "A HO:RA teammate will get back to you shortly." went out after
// each customer message, right after ops' own replies. Now: at most once
// per hand-off, and never once ops have written or claimed it.
func TestHoldingLineAtMostOnce(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.say(t, "Where are Blue Bottle in Manhattan,")
	if got := g.say(t, "talk to a human"); got != CopyHandoffStart {
		t.Fatalf("hand-off: %q", got)
	}
	if got := g.say(t, "hello?"); got != CopyHandoffHolding {
		t.Fatalf("first message while held: %q", got)
	}
	n := g.sentCount()
	g.say(t, "anyone there?")
	g.say(t, "Can you search Blue Bottle for me?")
	if g.sentCount() != n || g.state() != StateHandoff {
		t.Fatalf("the holding line went out again: %q", g.sentTexts()[n:])
	}
}

func TestHoldingLineNeverAfterAnOpsReply(t *testing.T) {
	g, ops := handoffRig(t, llm.IntentNewTask)
	g.say(t, "talk to a human")
	for _, s := range []string{"hi this is hora support", "how can i help you"} {
		if err := g.r.OpsSend(context.Background(), pilotID, s); err != nil {
			t.Fatal(err)
		}
	}
	n := g.sentCount()
	// The pilot's three customer messages after ops wrote.
	for _, s := range []string{"Can you search Blue Bottle for me?", "Yes", "Bond street 200 nyc"} {
		g.say(t, s)
		if s == "Can you search Blue Bottle for me?" {
			if err := g.r.OpsSend(context.Background(), pilotID, "you mean Blue Bottle, 54 W 40th St?"); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, s := range g.sentTexts()[n:] {
		if s == CopyHandoffHolding {
			t.Fatalf("holding line after an ops reply: %q", g.sentTexts()[n:])
		}
	}
	if got := len(ops.notices); got < 4 {
		t.Fatalf("every message still reaches ops: %d notices", got)
	}
}

func TestHoldingLineNeverAfterAClaim(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.r.Thread = pastThread{st: g.st, at: time.Now().Add(-time.Minute)}
	g.say(t, "talk to a human")
	if err := g.r.OpsClaim(context.Background(), pilotID); err != nil {
		t.Fatal(err)
	}
	n := g.sentCount()
	g.say(t, "hello?")
	if g.sentCount() != n {
		t.Fatalf("holding line after a claim: %q", g.sentTexts()[n:])
	}
}

// Items 4 and 5: ops prepared the coffee pickup and released the chat. The
// hand-back said "You're back with your HO:RA concierge. What can I help
// you with?" above the summary, and the budget came only after the person
// changed something. Now: only the next step — the budget question (the
// errand is a purchase, so only how much), then the summary.
func TestHandBackSendsOnlyTheNextStep(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.say(t, "talk to a human")
	c := g.conv(t)
	p := &PendingDraft{Source: "Hi I need a coffee at blue bottle", BudgetAskIndex: -1, CreatedAtUnix: time.Now().Unix()}
	p.SetItems([]DraftItem{{Draft: coffeeD, QuoteCents: 2400, QuoteMinutes: 30, NeedsBudget: true, PurchaseClear: true}})
	c.Pending = p
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := g.r.Release(context.Background(), pilotID); err != nil {
		t.Fatal(err)
	}
	got := g.ad.last()
	if got != CopyAskBudgetClear || g.state() != StateAwaitingBudget {
		t.Fatalf("hand-back: %q state %s", got, g.state())
	}
	sum := g.say(t, "Nothing")
	if !strings.HasPrefix(sum, "Pick up an American coffee at Blue Bottle") || !strings.Contains(sum, "Reply YES to book") {
		t.Fatalf("summary after the budget: %q", sum)
	}
	if got := g.say(t, "Yes ! Book"); got != CopyBooked || g.booker.posts != 1 {
		t.Fatalf("Yes ! Book: %q posts %d", got, g.booker.posts)
	}
}

func TestHandBackWithAReadySummaryIsTheSummaryAlone(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.say(t, "laundry pickup")
	g.say(t, "I want customer service")
	if err := g.r.Release(context.Background(), pilotID); err != nil {
		t.Fatal(err)
	}
	last := g.ad.last()
	if strings.Contains(last, CopyHandoffReleased) || strings.Contains(last, CopyBackToDraft) || !strings.HasPrefix(last, "Laundry pickup") {
		t.Fatalf("%q", last)
	}
}

// With nothing open the hand-back line stands.
func TestHandBackWithNothingOpen(t *testing.T) {
	g, _ := handoffRig(t, llm.IntentNewTask)
	g.say(t, "talk to a human")
	if err := g.r.Release(context.Background(), pilotID); err != nil {
		t.Fatal(err)
	}
	if got := g.ad.last(); got != CopyHandoffReleased {
		t.Fatalf("%q", got)
	}
}

// Item 7: a cancel by text is confirmed in the chat ("All set: … is
// cancelled. No charge…"); the app's "Your task has been cancelled. Reason:
// Cancelled by text message: …" is not sent to the phone as well.
func TestTextCancelSendsOneMessage(t *testing.T) {
	if s, ok := StatusText(StatusEvent{Type: "CANCELLED", TaskTitle: "Wait in line at Apple Store",
		Body: "Your task has been cancelled. Reason: " + TextCancelReasonPrefix + "Found another way"}); ok {
		t.Fatalf("sent: %q", s)
	}
	// A cancel made elsewhere (the app, ops) still reaches the chat.
	if s, ok := StatusText(StatusEvent{Type: "CANCELLED", Body: "Your task has been cancelled. Reason: Plans changed"}); !ok || s == "" {
		t.Fatal("an app cancel must still be told")
	}
}

// Item 1: "I need someone wait line for me at Apple Store 767 5th Ave,
// ASAP" was parsed as queue but asked the check type's "If they find it,
// what should they do (for example, buy 2)?". A queue errand asks what the
// line is for, the longest wait and what to do at the front.
func TestQueueErrandUsesTheQueuePlaybook(t *testing.T) {
	pbs, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Playbooks = pbs
	g.parser.draft = llm.TaskDraft{Title: "Wait in line at Apple Store", Category: "queue", Location1: "General Motors Building, 767 5th Ave, New York, NY 10153"}
	got := g.say(t, "I need someone wait line for me at Apple Store 767 5th Ave, ASAP")
	q := pbs.ByID("queue")
	for _, a := range q.Asks {
		if !strings.Contains(got, a.Question) {
			t.Errorf("missing %s: %q", a.Key, got)
		}
	}
	if strings.Contains(got, "If they find it") || g.pending(t).Playbook != "queue" {
		t.Fatalf("check questions on a queue errand: %q (playbook %q)", got, g.pending(t).Playbook)
	}
	if g.pending(t).Draft.Category != "queue" {
		t.Fatalf("category %q", g.pending(t).Draft.Category)
	}
	sum := g.say(t, "30 mins")
	if !strings.Contains(sum, "Reply YES to book") {
		t.Fatalf("after the answer: %q", sum)
	}
	if got := g.say(t, "yes book it"); got != CopyBooked {
		t.Fatalf("yes book it: %q", got)
	}
}

// The parser's queue category wins over a check matched by rule, unless the
// words only ask to look; words that match no type take the category's.
func TestQueueCategoryOverCheck(t *testing.T) {
	pbs, _ := llm.Playbooks()
	r := &Router{Playbooks: pbs}
	check := pbs.ByID("check")
	for _, c := range []struct {
		words, want string
	}{
		{"be at Supreme on Lafayette for the 11am drop for me", "queue"},
		{"how long is the line at Levain? just look", "check"},
		{"check if Levain still has cookies", "check"},
	} {
		if got := r.categoryPlaybook(check, draftOpts{}, nil, "queue", c.words); got == nil || got.ID != c.want {
			t.Errorf("%q: %s, want %s", c.words, pbID(got), c.want)
		}
	}
	if got := r.categoryPlaybook(nil, draftOpts{}, nil, "queue", "get me a spot for the Supreme drop"); got == nil || got.ID != "queue" {
		t.Errorf("no rule match: %s", pbID(got))
	}
	// An earlier draft's type, or discovery's, stands.
	if got := r.categoryPlaybook(check, draftOpts{playbook: "check"}, nil, "queue", "x"); got != check {
		t.Error("discovery's type must stand")
	}
}

// Every category maps to its playbook (or, for the general ones, to none:
// their words decide), and every mapped playbook exists and files its
// drafts under a category the form accepts.
func TestCategoryPlaybookMapping(t *testing.T) {
	pbs, _ := llm.Playbooks()
	for cat := range llm.Categories {
		id, ok := CategoryPlaybooks[cat]
		switch cat {
		case "delivery", "anything_else":
			if ok {
				t.Errorf("%s names no one type, mapped to %s", cat, id)
			}
			continue
		}
		if !ok || pbs.ByID(id) == nil {
			t.Errorf("%s: no playbook (%q)", cat, id)
		}
	}
	// By words, the queue type comes before check.
	for _, w := range []string{"I need someone wait line for me at Apple Store 767 5th Ave, ASAP", "can someone wait in line for me at Trader Joe's",
		"hold my spot in line at the DMV", "stand in line for me at Dominique Ansel"} {
		if p := pbs.Match(w); p == nil || p.ID != "queue" {
			t.Errorf("%q: %s", w, pbID(p))
		}
	}
	for _, w := range []string{"how long is the line at Levain", "check if Levain still has the cookie", "is there a line at Joe's Pizza right now"} {
		if p := pbs.Match(w); p == nil || p.ID != "check" {
			t.Errorf("%q: %s", w, pbID(p))
		}
	}
}

func pbID(p *llm.Playbook) string {
	if p == nil {
		return "none"
	}
	return p.ID
}

// ── Item 2: place search while drafting ─────────────────────────────────

const (
	bbMsg1 = "Hi I need a coffee at blue bottle, \nIs a one close to 54 w, right?"
	bbMsg2 = "I  want a coffee at Blue Bottle but I don’t know the address"
	bbMsg3 = "Where are Blue Bottle in Manhattan,"
	bbAddr = "54 W 40th St, New York, NY 10018"
	bbDrop = "200 Bond St, New York, NY 10012"
)

// bbParse: what the parser read in the pilot for each step (the stop it
// could not place), then a good parse once an address is in the words.
func bbParse(text string) (llm.TaskDraft, error) {
	d := llm.TaskDraft{Title: "Pick up an American coffee at Blue Bottle", Category: "delivery"}
	switch last := latestLine(text); {
	case strings.Contains(text, "Bond"):
		d.Location1, d.Location2 = bbAddr, bbDrop
	case strings.Contains(text, bbAddr):
		d.Location1 = bbAddr
	case last == bbMsg2:
		d.Location1 = "Blue Bottle Coffee near 54 W"
	case last == bbMsg3:
		d.Location1 = "Blue Bottle Coffee, Manhattan"
	default:
		d.Location1 = "W 54th St"
	}
	return d, nil
}

// bbArea: Google placed only full addresses.
func bbArea(d llm.TaskDraft) AreaCheck {
	var resolved []string
	for _, s := range []string{d.Location1, d.Location2} {
		switch strings.TrimSpace(s) {
		case "":
		case bbAddr, bbDrop:
			resolved = append(resolved, s)
		default:
			return AreaCheck{Outcome: AreaUnclear, Stop: s}
		}
	}
	return AreaCheck{Outcome: AreaInside, Resolved: resolved}
}

// stopGuesser reads the café and the area the way the prompt asks.
type stopGuesser struct {
	mu    sync.Mutex
	calls int
}

func (f *stopGuesser) GuessAddress(context.Context, []string, string, string) (string, error) {
	return "", nil
}

func (f *stopGuesser) GuessStop(_ context.Context, said []string, unclear, area string) (llm.StopGuess, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	all := strings.ToLower(strings.Join(said, " "))
	if strings.Contains(strings.ToLower(unclear), "bond") {
		// The drop-off as typed. Sonnet also named the café from the
		// earlier messages here (address eval 2026-11-17): the router
		// must not search a drop-off as a place.
		return llm.StopGuess{Address: bbDrop, Place: "Blue Bottle Coffee", Near: "Bond St, Manhattan"}, nil
	}
	if !strings.Contains(all, "blue bottle") {
		return llm.StopGuess{}, nil
	}
	near := "W 54th St, Manhattan"
	if strings.Contains(latestLine(strings.Join(said, "\n")), "Manhattan") {
		near = "Manhattan"
	}
	return llm.StopGuess{Place: "Blue Bottle Coffee", Near: near}, nil
}

// searchingPlaces: fakePlaces whose search returns its places in order
// (invented fixtures), recording each query.
type searchingPlaces struct {
	fakePlaces
	order   []string
	queries []string
	located []string
	near    int
}

func (f *searchingPlaces) SearchPlaces(_ context.Context, q string, _ bool) ([]llm.Place, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	var out []llm.Place
	for _, id := range f.order {
		out = append(out, f.byID[id])
	}
	return out, nil
}

// LocateArea: W 54th St (between 6th and 7th Ave) for the pilot's "54 w";
// anything else is not placed.
func (f *searchingPlaces) LocateArea(_ context.Context, area string) (float64, float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.located = append(f.located, area)
	if strings.Contains(area, "54") {
		return 40.7638, -73.9800, true
	}
	return 0, 0, false
}

func (f *searchingPlaces) SearchPlacesNear(ctx context.Context, q string, openNow bool, _, _, _ float64) ([]llm.Place, error) {
	f.mu.Lock()
	f.near++
	f.mu.Unlock()
	return f.SearchPlaces(ctx, q, openNow)
}

// blueBottles: invented fixtures at real-world points. Google's order puts
// W 15th St (2.7 miles from W 54th St on the streets) ahead of Columbus
// Circle; sorted, the three nearest are Rockefeller Plaza, Columbus Circle
// and W 40th St.
func blueBottles() *searchingPlaces {
	open := true
	ps := map[string]llm.Place{
		"bb1": {ID: "bb1", Name: "Blue Bottle Coffee", Address: "54 W 40th St", FullAddress: bbAddr, Rating: 4.5, RatingCount: 1200, OpenNow: &open, ClosesAt: "7 PM", InNYC: true, Lat: 40.7527, Lng: -73.9837},
		"bb2": {ID: "bb2", Name: "Blue Bottle Coffee", Address: "1 Rockefeller Plaza", FullAddress: "1 Rockefeller Plaza, New York, NY 10020", Rating: 4.4, RatingCount: 800, InNYC: true, Lat: 40.7587, Lng: -73.9787},
		"bb3": {ID: "bb3", Name: "Blue Bottle Coffee", Address: "450 W 15th St", FullAddress: "450 W 15th St, New York, NY 10011", Rating: 4.6, RatingCount: 2100, InNYC: true, Lat: 40.7424, Lng: -74.0063},
		"bbx": {ID: "bbx", Name: "Blue Bottle Coffee", Address: "Jersey City", FullAddress: "Jersey City, NJ", InNYC: false, Lat: 40.7178, Lng: -74.0431},
		"bb4": {ID: "bb4", Name: "Blue Bottle Coffee", Address: "Bryant Park", FullAddress: "Bryant Park, New York, NY", InNYC: true},
		"bb5": {ID: "bb5", Name: "Blue Bottle Coffee", Address: "10 Columbus Cir", FullAddress: "10 Columbus Cir, New York, NY 10019", Rating: 4.3, RatingCount: 600, InNYC: true, Lat: 40.7681, Lng: -73.9827},
	}
	return &searchingPlaces{fakePlaces: fakePlaces{byID: ps}, order: []string{"bb1", "bbx", "bb3", "bb2", "bb4", "bb5"}}
}

func newBlueBottleRig(t *testing.T) (*rig, *searchingPlaces, *memOps) {
	t.Helper()
	g, ops := handoffRig(t, llm.IntentNewTask)
	g.r.Parser = &funcParser{fn: bbParse}
	g.r.Addresses = &stopGuesser{}
	pl := blueBottles()
	g.r.Places = pl
	g.booker.area = bbArea
	return g, pl, ops
}

// The pilot's three messages: each offers the Blue Bottles found near the
// area named, with addresses; none is a failed answer, nothing hands off.
func TestBlueBottleTranscriptOffersPlaces(t *testing.T) {
	g, pl, ops := newBlueBottleRig(t)
	for i, msg := range []string{bbMsg1, bbMsg2, bbMsg3} {
		got := g.say(t, msg)
		t.Logf("%s\n→ %s", msg, got)
		if strings.Contains(got, "I couldn't find") || g.state() == StateHandoff || len(ops.notices) != 0 {
			t.Fatalf("message %d: %q (state %s, ops %d)", i+1, got, g.state(), len(ops.notices))
		}
		want := []string{"1. Blue Bottle Coffee", "2. Blue Bottle Coffee", "3. Blue Bottle Coffee", CopyStopOptionsPick}
		if i < 2 {
			// Near W 54th St: closest first, W 15th St (2.7 miles) gone.
			want = append(want, "1 Rockefeller Plaza", "10 Columbus Cir", "54 W 40th St")
			if strings.Contains(got, "450 W 15th St") || strings.Index(got, "Rockefeller") > strings.Index(got, "Columbus") || strings.Index(got, "Columbus") > strings.Index(got, "W 40th") {
				t.Fatalf("message %d: not by distance from W 54th St:\n%s", i+1, got)
			}
		}
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Fatalf("message %d lacks %q:\n%s", i+1, w, got)
			}
		}
		if strings.Contains(got, "Jersey City") || strings.Contains(got, "4. ") {
			t.Fatalf("outside NYC or more than three:\n%s", got)
		}
		if p := g.pending(t); p.DetailsTries != 0 || len(p.StopOptions) != 3 {
			t.Fatalf("message %d: tries %d options %v", i+1, p.DetailsTries, p.StopOptions)
		}
	}
	// W 54th St is located and searched around; "Manhattan" is a whole
	// borough: Google's order, no cut.
	if pl.near != 2 || len(pl.located) != 2 || !strings.Contains(pl.queries[2], "near Manhattan") {
		t.Fatalf("near searches %d located %q queries %q", pl.near, pl.located, pl.queries)
	}
}

// The pick fills the stop with Google's address; the next question is
// where to bring it (2026-11-17: the pilot went straight to the budget),
// then the budget (a coffee is a purchase: only how much), the summary
// with both stops, and "Yes ! Book".
func TestBlueBottlePickToBooking(t *testing.T) {
	g, _, _ := newBlueBottleRig(t)
	g.say(t, bbMsg1)
	got := g.say(t, "3") // 54 W 40th St, third by distance
	t.Logf("3 → %s", got)
	p := g.pending(t)
	if p.Draft.Location1 != bbAddr || len(p.StopOptions) != 0 || p.UnclearStop != "" || len(p.Places) != 1 || p.Places[0].ID != "bb1" {
		t.Fatalf("after the pick: %q %+v places %+v", got, p.Draft, p.Places)
	}
	if got != CopyAskDropOff || !p.DropOffAsked {
		t.Fatalf("the drop-off comes next: %q", got)
	}
	got = g.say(t, "Bond street 200 nyc")
	t.Logf("Bond street 200 nyc → %s", got)
	if got != fmt.Sprintf(CopyConfirmAddressGuess, bbDrop) {
		t.Fatalf("the drop-off as Google has it: %q", got)
	}
	got = g.say(t, "yes")
	t.Logf("yes → %s", got)
	if got != CopyAskBudgetClear {
		t.Fatalf("budget after the drop-off: %q", got)
	}
	sum := g.say(t, "Nothing")
	t.Logf("Nothing → %s", sum)
	if !strings.Contains(sum, bbAddr) || !strings.Contains(sum, bbDrop) || !strings.Contains(sum, "Reply YES to book") {
		t.Fatalf("summary: %q", sum)
	}
	if got := g.say(t, "Yes ! Book"); got != CopyBooked || g.booker.posts != 1 {
		t.Fatalf("Yes ! Book: %q posts %d", got, g.booker.posts)
	}
}

// Another address instead of a number re-drafts as before; YES to a single
// place picks it.
func TestStopOptionsAnswers(t *testing.T) {
	g, pl, _ := newBlueBottleRig(t)
	pl.order = []string{"bb1"}
	got := g.say(t, bbMsg1)
	if !strings.Contains(got, CopyStopOptionsPickOne) {
		t.Fatalf("one place: %q", got)
	}
	g.say(t, "yes")
	if p := g.pending(t); p.Draft.Location1 != bbAddr {
		t.Fatalf("YES to the one place: %+v", p.Draft)
	}

	g2, _, _ := newBlueBottleRig(t)
	g2.say(t, bbMsg1)
	g2.say(t, "Bond street 200 nyc and the one on 40th")
	if p := g2.pending(t); len(p.StopOptions) != 0 || p.Draft.Location2 != bbDrop {
		t.Fatalf("an address re-drafts: %+v options %v", p.Draft, p.StopOptions)
	}
}

func TestIsPlaceQuestion(t *testing.T) {
	for _, s := range []string{bbMsg1, bbMsg2, bbMsg3, "Can you search Blue Bottle for me?", "where's the nearest Duane Reade", "is there a branch near Union Square?"} {
		if !IsPlaceQuestion(s) {
			t.Errorf("%q is a place question", s)
		}
	}
	for _, s := range []string{"200 Bond St", "pick up my laundry", "Bond street 200 nyc", "yes", "deliver it to my office"} {
		if IsPlaceQuestion(s) {
			t.Errorf("%q is not a place question", s)
		}
	}
}
