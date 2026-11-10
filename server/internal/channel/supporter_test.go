package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Supporter interest (supporter.go): the application link, never a draft;
// follow-ups get one fixed line; ops hear once a day; a mixed message keeps
// its errand.

type supporterPhrasings struct {
	Interest []struct {
		Msg string `json:"msg"`
		By  string `json:"by"`
	} `json:"interest"`
	Errand          []string `json:"errand"`
	FollowUp        []string `json:"follow_up"`
	AfterLinkErrand []string `json:"after_link_errand"`
	Mixed           []struct {
		Msg  string `json:"msg"`
		Rest string `json:"rest"`
	} `json:"mixed"`
}

func loadSupporterPhrasings(t *testing.T) supporterPhrasings {
	t.Helper()
	b, err := os.ReadFile("testdata/supporter_phrasings.json")
	if err != nil {
		t.Fatal(err)
	}
	var p supporterPhrasings
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Interest) < 20 || len(p.Errand) < 8 || len(p.FollowUp) < 5 || len(p.Mixed) < 2 {
		t.Fatalf("phrasings: %d interest, %d errand, %d follow-up, %d mixed", len(p.Interest), len(p.Errand), len(p.FollowUp), len(p.Mixed))
	}
	return p
}

type fakeSupporters struct{ status map[string]string }

func (f fakeSupporters) SupporterStatus(_ context.Context, userID string) (string, error) {
	if s, ok := f.status[userID]; ok {
		return s, nil
	}
	return "none", nil
}

func supporterRig(t *testing.T, labels map[string]llm.Intent) (*rig, *memOps, *oracleClassifier) {
	t.Helper()
	g, ops := handoffRig(t, llm.IntentNewTask)
	cl := &oracleClassifier{label: labels, def: llm.IntentNewTask}
	g.r.Classifier = cl
	return g, ops, cl
}

func applyCopy(r *Router) string    { return fmt.Sprintf(CopySupporterApply, r.applyURL()) }
func followUpCopy(r *Router) string { return fmt.Sprintf(CopySupporterFollowUp, r.applyURL()) }

func supporterNotices(ops *memOps) int {
	n := 0
	for _, x := range ops.notices {
		if x.Reason == HandoffSupporterInterest {
			n++
		}
	}
	return n
}

// The rules: every "rule" phrasing matches, no "model" one does (it is the
// classifier's), and no errand near-miss matches.
func TestSupporterInterestRules(t *testing.T) {
	p := loadSupporterPhrasings(t)
	for _, c := range p.Interest {
		if got := SupporterInterest(c.Msg); got != (c.By == "rule") {
			t.Errorf("%q: rule %t, case says by %s", c.Msg, got, c.By)
		}
	}
	for _, m := range p.Errand {
		if SupporterInterest(m) {
			t.Errorf("errand read as supporter interest: %q", m)
		}
	}
	trim := func(s string) string { return strings.TrimRight(s, ".?! ") }
	for _, m := range p.Mixed {
		rest, mixed := SplitSupporterInterest(m.Msg)
		if !mixed || trim(rest) != trim(m.Rest) {
			t.Errorf("mixed %q: rest %q (%t), want %q", m.Msg, rest, mixed, m.Rest)
		}
	}
	for _, c := range p.Interest {
		if c.By != "rule" {
			continue // the split runs only once the rule matched
		}
		if _, mixed := SplitSupporterInterest(c.Msg); mixed {
			t.Errorf("interest only, read as mixed: %q", c.Msg)
		}
	}
}

// Every interest phrasing through the router: the application copy, no
// parse, no draft, no price; ops told once. Rule phrasings never ask the
// classifier; model ones are its supporter_interest.
func TestSupporterInterestThroughRouter(t *testing.T) {
	p := loadSupporterPhrasings(t)
	for _, c := range p.Interest {
		g, ops, cl := supporterRig(t, map[string]llm.Intent{c.Msg: llm.IntentSupporter})
		reply := g.say(t, c.Msg)
		if reply != applyCopy(g.r) {
			t.Errorf("%q: reply %q", c.Msg, reply)
		}
		// A model phrasing's words were parsed alongside the classifier (the
		// latency prefetch: it reads, it decides nothing); no draft is made.
		parsedByRule := c.By == "rule" && len(g.parser.sources) != 0
		if parsedByRule || g.conv(t).Pending != nil || g.booker.quotes != 0 || g.state() != StateIdle {
			t.Errorf("%q: parses %d state %s quotes %d", c.Msg, len(g.parser.sources), g.state(), g.booker.quotes)
		}
		if (c.By == "rule") != (cl.calls == 0) {
			t.Errorf("%q (%s): classifier calls %d", c.Msg, c.By, cl.calls)
		}
		if supporterNotices(ops) != 1 || ops.notices[0].PhoneHint != PhoneHint(me) || len(ops.notices[0].Thread) != 0 || ops.notices[0].Urgent {
			t.Errorf("%q: notices %+v", c.Msg, ops.notices)
		}
	}
}

// Near-misses are errands: drafted, no link, ops not told.
func TestSupporterNearMissesAreErrands(t *testing.T) {
	p := loadSupporterPhrasings(t)
	for _, m := range p.Errand {
		g, ops, _ := supporterRig(t, nil)
		reply := g.say(t, m)
		if strings.Contains(reply, g.r.applyURL()) || supporterNotices(ops) != 0 {
			t.Errorf("%q: treated as supporter interest: %q", m, reply)
		}
		if len(g.parser.sources) == 0 && g.state() == StateIdle {
			t.Errorf("%q: not drafted (reply %q)", m, reply)
		}
	}
}

// After the link: a question about being a supporter gets the follow-up
// line and the link — no hand-off, no draft, ops not told twice the same
// day. An errand after the link is an errand. Without the link, a vague
// "how much does it pay?" is not read as supporter interest.
func TestSupporterFollowUps(t *testing.T) {
	p := loadSupporterPhrasings(t)
	for _, f := range p.FollowUp {
		g, ops, _ := supporterRig(t, nil)
		g.say(t, "are you hiring?")
		reply := g.say(t, f)
		if reply != followUpCopy(g.r) || len(g.parser.sources) != 0 || g.state() == StateHandoff || supporterNotices(ops) != 1 {
			t.Errorf("%q: reply %q parses %d state %s notices %d", f, reply, len(g.parser.sources), g.state(), supporterNotices(ops))
		}
		if len(followUpCopy(g.r)) > 250 {
			t.Errorf("follow-up copy %d chars", len(followUpCopy(g.r)))
		}
	}
	// An explicit one ("did you get my application?") too.
	g, ops, _ := supporterRig(t, nil)
	g.say(t, "how do I become a supporter?")
	if reply := g.say(t, "did you get my application?"); reply != followUpCopy(g.r) || supporterNotices(ops) != 1 {
		t.Fatalf("explicit follow-up: %q notices %d", reply, supporterNotices(ops))
	}
	for _, m := range p.AfterLinkErrand {
		g, _, _ := supporterRig(t, nil)
		g.say(t, "are you hiring?")
		reply := g.say(t, m)
		if strings.Contains(reply, g.r.applyURL()) || len(g.parser.sources) != 1 {
			t.Errorf("errand after the link %q: reply %q parses %d", m, reply, len(g.parser.sources))
		}
	}
	g, ops, _ = supporterRig(t, nil)
	if reply := g.say(t, "how much does it pay?"); strings.Contains(reply, g.r.applyURL()) || supporterNotices(ops) != 0 {
		t.Fatalf("no link yet, read as a follow-up: %q", reply)
	}
}

// Ops: once per number per New York day; the next day again.
func TestSupporterOpsOncePerDay(t *testing.T) {
	g, ops, _ := supporterRig(t, nil)
	g.say(t, "are you hiring?")
	g.say(t, "do you need runners?")
	g.say(t, "how much do supporters make?")
	if supporterNotices(ops) != 1 {
		t.Fatalf("same day: %d notices", supporterNotices(ops))
	}
	tomorrow := time.Now().Add(26 * time.Hour)
	g.r.Now = func() time.Time { return tomorrow }
	if reply := g.say(t, "are you still hiring?"); reply != followUpCopy(g.r) || supporterNotices(ops) != 2 {
		t.Fatalf("next day: %q notices %d", reply, supporterNotices(ops))
	}
}

// An approved supporter's number: use the supporter app; ops not told. An
// applicant asking gets the usual answer.
func TestSupporterAlreadySupporter(t *testing.T) {
	g, ops, _ := supporterRig(t, nil)
	g.r.Supporters = fakeSupporters{status: map[string]string{"user-1": "approved"}}
	if reply := g.say(t, "are you hiring?"); reply != CopySupporterAlready || supporterNotices(ops) != 0 || len(g.parser.sources) != 0 {
		t.Fatalf("approved: %q notices %d", reply, supporterNotices(ops))
	}
	g, _, _ = supporterRig(t, nil)
	g.r.Supporters = fakeSupporters{status: map[string]string{"user-1": "applied"}}
	if reply := g.say(t, "did you get my application?"); reply != applyCopy(g.r) {
		t.Fatalf("applied: %q", reply)
	}
}

// A mixed message: the errand words alone are drafted, then the
// application line, once; ops told.
func TestSupporterMixedMessage(t *testing.T) {
	p := loadSupporterPhrasings(t)
	for _, m := range p.Mixed {
		g, ops, _ := supporterRig(t, nil)
		g.say(t, m.Msg)
		if len(g.parser.sources) == 0 || strings.Contains(strings.ToLower(strings.Join(g.parser.sources, " ")), "supporter") || strings.Contains(strings.Join(g.parser.sources, " "), "hiring") {
			t.Errorf("%q: parsed %q", m.Msg, g.parser.sources)
		}
		sent := g.sentTexts()
		line := fmt.Sprintf(CopySupporterLine, g.r.applyURL())
		n := 0
		for _, s := range sent {
			if strings.Contains(s, g.r.applyURL()) {
				n++
			}
		}
		if len(sent) < 2 || sent[len(sent)-1] != line || n != 1 {
			t.Errorf("%q: sent %q", m.Msg, sent)
		}
		if supporterNotices(ops) != 1 {
			t.Errorf("%q: notices %d", m.Msg, supporterNotices(ops))
		}
	}
	// The classifier's also_supporter_interest does the same for words no
	// rule splits.
	g, _, cl := supporterRig(t, nil)
	cl.def = llm.IntentNewTask
	g.r.Classifier = alsoSupporterClassifier{}
	g.say(t, "could you take care of my stuff at 12 E 14th St tomorrow, and do you guys take freelancers")
	if sent := g.sentTexts(); len(sent) < 2 || sent[len(sent)-1] != fmt.Sprintf(CopySupporterLine, g.r.applyURL()) || len(g.parser.sources) == 0 {
		t.Fatalf("model mixed: sent %q parses %d", sent, len(g.parser.sources))
	}
}

type alsoSupporterClassifier struct{}

func (alsoSupporterClassifier) Classify(ctx context.Context, text string) (llm.Intent, error) {
	return llm.IntentNewTask, nil
}
func (alsoSupporterClassifier) ClassifyDetail(context.Context, string) (llm.Classification, error) {
	return llm.Classification{Intent: llm.IntentNewTask, AlsoSupporter: true}, nil
}

// The copy: warm, short, the configured link, no earnings / rate / number of
// tasks.
func TestSupporterCopy(t *testing.T) {
	r := &Router{SupporterApplyURL: "https://example.test/apply"}
	if got := applyCopy(r); !strings.HasSuffix(got, "https://example.test/apply") || len(got) > 250 {
		t.Fatalf("custom URL: %q", got)
	}
	r = &Router{}
	for _, c := range []string{applyCopy(r), followUpCopy(r), fmt.Sprintf(CopySupporterLine, r.applyURL()), CopySupporterAlready} {
		low := strings.ToLower(c)
		if len(c) > 250 || strings.Contains(c, "$") || strings.Contains(low, "hour") || strings.Contains(low, "earn") || strings.Contains(low, "per task") || strings.Contains(low, "guarantee") {
			t.Errorf("copy %q", c)
		}
	}
	if !strings.Contains(applyCopy(r), DefaultSupporterApplyURL) {
		t.Fatal("default URL")
	}
}

// No search or errand in the routing paraphrases reads as supporter
// interest by rule.
func TestSupporterRulesSpareRoutingParaphrases(t *testing.T) {
	b, err := os.ReadFile("testdata/routing_paraphrases.json")
	if err != nil {
		t.Fatal(err)
	}
	var p struct{ Find, Errand []string }
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	for _, m := range append(p.Find, p.Errand...) {
		if SupporterInterest(m) {
			t.Errorf("read as supporter interest: %q", m)
		}
	}
}
