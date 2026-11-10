package channel

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Search vs errand (production 2026-11-07 20:56–21:04 Rome). The messages
// below are the person's, verbatim. Rules are the fast path; when they do
// not match, the configured model (LLM_SMALL_MODEL) decides.

// oracleClassifier answers what a correct model would, and counts calls.
type oracleClassifier struct {
	mu       sync.Mutex
	calls    int
	label    map[string]llm.Intent
	def      llm.Intent
	playbook string
}

func (c *oracleClassifier) Classify(ctx context.Context, text string) (llm.Intent, error) {
	got, err := c.ClassifyDetail(ctx, text)
	return got.Intent, err
}

func (c *oracleClassifier) ClassifyDetail(_ context.Context, text string) (llm.Classification, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if l, ok := c.label[text]; ok {
		return llm.Classification{Intent: l, Playbook: c.playbook}, nil
	}
	return llm.Classification{Intent: c.def, Playbook: c.playbook}, nil
}

type fakeQuestionReader struct {
	mu    sync.Mutex
	calls int
	yes   bool
}

func (f *fakeQuestionReader) ReadsAsQuestion(context.Context, string, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.yes, nil
}

const (
	prodPizza      = "Hi can you help me to find pizzeria around 250 Mercer street?"
	prodRecommend  = "What kind of pizza can I get? Any recommendations?"
	prodSoho       = "Can you help me to find pizzeria around soho"
	prodDryCleaner = "Can you check if there is the dry cleaner open until 9:00pm?"
	prodCancel     = "No cancel the draft"
)

// 1–3. The three searches are discovery by rule, with GLM's answer
// ("new_task") never asked: no parse, no draft, no address check.
func TestProd1107SearchesAreDiscovery(t *testing.T) {
	for _, msg := range []string{prodPizza, prodSoho, prodDryCleaner} {
		g := newDiscoRig(llm.IntentNewTask, infoResult("Here are some places.", false, []llm.Place{nolita}, []llm.Place{princeSt}))
		g.ids.link(me, "user-1")
		cl := &oracleClassifier{def: llm.IntentNewTask} // what GLM said
		g.r.Classifier = cl
		g.booker.area = func(d llm.TaskDraft) AreaCheck {
			t.Fatalf("%q: address check on a search (%q)", msg, d.Location1)
			return AreaCheck{}
		}
		reply := g.say(t, msg)
		if len(g.d.reqs) != 1 || g.d.reqs[0].Text != msg {
			t.Fatalf("%q: discovery reqs %d", msg, len(g.d.reqs))
		}
		if !strings.Contains(reply, "1. Nolita Pizza") || strings.Contains(reply, "couldn't find") {
			t.Fatalf("%q: reply\n%s", msg, reply)
		}
		if cl.calls != 0 || len(g.parser.sources) != 0 || g.state() != StateIdle || g.booker.quotes != 0 {
			t.Fatalf("%q: classifier %d parses %d state %s quotes %d", msg, cl.calls, len(g.parser.sources), g.state(), g.booker.quotes)
		}
	}
}

// A neighbourhood is where to look: the discovery model gets the words as
// they are (it puts "SoHo" in its query), and the "Looking that up…" line
// goes before the model's first call — a neighbourhood says where.
func TestNeighbourhoodIsADiscoveryLocation(t *testing.T) {
	for _, msg := range []string{prodSoho, "any good bagel places around Union Square?", "looking for a tailor in Williamsburg"} {
		g := newDiscoRig(llm.IntentNewTask, infoResult("", false, []llm.Place{nolita}))
		var before []string
		g.d.before = func() { before = g.sentTexts() }
		g.say(t, msg)
		if len(before) != 1 || before[0] != CopyLookingUp {
			t.Fatalf("%q: sent before the model %q", msg, before)
		}
		if strings.Contains(strings.Join(g.sentTexts(), "\n"), "full address") {
			t.Fatalf("%q: asked for a street address", msg)
		}
	}
}

// Words no rule knows: the classifier's find_place is a search; its
// new_task an errand, with its playbook when no rule names one. A search
// the discovery model says is not one is drafted, as before.
func TestClassifierDecidesWhenRulesDoNot(t *testing.T) {
	msg := "pizza places in soho?"
	if LooksLikeDiscovery(msg) || IsClearErrand(msg) {
		t.Fatalf("%q: a rule matched — pick words no rule knows", msg)
	}
	g := newDiscoRig(llm.IntentNewTask, infoResult("", false, []llm.Place{nolita}))
	cl := &oracleClassifier{label: map[string]llm.Intent{msg: llm.IntentFind}, def: llm.IntentNewTask}
	g.r.Classifier = cl
	if reply := g.say(t, msg); cl.calls != 1 || len(g.d.reqs) != 1 || !strings.Contains(reply, "1. Nolita Pizza") || g.state() != StateIdle || g.conv(t).Pending != nil {
		t.Fatalf("find_place: classifier %d discovery %d parses %d\n%s", cl.calls, len(g.d.reqs), len(g.parser.sources), reply)
	}

	// find_place, but discovery says not_discovery: a draft.
	g = newDiscoRig(llm.IntentNewTask, llm.DiscoveryResult{Action: llm.DiscoverNotDiscovery, Usual: -1})
	g.ids.link(me, "user-1")
	g.r.Classifier = &oracleClassifier{def: llm.IntentFind}
	g.say(t, msg)
	if len(g.parser.sources) != 1 || g.state() == StateIdle {
		t.Fatalf("not_discovery: parses %d state %s", len(g.parser.sources), g.state())
	}

	// new_task with the classifier's playbook: no rule word names a type
	// ("take care of my stuff"), the model's type is used.
	pbs, _ := llm.Playbooks()
	g = newDiscoRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Playbooks = pbs
	g.r.Classifier = &oracleClassifier{def: llm.IntentNewTask, playbook: "laundry"}
	g.parser.draft = llm.TaskDraft{Title: "Errand", Category: "laundry", Location1: "12 E 14th St, New York, NY 10003"}
	g.say(t, "could you take care of my stuff at 12 E 14th St tomorrow")
	if p := g.pending(t); p.Playbook != "laundry" {
		t.Fatalf("playbook %q: the classifier's type unused", p.Playbook)
	}
	// A rule match wins over the classifier's type.
	g = newDiscoRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.r.Playbooks = pbs
	g.r.Classifier = &oracleClassifier{def: llm.IntentNewTask, playbook: "laundry"}
	g.parser.draft = llm.TaskDraft{Title: "Groceries", Category: "grocery", Location1: "142 E 14th St, New York, NY 10003"}
	g.say(t, "could you sort out milk and eggs at 142 E 14th St")
	if p := g.pending(t); p.Playbook != "groceries" {
		t.Fatalf("playbook %q: the rule must win", p.Playbook)
	}
}

// The errand fast path: a clear errand is drafted without the classifier,
// with one split and one parse.
func TestClearErrandSkipsTheClassifier(t *testing.T) {
	g := newDiscoRig(llm.IntentGreeting)
	g.ids.link(me, "user-1")
	cl := &oracleClassifier{def: llm.IntentGreeting}
	g.r.Classifier = cl
	g.say(t, "pick up my dry cleaning at 12 Bedford Ave at 5pm")
	if cl.calls != 0 || len(g.d.reqs) != 0 || len(g.parser.sources) != 1 || g.state() != StateAwaitingConfirm {
		t.Fatalf("classifier %d discovery %d parses %d state %s", cl.calls, len(g.d.reqs), len(g.parser.sources), g.state())
	}
}

// 2 with a draft open: "is there a dry cleaner open until 9?" is a search
// (Google's hours), never a second errand on the draft.
func TestHoursQuestionWhileDraftIsOpen(t *testing.T) {
	g := newDiscoRig(llm.IntentNewTask, infoResult("Here are dry cleaners open until 9.", false, []llm.Place{joes}))
	g.ids.link(me, "user-1")
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	before := g.pending(t)
	parses := len(g.parser.sources)
	reply := g.say(t, prodDryCleaner)
	if len(g.d.reqs) != 1 || len(g.parser.sources) != parses || !strings.Contains(reply, CopyBookingStillWaiting) {
		t.Fatalf("discovery %d parses %d→%d\n%s", len(g.d.reqs), parses, len(g.parser.sources), reply)
	}
	if p := g.pending(t); p.Source != before.Source || len(p.Items()) != len(before.Items()) || g.state() != StateAwaitingConfirm {
		t.Fatalf("draft changed: %q → %q", before.Source, p.Source)
	}
}

// 4. "No cancel the draft" drops the draft by rule in every drafting state
// — the summary, the details question (where production handed it off),
// the type's questions, the budget — never a hand-off, never a parse.
func TestCancelTheDraftDropsByRule(t *testing.T) {
	type setup struct {
		name  string
		draft llm.TaskDraft
		msg   string
		pbs   bool
		buy   bool
		state State
	}
	setups := []setup{
		{"summary", laundry, "pick up my laundry at 12 Bedford Ave", false, false, StateAwaitingConfirm},
		{"details", llm.TaskDraft{Title: "Two errands", Category: "anything_else"}, "can you do 2 errands for me", false, false, StateAwaitingConfirm},
		{"playbook ask", llm.TaskDraft{Title: "Pizza", Category: "food", Location1: "68 Kenmare St, New York, NY 10012"}, "pick up lunch from 68 Kenmare St", true, false, StateAwaitingConfirm},
		{"budget", llm.TaskDraft{Title: "Buy detergent", Category: "grocery", Location1: "9 Ave A, New York, NY 10009"}, "buy detergent at 9 Ave A", false, true, StateAwaitingBudget},
	}
	for _, s := range setups {
		for _, cancel := range []string{prodCancel, "cancel the draft", "no, cancel it", "cancel it please", "Cancel draft"} {
			g, ops := handoffRig(t, llm.IntentNewTask)
			g.ids.link(me, "user-1")
			g.parser.draft = s.draft
			if s.pbs {
				g.r.Playbooks, _ = llm.Playbooks()
			}
			if s.buy {
				g.r.Purchases = &fakePurchases{yes: true}
			}
			g.say(t, s.msg)
			if g.state() != s.state {
				t.Fatalf("%s: set-up state %s", s.name, g.state())
			}
			if s.name == "details" && !g.pending(t).DetailsAsked {
				t.Fatalf("details: not asked")
			}
			parses := len(g.parser.sources)
			reply := g.say(t, cancel)
			c := g.conv(t)
			if c.State != StateIdle || c.Pending != nil || len(g.parser.sources) != parses || len(ops.notices) != 0 {
				t.Fatalf("%s / %q: state %s pending %v parses %d→%d ops %d\n%s", s.name, cancel, c.State, c.Pending != nil, parses, len(g.parser.sources), len(ops.notices), reply)
			}
		}
	}
}

// 5. With "what should they order?" open, a question back is answered and
// the question put again — never drafted as the order. No place yet: a
// search near the draft's words; a pick from it makes the draft there.
func TestOrderQuestionAskedBackNoPlace(t *testing.T) {
	g := newDiscoRig(llm.IntentNewTask, infoResult("Here are pizzerias near 250 Mercer St.", false, []llm.Place{nolita}, []llm.Place{princeSt}))
	g.ids.link(me, "user-1")
	g.r.Playbooks, _ = llm.Playbooks()
	g.parser.draft = llm.TaskDraft{Title: "Pizza to the office", Category: "food", Location1: "250 Mercer St, New York, NY 10012"}
	ask := g.say(t, "bring me pizza at 250 Mercer St")
	if p := g.pending(t); len(p.PlaybookAsk) == 0 {
		t.Fatalf("no order question:\n%s", ask)
	}
	parses := len(g.parser.sources)
	reply := g.say(t, prodRecommend)
	if len(g.parser.sources) != parses || len(g.d.reqs) != 1 {
		t.Fatalf("parses %d→%d discovery %d: drafted as the order", parses, len(g.parser.sources), len(g.d.reqs))
	}
	if req := g.d.reqs[0]; len(req.Earlier) != 1 || !strings.Contains(req.Earlier[0], "250 Mercer St") {
		t.Fatalf("search words %+v", req.Earlier)
	}
	if !strings.Contains(reply, "1. Nolita Pizza") || !strings.Contains(reply, "SKIP") {
		t.Fatalf("reply:\n%s", reply)
	}
	if p := g.pending(t); len(p.PlaybookAsk) == 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("order question dropped: %+v", p)
	}
	// "1": the draft is made at that place, its questions asked afresh.
	g.parser.draft = llm.TaskDraft{Title: "Pizza from Nolita Pizza", Category: "food", Location1: "68 Kenmare St, New York, NY 10012", Location2: "250 Mercer St, New York, NY 10012"}
	g.say(t, "1")
	if p := g.pending(t); len(p.Places) != 1 || p.Places[0].Name != "Nolita Pizza" || p.Places[0].ID != nolita.ID {
		t.Fatalf("pick: %+v", p.Places)
	}
}

// 5 with the draft's place known: answered from that place's own data,
// with no "Want me to set that up…?" (the draft is already open), and the
// question again. The model reads a "?" the rule does not know.
func TestOrderQuestionAskedBackKnownPlace(t *testing.T) {
	g, a, f := playRig(t)
	g.parser.draft = llm.TaskDraft{Title: "Pizza pickup", Category: "food", Location1: "68 Kenmare St, New York, NY 10012", Location2: "250 Mercer St, New York, NY 10012"}
	ask := g.say(t, "1")
	p := g.pending(t)
	if len(p.PlaybookAsk) == 0 || len(p.Places) != 1 || p.Places[0].ID != nolita.ID {
		t.Fatalf("set-up: asks %v places %+v\n%s", p.PlaybookAsk, p.Places, ask)
	}
	parses := len(g.parser.sources)
	reply := g.say(t, prodRecommend)
	if len(g.parser.sources) != parses || len(a.reqs) != 1 || len(f.looked) == 0 || f.looked[len(f.looked)-1] != nolita.ID {
		t.Fatalf("parses %d→%d answer calls %d looked %v", parses, len(g.parser.sources), len(a.reqs), f.looked)
	}
	if !strings.Contains(reply, "spicy vodka slice") || strings.Contains(reply, "Want ") || !strings.Contains(reply, "SKIP") {
		t.Fatalf("reply:\n%s", reply)
	}
	if p := g.pending(t); len(p.PlaybookAsk) == 0 {
		t.Fatal("order question dropped")
	}
	// The answer itself is still the answer.
	g.say(t, "a margherita please")
	if p := g.pending(t); len(p.PlaybookAsk) != 0 {
		t.Fatalf("the order was not taken: %+v", p)
	}

	// A "?" no rule knows: the model reads it.
	g, _, _ = playRig(t)
	qr := &fakeQuestionReader{yes: false}
	g.r.QuestionReader = qr
	g.parser.draft = llm.TaskDraft{Title: "Pizza pickup", Category: "food", Location1: "68 Kenmare St, New York, NY 10012"}
	g.say(t, "1")
	parses = len(g.parser.sources)
	g.say(t, "can you get me a pepperoni?")
	if qr.calls != 1 || len(g.pending(t).PlaybookAsk) != 0 {
		t.Fatalf("question reader %d, asks %v", qr.calls, g.pending(t).PlaybookAsk)
	}
	_ = parses
}

type paraphrases struct {
	Find   []string `json:"find"`
	Errand []string `json:"errand"`
}

func loadParaphrases(t *testing.T) paraphrases {
	t.Helper()
	b, err := os.ReadFile("testdata/routing_paraphrases.json")
	if err != nil {
		t.Fatal(err)
	}
	var p paraphrases
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Find) < 30 || len(p.Errand) < 30 {
		t.Fatalf("paraphrases: %d find, %d errand (want 30+ each)", len(p.Find), len(p.Errand))
	}
	return p
}

// 6. The paraphrase set, both directions. The rules never misroute: no
// search matches the errand fast path, no errand matches the search rule.
// What the rules leave, the router hands to the classifier and obeys it
// (the live model is measured by cmd/agenttest -routing).
func TestRoutingParaphrases(t *testing.T) {
	p := loadParaphrases(t)
	fast := 0
	for _, s := range p.Find {
		if IsClearErrand(s) {
			t.Errorf("search on the errand fast path: %q", s)
		}
		if LooksLikeDiscovery(s) {
			fast++
		}
	}
	for _, s := range p.Errand {
		if LooksLikeDiscovery(s) {
			t.Errorf("errand matches the search rule: %q", s)
		}
		if IsClearErrand(s) {
			fast++
		}
	}
	t.Logf("fast path: %d of %d phrasings by rule; the rest go to the classifier", fast, len(p.Find)+len(p.Errand))

	label := map[string]llm.Intent{}
	for _, s := range p.Find {
		label[s] = llm.IntentFind
	}
	for _, s := range p.Errand {
		label[s] = llm.IntentNewTask
	}
	for _, s := range append(append([]string{}, p.Find...), p.Errand...) {
		g := newDiscoRig(llm.IntentNewTask, infoResult("", false, []llm.Place{nolita}))
		g.ids.link(me, "user-1")
		g.r.Classifier = &oracleClassifier{label: label}
		g.say(t, s)
		// (A parse may have started alongside the classifier; a search
		// leaves no draft.)
		searched := len(g.d.reqs) == 1 && g.state() == StateIdle && g.conv(t).Pending == nil
		if label[s] == llm.IntentFind && !searched {
			t.Errorf("search drafted: %q (discovery %d parses %d)", s, len(g.d.reqs), len(g.parser.sources))
		}
		if label[s] == llm.IntentNewTask && (len(g.d.reqs) != 0 || len(g.parser.sources) == 0) {
			t.Errorf("errand searched: %q (discovery %d parses %d)", s, len(g.d.reqs), len(g.parser.sources))
		}
	}
}

type slowParser struct {
	fakeParser
	d time.Duration
}

func (s *slowParser) ParseTask(ctx context.Context, p, text string) (llm.TaskDraft, llm.Usage, error) {
	time.Sleep(s.d)
	return s.fakeParser.ParseTask(ctx, p, text)
}

type slowSplitter struct{ d time.Duration }

func (s slowSplitter) SplitErrands(_ context.Context, text string) ([]string, llm.Usage, error) {
	time.Sleep(s.d)
	return []string{text}, llm.Usage{Model: "fake"}, nil
}

type slowClassifier struct {
	d time.Duration
	c llm.Intent
}

func (s slowClassifier) Classify(context.Context, string) (llm.Intent, error) {
	time.Sleep(s.d)
	return s.c, nil
}

// The draft's model calls run side by side (the 6 s budget): the split
// and the parse together, and both alongside the classifier when it is
// asked — so an errand waits for the slowest call, not the sum.
func TestDraftCallsRunTogether(t *testing.T) {
	const d = 80 * time.Millisecond
	for _, tc := range []struct {
		msg  string
		cl   bool
		over time.Duration // the sum, which it must beat
	}{
		{"pick up my dry cleaning at 12 Bedford Ave", false, 2 * d},             // rule: split ∥ parse
		{"could you sort out my stuff at 12 Bedford Ave tomorrow", true, 3 * d}, // classifier ∥ split ∥ parse
	} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.r.Go = nil // real goroutines
		sp := &slowParser{fakeParser: fakeParser{draft: laundry}, d: d}
		g.r.Parser, g.r.Splitter = sp, slowSplitter{d: d}
		if tc.cl {
			g.r.Classifier = slowClassifier{d: d, c: llm.IntentNewTask}
		}
		start := time.Now()
		g.say(t, tc.msg)
		took := time.Since(start)
		if g.state() != StateAwaitingConfirm || len(sp.sources) != 1 {
			t.Fatalf("%q: state %s parses %d", tc.msg, g.state(), len(sp.sources))
		}
		if took >= tc.over-d/2 {
			t.Fatalf("%q: took %s — the calls ran one after another (sum %s)", tc.msg, took, tc.over)
		}
	}
}
