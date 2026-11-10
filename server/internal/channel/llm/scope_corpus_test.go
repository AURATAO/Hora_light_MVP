package llm

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// The scope rules spare everyday errands: across the repo's message
// corpora (routing paraphrases, discovery and agent cases, the parse eval,
// every playbook's eval messages), a rule refuses only the messages below,
// which ask for what HO:RA does not do — and the model is asked about few.
func TestScopeRulesSpareExistingErrands(t *testing.T) {
	s, _ := Playbooks()
	var msgs []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				if str, ok := vv.(string); ok && (k == "msg" || k == "text" || k == "input" || k == "message") {
					msgs = append(msgs, str)
				}
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				if str, ok := vv.(string); ok && strings.Contains(str, " ") && len(str) < 200 {
					msgs = append(msgs, str)
				}
				walk(vv)
			}
		}
	}
	for _, f := range []string{"../testdata/routing_paraphrases.json", "../../../cmd/agenttest/cases.json", "../../../cmd/agenttest/discovery.json", "../../../testdata/chat_parse_eval.json"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		walk(v)
	}
	for _, p := range s.Playbooks {
		for _, e := range p.Evals {
			msgs = append(msgs, e.Msg)
		}
	}
	refusedOK := map[string]string{
		"can someone buy me a bottle of wine and a pack of cigarettes near Union Square":                     "alcohol",
		"pick up my prescription at the CVS on 23rd":                                                         "personal_care",
		"log into my Amazon and reorder my last order":                                                       "id_login",
		"buy flowers at the deli on 5th, pick up my prescription at CVS, and wait in line at the DMV for me": "personal_care",
	}
	seen := map[string]bool{}
	var model []string
	for _, m := range msgs {
		if seen[m] {
			continue
		}
		seen[m] = true
		d := s.ScopeRules.Rule(m)
		switch {
		case d.Refusal != nil && refusedOK[m] != d.Refusal.ID:
			t.Errorf("refused as %s: %q", d.Refusal.ID, m)
		case d.Refusal == nil && refusedOK[m] != "":
			t.Errorf("not refused (want %s): %q", refusedOK[m], m)
		case !d.Decided:
			model = append(model, m)
		}
	}
	sort.Strings(model)
	for _, m := range model {
		t.Logf("to the model: %q", m)
	}
	if len(seen) < 350 || len(model) > len(seen)/50 {
		t.Fatalf("%d messages, %d to the model", len(seen), len(model))
	}
}
