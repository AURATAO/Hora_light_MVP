package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// Referrals (supporter.go, 2026-11-11): an approved supporter asking for the
// link to pass on gets it; only questions about their own work get "you're
// already a supporter"; that line is never sent twice in a row. Anyone else
// asking on a friend's behalf gets the usual application answer.

type referralPhrasings struct {
	Referral []struct {
		Msg string `json:"msg"`
		By  string `json:"by"`
	} `json:"referral"`
	ReferralErrand []string `json:"referral_errand"`
	ShareLink      []string `json:"share_link"`
}

func loadReferralPhrasings(t *testing.T) referralPhrasings {
	t.Helper()
	b, err := os.ReadFile("testdata/supporter_phrasings.json")
	if err != nil {
		t.Fatal(err)
	}
	var p referralPhrasings
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Referral) < 8 || len(p.ReferralErrand) < 5 || len(p.ShareLink) < 1 {
		t.Fatalf("phrasings: %d referral, %d errand, %d share link", len(p.Referral), len(p.ReferralErrand), len(p.ShareLink))
	}
	return p
}

func shareCopy(r *Router) string { return fmt.Sprintf(CopySupporterShare, r.applyURL()) }

// referralClassifier: what a correct model says — supporter_interest, with
// supporter_referral for the listed words.
type referralClassifier struct {
	calls    int
	referral map[string]bool
}

func (c *referralClassifier) Classify(ctx context.Context, text string) (llm.Intent, error) {
	got, err := c.ClassifyDetail(ctx, text)
	return got.Intent, err
}

func (c *referralClassifier) ClassifyDetail(_ context.Context, text string) (llm.Classification, error) {
	c.calls++
	return llm.Classification{Intent: llm.IntentSupporter, Referral: c.referral[text]}, nil
}

func approvedRig(t *testing.T) (*rig, *memOps) {
	t.Helper()
	g, ops, _ := supporterRig(t, nil)
	g.r.Supporters = fakeSupporters{status: map[string]string{"user-1": "approved"}}
	return g, ops
}

// Production 2026-11-10 15:20–15:26, an approved supporter's number: "Can I
// be a supporter?" is about themselves; the next message asks for the link
// to share and gets it — no "already a supporter" line again.
func TestSupporterReferralProduction(t *testing.T) {
	g, ops := approvedRig(t)
	if reply := g.say(t, "Can I be a supporter?"); reply != CopySupporterAlready {
		t.Fatalf("first: %q", reply)
	}
	reply := g.say(t, "Can you share me the apply link that I can send to my friends")
	if reply != shareCopy(g.r) || strings.Contains(reply, "already") {
		t.Fatalf("share: %q", reply)
	}
	if reply != "Here's the link to share: "+DefaultSupporterApplyURL {
		t.Fatalf("copy: %q", reply)
	}
	if supporterNotices(ops) != 0 || len(g.parser.sources) != 0 || g.state() != StateIdle {
		t.Fatalf("notices %d parses %d state %s", supporterNotices(ops), len(g.parser.sources), g.state())
	}
	// The same message first, too: the link, not "already".
	g, _ = approvedRig(t)
	if reply := g.say(t, "Can you share me the apply link that I can send to my friends"); reply != shareCopy(g.r) {
		t.Fatalf("share first: %q", reply)
	}
}

// The rules: every "rule" referral phrasing matches SupporterReferral (and
// so SupporterInterest), no "model" one does, and no errand with someone
// else in it does.
func TestSupporterReferralRules(t *testing.T) {
	p := loadReferralPhrasings(t)
	for _, c := range p.Referral {
		rule := c.By == "rule"
		if SupporterReferral(c.Msg) != rule || SupporterInterest(c.Msg) != rule {
			t.Errorf("%q: referral %t interest %t, case says by %s", c.Msg, SupporterReferral(c.Msg), SupporterInterest(c.Msg), c.By)
		}
		if _, mixed := SplitSupporterInterest(c.Msg); rule && mixed {
			t.Errorf("referral read as mixed: %q", c.Msg)
		}
	}
	for _, m := range p.ReferralErrand {
		if SupporterReferral(m) || SupporterInterest(m) {
			t.Errorf("errand read as a referral: %q", m)
		}
	}
	// Someone asking about themselves is not a referral.
	sp := loadSupporterPhrasings(t)
	for _, c := range sp.Interest {
		if SupporterReferral(c.Msg) {
			t.Errorf("own interest read as a referral: %q", c.Msg)
		}
	}
	for _, m := range append(sp.Errand, sp.FollowUp...) {
		if SupporterReferral(m) {
			t.Errorf("read as a referral: %q", m)
		}
	}
}

// An approved supporter: every referral gets the link to share — by rule
// without the classifier, the model's words by its supporter_referral. No
// "already" line, no ops notice, no draft.
func TestSupporterReferralApproved(t *testing.T) {
	p := loadReferralPhrasings(t)
	for _, c := range p.Referral {
		g, ops := approvedRig(t)
		cl := &referralClassifier{referral: map[string]bool{c.Msg: true}}
		g.r.Classifier = cl
		reply := g.say(t, c.Msg)
		if reply != shareCopy(g.r) || supporterNotices(ops) != 0 || g.conv(t).Pending != nil {
			t.Errorf("%q: reply %q notices %d", c.Msg, reply, supporterNotices(ops))
		}
		if (c.By == "rule") != (cl.calls == 0) {
			t.Errorf("%q (%s): classifier calls %d", c.Msg, c.By, cl.calls)
		}
	}
}

// An approved supporter asking about their own work: "already", once; asked
// again, the link to share instead of the same line.
func TestSupporterAlreadyNotTwiceInARow(t *testing.T) {
	for _, pair := range [][2]string{
		{"Can I be a supporter?", "Can I be a supporter?"},
		{"are you hiring?", "how do I become a supporter?"},
		{"Can I be a supporter?", "I'm a supporter already? how do I get more tasks? can I work more"},
	} {
		g, _ := approvedRig(t)
		if reply := g.say(t, pair[0]); reply != CopySupporterAlready {
			t.Fatalf("%q: %q", pair[0], reply)
		}
		if reply := g.say(t, pair[1]); reply != shareCopy(g.r) {
			t.Fatalf("again %q: %q", pair[1], reply)
		}
	}
	// The model's supporter_interest without referral: the same.
	g, _ := approvedRig(t)
	g.r.Classifier = &referralClassifier{}
	msg := "Do you guys take freelancers?"
	if a, b := g.say(t, msg), g.say(t, msg); a != CopySupporterAlready || b != shareCopy(g.r) {
		t.Fatalf("model: %q then %q", a, b)
	}
	// Something else in between: "already" again is not in a row.
	g, _ = approvedRig(t)
	g.say(t, "are you hiring?")
	g.say(t, "hi")
	if reply := g.say(t, "are you hiring?"); reply != CopySupporterAlready {
		t.Fatalf("not in a row: %q", reply)
	}
}

// Not a supporter: a referral is supporter interest — the usual
// application answer, then the follow-up line; ops told once.
func TestSupporterReferralNotSupporter(t *testing.T) {
	p := loadReferralPhrasings(t)
	for _, c := range p.Referral {
		for _, status := range []string{"none", "applied", "rejected"} {
			g, ops, _ := supporterRig(t, map[string]llm.Intent{c.Msg: llm.IntentSupporter})
			g.r.Supporters = fakeSupporters{status: map[string]string{"user-1": status}}
			if reply := g.say(t, c.Msg); reply != applyCopy(g.r) || supporterNotices(ops) != 1 || g.conv(t).Pending != nil {
				t.Errorf("%q (%s): reply %q notices %d", c.Msg, status, reply, supporterNotices(ops))
			}
		}
	}
	g, ops, _ := supporterRig(t, nil)
	g.say(t, "are you hiring?")
	if reply := g.say(t, "can my friend join?"); reply != followUpCopy(g.r) || supporterNotices(ops) != 1 {
		t.Fatalf("after the link: %q notices %d", reply, supporterNotices(ops))
	}
}

// Errands with someone else in them stay errands, for a supporter too.
func TestSupporterReferralErrands(t *testing.T) {
	p := loadReferralPhrasings(t)
	for _, m := range p.ReferralErrand {
		g, ops := approvedRig(t)
		reply := g.say(t, m)
		if strings.Contains(reply, g.r.applyURL()) || reply == CopySupporterAlready || supporterNotices(ops) != 0 {
			t.Errorf("%q: %q", m, reply)
		}
	}
}

// "share the link with my friends": which link the words do not say — the
// application link for an approved supporter or right after it went out;
// otherwise routed as usual.
func TestSupporterShareLink(t *testing.T) {
	p := loadReferralPhrasings(t)
	for _, m := range p.ShareLink {
		if SupporterInterest(m) {
			t.Errorf("%q: interest by rule alone", m)
		}
		g, _ := approvedRig(t)
		if reply := g.say(t, m); reply != shareCopy(g.r) {
			t.Errorf("approved %q: %q", m, reply)
		}
		g, ops, _ := supporterRig(t, nil)
		g.say(t, "are you hiring?")
		if reply := g.say(t, m); reply != followUpCopy(g.r) || supporterNotices(ops) != 1 {
			t.Errorf("after the link %q: %q", m, reply)
		}
		g, ops, _ = supporterRig(t, nil)
		if reply := g.say(t, m); strings.Contains(reply, g.r.applyURL()) || supporterNotices(ops) != 0 {
			t.Errorf("cold %q: %q", m, reply)
		}
	}
}

func TestSupporterShareCopy(t *testing.T) {
	r := &Router{SupporterApplyURL: "https://example.test/apply"}
	if got := shareCopy(r); got != "Here's the link to share: https://example.test/apply" {
		t.Fatalf("%q", got)
	}
}
