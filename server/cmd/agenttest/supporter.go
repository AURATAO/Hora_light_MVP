package main

// -supporter: people who want to WORK as a supporter (2026-11-10,
// internal/channel/supporter.go). Every phrasing in
// internal/channel/testdata/supporter_phrasings.json goes the production
// way: the rule first (channel.SupporterInterest), and only what it leaves
// goes to the classifier on the small step's model (LLM_SMALL_MODEL,
// default anthropic:claude-sonnet-5-5; -model overrides), no fallback.
//
//	interest  → supporter_interest (by rule or by the model)
//	errand    → never supporter_interest (near-misses: "I need someone to
//	            work on my furniture assembly")
//	mixed     → the rule splits off the errand words (no model call)
//
//	go run ./cmd/agenttest -supporter [-runs 3]

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

func runSupporter(spec llm.Spec, providers llm.Providers, runs int, delay time.Duration) int {
	b, err := os.ReadFile("internal/channel/testdata/supporter_phrasings.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var p struct {
		Interest []struct{ Msg, By string }
		Errand   []string
		Mixed    []struct{ Msg, Rest string }
	}
	if err := json.Unmarshal(b, &p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	model := &llm.Model{Primary: providers.Provider(spec)}
	if runs < 1 {
		runs = 1
	}
	fmt.Printf("supporter interest: %d interest, %d errand, %d mixed × %d run(s), classifier %s (no fallback)\n\n", len(p.Interest), len(p.Errand), len(p.Mixed), runs, spec)
	pass, total, rule := 0, 0, 0
	var lat []time.Duration
	classify := func(msg string) (llm.Classification, time.Duration, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		start := time.Now()
		c, err := model.ClassifyDetail(ctx, msg)
		d := time.Since(start)
		lat = append(lat, d)
		time.Sleep(delay)
		return c, d, err
	}
	mark := func(ok bool) string {
		total++
		if ok {
			pass++
			return "PASS"
		}
		return "FAIL"
	}
	for run := 0; run < runs; run++ {
		for _, c := range p.Interest {
			if channel.SupporterInterest(c.Msg) {
				rule++
				fmt.Printf("%s interest rule                    %q\n", mark(true), c.Msg)
				continue
			}
			cl, d, err := classify(c.Msg)
			ok := err == nil && cl.Intent == llm.IntentSupporter
			fmt.Printf("%s interest model %-18s %5dms %q\n", mark(ok), cl.Intent, d.Milliseconds(), c.Msg)
		}
		for _, m := range p.Errand {
			if channel.SupporterInterest(m) {
				rule++
				fmt.Printf("%s errand   rule says supporter        %q\n", mark(false), m)
				continue
			}
			cl, d, err := classify(m)
			ok := err == nil && cl.Intent != llm.IntentSupporter
			fmt.Printf("%s errand   model %-18s %5dms %q\n", mark(ok), cl.Intent, d.Milliseconds(), m)
		}
		for _, m := range p.Mixed {
			rest, mixed := channel.SplitSupporterInterest(m.Msg)
			ok := channel.SupporterInterest(m.Msg) && mixed && strings.TrimRight(rest, ".?! ") == strings.TrimRight(m.Rest, ".?! ")
			rule++
			fmt.Printf("%s mixed    rule rest=%q  %q\n", mark(ok), rest, m.Msg)
		}
	}
	fmt.Printf("\nsupporter interest: %d/%d pass; %d by rule (no model call), %d to the model; %s\n", pass, total, rule, total-rule, latSummary(lat))
	if pass != total {
		return 1
	}
	return 0
}
