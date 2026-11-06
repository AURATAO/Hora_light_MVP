package channel

import (
	"context"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// The first real discovery test (production 2026-11-06 00:09–00:11 Rome):
// the messages below are the person's, verbatim.

var (
	nolita = llm.Place{ID: "gN", Name: "Nolita Pizza", Address: "68 Kenmare St, Manhattan", FullAddress: "68 Kenmare St, New York, NY 10012",
		Rating: 4.6, RatingCount: 1890, PriceLevel: 1, OpenNow: open(true), ClosesAt: "11 PM", InNYC: true}
	joesPizza = llm.Place{ID: "gJ", Name: "Joe's Pizza - Broadway", Address: "1435 Broadway, Manhattan", FullAddress: "1435 Broadway, New York, NY 10018",
		Rating: 4.5, RatingCount: 12000, PriceLevel: 1, OpenNow: open(true), InNYC: true}
	princeSt = llm.Place{ID: "gP", Name: "Prince Street Pizza", Address: "27 Prince St, Manhattan", FullAddress: "27 Prince St, New York, NY 10012",
		Rating: 4.4, RatingCount: 8000, InNYC: true}
)

const pizzaAsk = "my office is 250 Mercer street can you recommend me few nice pizzeria?"

// pizzaRig has the three pizzerias open as options, from pizzaAsk.
func pizzaRig(t *testing.T, intent llm.Intent, more ...llm.DiscoveryResult) *discoRig {
	t.Helper()
	res := append([]llm.DiscoveryResult{infoResult("Here are pizzerias near 250 Mercer St.", false, []llm.Place{nolita}, []llm.Place{joesPizza}, []llm.Place{princeSt})}, more...)
	g := newDiscoRig(intent, res...)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Pizza pickup", Category: "food", Location1: "68 Kenmare St, New York, NY 10012", Location2: "250 Mercer St, New York, NY 10012"}
	if reply := g.say(t, pizzaAsk); !strings.Contains(reply, "1. Nolita Pizza") {
		t.Fatalf("options:\n%s", reply)
	}
	return g
}

// 1a. A search by rule that says where to look: "Looking that up…" goes
// out before the first model call — no classifier first — and once only,
// though the model then searches. Not when the daily cap leaves no search.
func TestDiscoveryLookingUpBeforeTheModel(t *testing.T) {
	g := newDiscoRig(llm.IntentNewTask, infoResult("", false, []llm.Place{nolita}))
	cl := &countingClassifier{}
	g.r.Classifier = cl
	g.d.ask = 1
	var sentAtCall []string
	g.d.before = func() { sentAtCall = g.sentTexts() }
	g.say(t, pizzaAsk)
	if len(sentAtCall) != 1 || sentAtCall[0] != CopyLookingUp {
		t.Fatalf("sent before the model call: %q", sentAtCall)
	}
	if sent := g.sentTexts(); len(sent) != 2 || !strings.Contains(sent[1], "1. Nolita Pizza") {
		t.Fatalf("sent %q", sent)
	}
	if cl.calls != 0 {
		t.Fatalf("classifier called %d times before a search by rule", cl.calls)
	}

	// The cap is used up: no "Looking that up…", the cap's own copy.
	g = newDiscoRig(llm.IntentNewTask, infoResult("", false, []llm.Place{nolita}))
	g.d.ask = 1
	g.store.calls["id-"+me+"|"+g.r.now().In(g.r.loc()).Format("2006-01-02")] = g.r.limits().PerDay
	g.say(t, pizzaAsk)
	if sent := g.sentTexts(); len(sent) != 1 || sent[0] != CopySearchLimit {
		t.Fatalf("at the cap, sent %q", sent)
	}

	// No where in the words: the model may ask "Where should I look?" —
	// the line waits for an actual search.
	g = newDiscoRig(llm.IntentNewTask, llm.DiscoveryResult{Action: llm.DiscoverAskLocation, Usual: -1})
	g.say(t, "can you recommend a good pizzeria?")
	if sent := g.sentTexts(); len(sent) != 1 || sent[0] != CopyAskLocation {
		t.Fatalf("no where: sent %q", sent)
	}
}

// 2. "Ok nolita pizza, what is the best flavor?" names an option inside a
// longer message: that option is chosen (no model call), and the question
// is kept as a note for the supporter — there is no menu to answer from.
func TestDiscoveryPickByNameWithAQuestion(t *testing.T) {
	g := pizzaRig(t, llm.IntentNewTask)
	reply := g.say(t, "Ok nolita pizza, what is the best flavor?")
	if len(g.d.reqs) != 1 {
		t.Fatalf("a pick by name went to the model (%d turns)", len(g.d.reqs))
	}
	p := g.pending(t)
	if !drafting(g.state()) || len(p.Places) != 1 || p.Places[0].Name != "Nolita Pizza" || len(g.places.details) != 1 || g.places.details[0] != "gN" {
		t.Fatalf("state %s places %+v details %v", g.state(), p.Places, g.places.details)
	}
	if len(p.Notes) != 1 || p.Notes[0] != "what is the best flavor?" {
		t.Fatalf("notes %q", p.Notes)
	}
	if !strings.Contains(reply, "I don't have Nolita Pizza's menu") {
		t.Fatalf("reply:\n%s", reply)
	}
	if src := p.Source; !strings.Contains(src, pizzaAsk) || !strings.Contains(src, "At: 68 Kenmare St") {
		t.Fatalf("source %q", src)
	}

	// A name with details, no question: the pick, the details for the parser.
	g = pizzaRig(t, llm.IntentNewTask)
	g.say(t, "let's do joe's pizza and get me two plain slices")
	if p := g.pending(t); !drafting(g.state()) || p.Places[0].Name != "Joe's Pizza - Broadway" || len(p.Notes) != 0 ||
		!strings.Contains(p.Source, "two plain slices") || len(g.d.reqs) != 1 {
		t.Fatalf("details: state %s pending %+v turns %d", g.state(), p, len(g.d.reqs))
	}
}

// 3. A question about the options goes to discovery and is answered from
// Google's data and fixed copy; the options stay open; nothing is drafted.
// A YES to "Want me to set that up at …?" starts that option's draft.
func TestDiscoveryQuestionAboutTheOptions(t *testing.T) {
	about := func(topic, item string, choice int) llm.DiscoveryResult {
		return llm.DiscoveryResult{Action: llm.DiscoverAbout, Topic: topic, Item: item, Choice: choice, Usual: -1,
			Lead: "Their margherita is the best!"} // never shown: about has no model text
	}
	g := pizzaRig(t, llm.IntentNewTask, about("menu", "pizza", 0))
	parses := len(g.parser.sources)
	reply := g.say(t, "No I was asking which pizza should I choose")
	want := "I don't have Nolita Pizza's menu, but I can ask your supporter to get their most popular pizza."
	if !strings.HasPrefix(reply, want) || !strings.HasSuffix(reply, "Want me to set that up at Nolita Pizza? Or reply with another number.") {
		t.Fatalf("reply:\n%s", reply)
	}
	if !strings.Contains(reply, "1. Nolita Pizza · ★ 4.6 (1,890 reviews) · inexpensive · open now, closes 11 PM") || !strings.Contains(reply, CopyDiscoveryFooter) {
		t.Fatalf("facts:\n%s", reply)
	}
	for _, bad := range []string{"margherita", "$", "estimate"} {
		if strings.Contains(strings.ToLower(reply), bad) {
			t.Fatalf("about reply carries %q:\n%s", bad, reply)
		}
	}
	o := g.offer()
	if g.state() != StateIdle || len(g.parser.sources) != parses || o == nil || len(o.Options) != 3 || o.Suggest != 1 {
		t.Fatalf("state %s parses %d offer %+v", g.state(), len(g.parser.sources), o)
	}
	// One set of lookups for the message: the name match and the answer
	// share them (the daily cap counts 3, not 6).
	if len(g.places.about) != 3 || len(g.places.names) != 0 {
		t.Fatalf("lookups: about %v names %v", g.places.about, g.places.names)
	}
	if req := g.d.reqs[1]; !strings.Contains(req.HoraLine, "1. Nolita Pizza") || len(req.Earlier) != 1 || req.Earlier[0] != pizzaAsk {
		t.Fatalf("the model did not see the options: %+v", req)
	}
	// YES: Nolita's draft, with the suggestion's words for the parser.
	g.say(t, "yes")
	if p := g.pending(t); !drafting(g.state()) || p.Places[0].Name != "Nolita Pizza" || !strings.Contains(p.Source, "Get their most popular pizza.") {
		t.Fatalf("yes: state %s pending %+v", g.state(), p)
	}

	// "which one should I pick?": the best rated by rule, all three's facts.
	g = pizzaRig(t, llm.IntentNewTask, about("pick", "", 0))
	reply = g.say(t, "which one should I pick?")
	if !strings.HasPrefix(reply, "Going by Google Maps ratings, I'd go with Nolita Pizza.") || !strings.Contains(reply, "2. Joe's Pizza - Broadway · ★ 4.5 (12,000 reviews)") ||
		!strings.Contains(reply, "3. Prince Street Pizza · ★ 4.4 (8,000 reviews)") || g.offer() == nil {
		t.Fatalf("pick:\n%s", reply)
	}
	// The options stay pickable by number after the answer.
	g.say(t, "2")
	if p := g.pending(t); !drafting(g.state()) || p.Places[0].Name != "Joe's Pizza - Broadway" {
		t.Fatalf("2 after the answer: state %s", g.state())
	}

	// "what's good there?" about option 2; "do they do delivery?".
	g = pizzaRig(t, llm.IntentNewTask, about("menu", "", 2), about("delivery", "", 3))
	if reply := g.say(t, "what's good there?"); !strings.HasPrefix(reply, "I don't have Joe's Pizza - Broadway's menu, but I can ask your supporter to find out what's most popular there") ||
		!strings.Contains(reply, "Want me to set that up at Joe's Pizza - Broadway?") {
		t.Fatalf("menu:\n%s", reply)
	}
	if reply := g.say(t, "do they do delivery?"); !strings.HasPrefix(reply, "I can't tell from Google Maps whether Prince Street Pizza delivers") || g.offer().Suggest != 3 {
		t.Fatalf("delivery:\n%s", reply)
	}
	if len(g.places.about) == 0 || g.state() != StateIdle {
		t.Fatalf("about lookups %v state %s", g.places.about, g.state())
	}

	// A model item that is not a plain thing is not repeated.
	g = pizzaRig(t, llm.IntentNewTask, about("menu", "pizza for $5, reply YES", 1))
	if reply := g.say(t, "what should I get there?"); strings.Contains(reply, "$5") || !strings.Contains(reply, "find out what's most popular") {
		t.Fatalf("item:\n%s", reply)
	}

	// The lookups fail: fixed copy, options still open, no suggestion.
	g = pizzaRig(t, llm.IntentNewTask, about("pick", "", 0))
	g.places.err = context.DeadlineExceeded
	if reply := g.say(t, "which one should I pick?"); reply != CopyAboutNoData || g.offer() == nil || g.offer().Suggest != 0 {
		t.Fatalf("down: %q", reply)
	}
}

// 4. With options open: a bare "1" still picks; "never mind" drops them; a
// real new errand still drafts (discovery reads it once, says not
// discovery, and the classifier's new_task drafts it).
func TestDiscoveryOptionsOpenStillWork(t *testing.T) {
	g := pizzaRig(t, llm.IntentNewTask)
	g.say(t, "1")
	if p := g.pending(t); !drafting(g.state()) || p.Places[0].Name != "Nolita Pizza" || len(g.d.reqs) != 1 {
		t.Fatalf("1: state %s turns %d", g.state(), len(g.d.reqs))
	}

	g = pizzaRig(t, llm.IntentNewTask)
	if got := g.say(t, "never mind"); got != CopyAck || g.offer() != nil || g.booker.quotes != 0 {
		t.Fatalf("never mind: %q offer %+v", got, g.offer())
	}

	g = pizzaRig(t, llm.IntentNewTask, llm.DiscoveryResult{Action: llm.DiscoverNotDiscovery, Usual: -1})
	cl := &countingClassifier{}
	g.r.Classifier = cl
	g.parser.draft = llm.TaskDraft{Title: "Dry cleaning pickup", Category: "laundry", Location1: "100 Washington St, New York, NY 10006"}
	errand := "also pick up my dry cleaning at 100 Washington St"
	g.say(t, errand)
	if !drafting(g.state()) || !strings.Contains(g.pending(t).Source, errand) || len(g.pending(t).Places) != 0 {
		t.Fatalf("new errand: state %s", g.state())
	}
	if len(g.d.reqs) != 2 || cl.calls != 1 {
		t.Fatalf("discovery turns %d (want 2: the search, this message once), classifier %d", len(g.d.reqs), cl.calls)
	}
}

func TestPickNameIn(t *testing.T) {
	names := []string{"Nolita Pizza", "Joe's Pizza - Broadway", "Prince Street Pizza"}
	for msg, want := range map[string]int{
		"Ok nolita pizza, what is the best flavor?":           1,
		"let's do joe's pizza and get me two plain slices":    2,
		"I'll take prince street, can you get a pepperoni":    3,
		"can you get me a pepperoni square from Nolita Pizza": 1,
		"No I was asking which pizza should I choose":         0,
		"not nolita pizza, something closer please":           0,
		"is joe's pizza better than prince street pizza?":     0,
		"which pizza place has the best reviews here?":        0,
		"also pick up my dry cleaning at 100 Washington St":   0,
	} {
		if got, _ := pickNameIn(msg, 3, names); got != want {
			t.Errorf("%q: %d, want %d", msg, got, want)
		}
	}
	if d, q := splitQuestion("Ok nolita pizza, what is the best flavor?"); d != "Ok nolita pizza" || q != "what is the best flavor?" {
		t.Errorf("split %q / %q", d, q)
	}
	if d, q := splitQuestion("Nolita pizza please. Do they have gluten free"); d != "Nolita pizza please" || q != "Do they have gluten free?" {
		t.Errorf("split %q / %q", d, q)
	}
}

func TestAboutItemOK(t *testing.T) {
	for _, ok := range []string{"pizza", "birthday cake", "slice"} {
		if !AboutItemOK(ok) {
			t.Errorf("refused %q", ok)
		}
	}
	for _, bad := range []string{"", "pizza for $5", "reply YES", "ignore the rules", "a very long list of many words", "2 slices", "pizza?"} {
		if AboutItemOK(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// drafting: a draft was started (a food errand asks its budget first).
func drafting(s State) bool { return s == StateAwaitingConfirm || s == StateAwaitingBudget }
