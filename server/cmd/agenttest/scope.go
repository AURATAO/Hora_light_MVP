package main

// -scope: what HO:RA does and does not do about people and homes
// (2026-11-10). Every case in playbooks.json scope_rules.evals goes the
// production way (llm.ScopeRules.Decide): the rules first, and only what
// they leave goes to the small step's model (LLM_SMALL_MODEL, default
// anthropic:claude-sonnet-5-5; -model overrides), with no fallback.
// -scope-all also asks the model on the cases a rule decides, to measure
// the model on its own (production never does).
//
// Grades: allow vs refuse right; the category one of the acceptable ones;
// the ops alert exactly when the category is the romantic/sexual one. That
// a refusal makes no draft and alerts ops is graded through the router by
// go test (internal/channel/scope_test.go).
//
//	go run ./cmd/agenttest -scope
//	go run ./cmd/agenttest -scope -runs 3

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"hora-auth/internal/channel/llm"
)

func runScope(spec llm.Spec, providers llm.Providers, runs int, all bool, delay time.Duration) int {
	set, err := llm.Playbooks()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sr := &set.ScopeRules
	model := &llm.Model{Primary: providers.Provider(spec)}
	if runs < 1 {
		runs = 1
	}
	fmt.Printf("scope %s: %d cases × %d run(s), model %s (no fallback)\n\n", sr.Version, len(sr.Evals), runs, spec)
	type tally struct{ pass, n int }
	byGroup := map[string]*tally{}
	var groups []string
	pass, total, byRule, wrongWay, alertWrong := 0, 0, 0, 0, 0
	var lat []time.Duration
	for run := 1; run <= runs; run++ {
		for _, e := range sr.Evals {
			want := map[string]bool{}
			for _, w := range strings.Split(e.Want, "|") {
				want[w] = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			start := time.Now()
			var d llm.ScopeDecision
			var err error
			rule := sr.Rule(e.Msg)
			if all && rule.Decided {
				// The model alone, for comparison.
				id, merr := model.CheckScope(ctx, e.Msg)
				err = merr
				d = llm.ScopeDecision{Refusal: sr.Refusal(id), By: "model"}
				if id != llm.ScopeAllowID && d.Refusal == nil && err == nil {
					err = fmt.Errorf("unknown verdict %q", id)
				}
			} else {
				d, err = sr.Decide(ctx, model, e.Msg)
			}
			took := time.Since(start)
			cancel()
			how := d.By
			if rule.Decided && !all {
				byRule++
			} else {
				lat = append(lat, took)
				time.Sleep(delay)
			}
			got := d.ID()
			ok := err == nil && want[got]
			wantAllow := want[llm.ScopeAllowID]
			if err == nil && wantAllow != d.Allowed() {
				wrongWay++
			}
			// The alert: on when every acceptable category alerts, off when
			// none does; either way when the case accepts both.
			all, none := true, true
			for w := range want {
				if r := sr.Refusal(w); r != nil && r.Alert {
					none = false
				} else {
					all = false
				}
			}
			alerted := d.Refusal != nil && d.Refusal.Alert
			if err == nil && ((all && !alerted) || (none && alerted)) {
				ok = false
				alertWrong++
			}
			mark := "PASS"
			if ok {
				pass++
			} else {
				mark = "FAIL"
			}
			total++
			if byGroup[e.Group] == nil {
				byGroup[e.Group] = &tally{}
				groups = append(groups, e.Group)
			}
			byGroup[e.Group].n++
			if ok {
				byGroup[e.Group].pass++
			}
			ms := ""
			if !rule.Decided || all {
				ms = fmt.Sprintf(" %5dms", took.Milliseconds())
			}
			errs := ""
			if err != nil {
				errs = " ERROR " + err.Error()
			}
			fmt.Printf("%s %-15s %-26s got %-16s %-24s%s%s  %q\n", mark, e.Group, e.Want, got, how, ms, errs, e.Msg)
		}
	}
	fmt.Printf("\nscope: %d/%d pass; %d decided by rule (no model call), %d by the model; allow/refuse wrong %d; alert wrong %d; %s\n",
		pass, total, byRule, total-byRule, wrongWay, alertWrong, latSummary(lat))
	for _, g := range groups {
		fmt.Printf("  %-16s %d/%d\n", g, byGroup[g].pass, byGroup[g].n)
	}
	if pass != total {
		return 1
	}
	return 0
}
