package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func wants(e ScopeEval) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Split(e.Want, "|") {
		m[w] = true
	}
	return m
}

// Every eval case: when a rule decides, it decides right. What the rules
// leave is the model's (agenttest -scope grades it); the plain refusal
// categories must not need it.
func TestScopeRulesOnEvals(t *testing.T) {
	s, err := Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	sr := &s.ScopeRules
	if len(sr.Evals) < 60 {
		t.Fatalf("%d scope evals", len(sr.Evals))
	}
	model := 0
	for _, e := range sr.Evals {
		d := sr.Rule(e.Msg)
		if !d.Decided {
			model++
			t.Logf("model: %-20s %q", e.Want, e.Msg)
			// A plain refusal category is the rules' job; an allowed errand
			// that only sounds close ("wine glasses") and a rephrasing are
			// the model's.
			if e.Group != "rephrase" && e.Group != "allowed" {
				t.Errorf("%s case left to the model (%s): %q", e.Group, e.Want, e.Msg)
			}
			continue
		}
		if !wants(e)[d.ID()] {
			t.Errorf("rule %s, want %s: %q", d.By, e.Want, e.Msg)
		}
	}
	t.Logf("%d/%d cases go to the model", model, len(sr.Evals))
}

// The spec's borderline cases, by rule, with the alert only for the
// romantic/sexual category.
func TestScopeBorderline(t *testing.T) {
	s, _ := Playbooks()
	sr := &s.ScopeRules
	for msg, want := range map[string]string{
		"can you keep me company for a walk in Washington Square Park?": "allow",
		"can someone keep my grandma company?":                          "third_person",
		"come keep me company, I'm home alone":                          "private_company",
		"grab a drink with me":                                          "alcohol",
		"pick up my laundry, the bag is with my doorman":                "allow",
		"pick up the keys from my friend Sarah in Brooklyn":             "allow",
		"just hang out at my place":                                     "private_company",
		"a friendly massage":                                            "intimate",
		"my 12-year-old needs someone to walk her home":                 "minors",
		"looking for an escort tonight":                                 "intimate",
	} {
		d := sr.Rule(msg)
		if !d.Decided || d.ID() != want {
			t.Errorf("%q: %s (%s), want %s", msg, d.ID(), d.By, want)
		}
		if d.Refusal != nil && d.Refusal.Alert != (want == "intimate") {
			t.Errorf("%q: alert %t", msg, d.Refusal.Alert)
		}
	}
	for _, r := range sr.Refusals {
		if r.Alert != (r.ID == "intimate") {
			t.Errorf("%s: alert %t", r.ID, r.Alert)
		}
	}
}

type fakeScope struct {
	verdict string
	err     error
	calls   int
}

func (f *fakeScope) CheckScope(context.Context, string) (string, error) {
	f.calls++
	return f.verdict, f.err
}

// Decide: a rule decides without the model; the rest is the model's; a
// model error or an unknown verdict allows.
func TestScopeDecide(t *testing.T) {
	s, _ := Playbooks()
	sr := &s.ScopeRules
	ctx := context.Background()
	m := &fakeScope{verdict: "intimate"}
	if d, _ := sr.Decide(ctx, m, "pick up my dry cleaning at 12 Bedford Ave"); !d.Allowed() || m.calls != 0 || d.By != "no_scope_words" {
		t.Fatalf("plain errand: %+v calls %d", d, m.calls)
	}
	if d, _ := sr.Decide(ctx, m, "be my date tonight"); d.ID() != "intimate" || m.calls != 0 {
		t.Fatalf("rule refusal: %+v calls %d", d, m.calls)
	}
	msg := "my mom needs company on her walk, can someone go?"
	if sr.Rule(msg).Decided {
		t.Fatalf("%q decided by rule — pick words no rule decides", msg)
	}
	m.verdict = "third_person"
	if d, _ := sr.Decide(ctx, m, msg); d.ID() != "third_person" || m.calls != 1 || d.By != "model" {
		t.Fatalf("model refusal: %+v calls %d", d, m.calls)
	}
	m.verdict = "allow"
	if d, _ := sr.Decide(ctx, m, msg); !d.Allowed() || d.By != "model" {
		t.Fatalf("model allow: %+v", d)
	}
	m.err = errors.New("down")
	if d, err := sr.Decide(ctx, m, msg); !d.Allowed() || err == nil || d.By != "model_error" {
		t.Fatalf("model error: %+v %v", d, err)
	}
	m.err, m.verdict = nil, "nonsense"
	if d, err := sr.Decide(ctx, m, msg); !d.Allowed() || err == nil {
		t.Fatalf("unknown verdict: %+v %v", d, err)
	}
	if d, _ := sr.Decide(ctx, nil, msg); !d.Allowed() || d.By != "no_model" {
		t.Fatalf("no model: %+v", d)
	}
}

// The model's prompt names every category with its rule sentence, and the
// schema's enum is "allow" plus every id.
func TestScopePromptAndSchema(t *testing.T) {
	s, _ := Playbooks()
	p := ScopePrompt()
	if strings.Contains(p, "SCOPE_") {
		t.Fatal("placeholder left")
	}
	enum := scopeSchema()["properties"].(map[string]any)["verdict"].(map[string]any)["enum"].([]string)
	if enum[0] != "allow" || len(enum) != len(s.ScopeRules.Refusals)+1 {
		t.Fatalf("enum %v", enum)
	}
	for _, r := range s.ScopeRules.Refusals {
		if !strings.Contains(p, `"`+r.ID+`": `+r.Rule) {
			t.Errorf("prompt lacks %s", r.ID)
		}
	}
	for _, l := range append(append([]string{}, s.ScopeRules.People...), s.ScopeRules.Homes...) {
		if !strings.Contains(p, l) {
			t.Errorf("prompt lacks %q", l)
		}
	}
}

// Home entry: the access question for words that send a supporter inside,
// skipped when they already say who is there or how to get in; never for
// an errand that stays outside.
func TestHomeEntryAsk(t *testing.T) {
	s, _ := Playbooks()
	sr := &s.ScopeRules
	for msg, ask := range map[string]bool{
		"pick up groceries from Whole Foods and put them away in my kitchen":  true,
		"put my groceries away, I'll be home":                                 false,
		"feed my cat at 12 Bedford Ave tomorrow, the key is with the doorman": false,
		"water my plants at 40 Bond St, my roommate will let you in":          false,
		"pick up my laundry from 12 Bedford Ave":                              false,
		"buy milk at Trader Joe's and bring it to 12 Bedford Ave":             false,
		"tidy up my apartment at 40 Bond St":                                  true,
	} {
		if got := sr.HomeEntryAsk(msg) != nil; got != ask {
			t.Errorf("%q: ask %t", msg, got)
		}
	}
	if !sr.EntersHome("put my groceries away, I'll be home") || sr.EntersHome("pick up my laundry") {
		t.Fatal("EntersHome")
	}
	food := s.ByID("food")
	if asks := s.DraftAsks(food, "pick up pizza from 68 Kenmare St and put it away in my fridge"); len(asks) != 2 || asks[0].Key != "home_access" {
		t.Fatalf("draft asks %v", asks)
	}
	if asks := s.AsksOf(nil); len(asks) != 1 || asks[0].Key != "home_access" {
		t.Fatalf("asks of none %v", asks)
	}
}

// A refusal's reply names what HO:RA can do instead, and never a price or a
// YES to book.
func TestScopeReplies(t *testing.T) {
	s, _ := Playbooks()
	for _, r := range s.ScopeRules.Refusals {
		low := strings.ToLower(r.Reply)
		if !strings.HasPrefix(r.Reply, "Sorry") || strings.Contains(low, "yes") || strings.Contains(r.Reply, "$") || len(r.Reply) > 320 {
			t.Errorf("%s reply: %q", r.ID, r.Reply)
		}
	}
}
