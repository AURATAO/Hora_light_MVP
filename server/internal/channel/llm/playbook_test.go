package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The embedded file parses, every entry is complete, the scope rules are
// filled (scope_test.go grades them), and the discovery prompt
// and schema name every entry — a new entry reaches the model with no code.
func TestPlaybooksFile(t *testing.T) {
	s, err := Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"companionship", "gifts", "friend", "check", "packages", "small_services", "laundry", "food", "pharmacy", "groceries", "repeat_multi"}
	if got := s.IDs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ids %v", got)
	}
	if len(s.ScopeRules.Refusals) == 0 || len(s.ScopeRules.People) == 0 || len(s.ScopeRules.Homes) == 0 {
		t.Fatal("scope_rules not filled")
	}
	enum := DiscoverySchema(0)["properties"].(map[string]any)["playbook"].(map[string]any)["enum"].([]string)
	for _, id := range want {
		if !strings.Contains(DiscoveryPrompt, `"`+id+`"`) {
			t.Errorf("prompt does not name %s", id)
		}
		found := false
		for _, e := range enum {
			found = found || e == id
		}
		if !found {
			t.Errorf("schema enum lacks %s", id)
		}
	}
	if strings.Contains(DiscoveryPrompt, "PLAYBOOK_LIST") {
		t.Fatal("prompt placeholder not filled")
	}
	for _, d := range s.WebSources.WellKnown {
		switch d {
		case "eater.com", "nytimes.com", "nymag.com", "grubstreet.com", "seriouseats.com", "newyorker.com", "thrillist.com", "bonappetit.com", "foodandwine.com", "nypost.com":
			t.Fatalf("%s refuses Anthropic's crawler (2026-11-06): listing it fails every web search", d)
		}
	}
	for _, p := range s.Playbooks {
		for _, e := range p.Evals {
			if e.Kind == "answer" && s.EvalPlace(e.Place) == nil {
				t.Errorf("%s: eval %s: no fixture place %q", p.ID, e.ID, e.Place)
			}
		}
	}
}

// Every "asks" eval in the file, by rule: the entry recognised by its match
// words (or named), and exactly the questions the words leave open.
func TestPlaybookAsksEvals(t *testing.T) {
	s, err := Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range s.Playbooks {
		for _, e := range p.Evals {
			if e.Kind != "asks" {
				continue
			}
			n++
			pb := s.ByID(e.Playbook)
			if pb == nil {
				pb = s.Match(e.Msg)
			}
			if pb == nil || pb.ID != p.ID {
				got := "none"
				if pb != nil {
					got = pb.ID
				}
				t.Errorf("%s: %q recognised as %s", p.ID, e.Msg, got)
				continue
			}
			var keys []string
			for _, a := range pb.AsksFor(e.Msg) {
				keys = append(keys, a.Key)
			}
			if strings.Join(keys, ",") != strings.Join(e.ExpectAsks, ",") {
				t.Errorf("%s: %q asks %v, want %v", p.ID, e.Msg, keys, e.ExpectAsks)
			}
		}
	}
	if n < 15 {
		t.Fatalf("only %d asks evals", n)
	}
}

// A broken entry is refused with its name: unknown source, a regexp that
// does not compile, a note answer without its prefix, no evals.
func TestParsePlaybooksRefusesBadEntries(t *testing.T) {
	base := func(mut func(m map[string]any)) []byte {
		e := map[string]any{"id": "x_type", "name": "X", "guidance": "g", "sources": []string{"places_facts"},
			"asks":  []map[string]any{{"key": "k", "question": "q?", "answer": "redraft"}},
			"evals": []map[string]any{{"kind": "asks", "msg": "m"}}}
		mut(e)
		b, _ := json.Marshal(map[string]any{"playbooks": []any{e}})
		return b
	}
	if _, err := ParsePlaybooks(base(func(map[string]any) {})); err != nil {
		t.Fatalf("good entry: %v", err)
	}
	for name, mut := range map[string]func(map[string]any){
		"source": func(m map[string]any) { m["sources"] = []string{"yelp"} },
		"match":  func(m map[string]any) { m["match"] = []string{"(?!x)"} },
		"note":   func(m map[string]any) { m["asks"] = []map[string]any{{"key": "k", "question": "q?", "answer": "note"}} },
		"evals":  func(m map[string]any) { delete(m, "evals") },
		"id":     func(m map[string]any) { m["id"] = "Bad Id" },
		"skip_if": func(m map[string]any) {
			m["asks"] = []map[string]any{{"key": "k", "question": "q?", "answer": "redraft", "skip_if": "("}}
		},
	} {
		if _, err := ParsePlaybooks(base(mut)); err == nil || !strings.Contains(err.Error(), "x_type") && name != "id" {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

// The prompt block carries the scope rules first (what is allowed about
// people and homes, then every refusal), then the entry's guidance and
// limits; with no scope rules, the entry alone.
func TestPlaybookPromptBlock(t *testing.T) {
	s, _ := Playbooks()
	food := s.ByID("food")
	b := s.PromptBlock(food)
	if !strings.HasPrefix(b, "Scope rules:\n- Companionship in PUBLIC places only") || !strings.Contains(b, "Allergies") || !strings.Contains(b, "Food & restaurants") {
		t.Fatalf("block:\n%s", b)
	}
	for _, r := range s.ScopeRules.Refusals {
		if !strings.Contains(b, "- Not done: "+r.Rule) {
			t.Errorf("block lacks %s", r.ID)
		}
	}
	if strings.Index(b, "Scope rules") > strings.Index(b, "Errand type") {
		t.Fatal("scope rules not first")
	}
	c := *s
	c.ScopeRules = ScopeRules{}
	if b := c.PromptBlock(food); strings.Contains(b, "Scope rules") {
		t.Fatalf("empty scope rules shown:\n%s", b)
	}
}

// d0.4 decoding: the playbook must be a known id; topic aliases; compare
// and origin only for their topics.
func TestDecodeDiscoveryPlaybookFields(t *testing.T) {
	dec := func(j string) DiscoveryResult {
		t.Helper()
		r, err := decodeDiscovery(j, map[string]Place{}, nil, 0, DiscoveryResult{Usual: -1})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := dec(`{"action":"about","topic":"delivery","choice":1,"playbook":"food","origin":"x","compare":[1,2]}`)
	if r.Topic != "takeout_delivery" || r.Playbook != "food" || r.Origin != "" || r.Compare != nil {
		t.Fatalf("%+v", r)
	}
	r = dec(`{"action":"about","topic":"compare","compare":[1,2,2,9],"playbook":"bogus"}`)
	if len(r.Compare) != 2 || r.Playbook != "" {
		t.Fatalf("%+v", r)
	}
	r = dec(`{"action":"about","topic":"closest","origin":" 250 Mercer St ","playbook":"food"}`)
	if r.Origin != "250 Mercer St" {
		t.Fatalf("%+v", r)
	}
	if r := dec(`{"action":"not_discovery","playbook":"food"}`); r.Playbook != "" {
		t.Fatal("not_discovery kept a playbook")
	}
}

// Web excerpts are kept only when they are about this place.
func TestExcerptAboutPlace(t *testing.T) {
	if ExcerptAboutPlace("the vodka pie, for which the pizzeria is rightfully famous", "11 Best Gluten Free Pizza in NYC", "www.timeout.com", "Nolita Pizza", "") {
		t.Fatal("another pizzeria's excerpt kept")
	}
	if !ExcerptAboutPlace("At Nolita Pizza, get the grandma slice", "Best slices", "timeout.com", "Nolita Pizza", "") {
		t.Fatal("named excerpt dropped")
	}
	if !ExcerptAboutPlace("dan dan noodles", "Menu", "kenmarenoodle.com", "Kenmare Noodle Bar", "kenmarenoodle.com") {
		t.Fatal("own site dropped")
	}
	if !domainAllowed("www.nytimes.com", []string{"nytimes.com"}) || domainAllowed("evilnytimes.com", []string{"nytimes.com"}) {
		t.Fatal("domain match")
	}
}
