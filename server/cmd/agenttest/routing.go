package main

// -routing: search vs errand (2026-11-07). Every phrasing in
// internal/channel/testdata/routing_paraphrases.json goes the production
// way: the rules first (channel.LooksLikeDiscovery → a search,
// channel.IsClearErrand → an errand, no model call), and only what they
// leave goes to the classifier on the small step's model (LLM_SMALL_MODEL,
// default anthropic:claude-sonnet-5-5; -model overrides), with no fallback.
// Then the reply-to-"what to order?" cases (ReadsAsQuestion).
//
//	go run ./cmd/agenttest -routing
//	go run ./cmd/agenttest -routing -model baseten:zai-org/GLM-5.3-Flash   # the old small model, for comparison

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

type routingCase struct {
	Text, Want string // Want: find | errand
}

// askBackCases: replies to "What should your supporter order there?".
var askBackCases = []struct {
	Reply string
	Back  bool
}{
	{"What kind of pizza can I get? Any recommendations?", true},
	{"not sure, what do people usually get there?", true},
	{"hmm what's good?", true},
	{"do they have anything vegan?", true},
	{"is the margherita any good?", true},
	{"can you get me a pepperoni?", false},
	{"2 plain slices, is that ok?", false},
	{"maybe a margherita?", false},
	{"one large cheese pie please", false},
	{"whatever's popular, surprise me", false},
}

func runRouting(spec llm.Spec, providers llm.Providers, delay time.Duration) int {
	b, err := os.ReadFile("internal/channel/testdata/routing_paraphrases.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var p struct {
		Find   []string `json:"find"`
		Errand []string `json:"errand"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var cases []routingCase
	for _, s := range p.Find {
		cases = append(cases, routingCase{s, "find"})
	}
	for _, s := range p.Errand {
		cases = append(cases, routingCase{s, "errand"})
	}
	model := &llm.Model{Primary: providers.Provider(spec)}
	fmt.Printf("routing: %d phrasings, classifier %s (no fallback)\n\n", len(cases), spec)
	pass, rule, wrong := 0, 0, 0
	var lat []time.Duration
	for _, c := range cases {
		got, how := "", "rule"
		var d time.Duration
		switch {
		case channel.LooksLikeDiscovery(c.Text):
			got = "find"
		case channel.IsClearErrand(c.Text):
			got = "errand"
		default:
			how = "model"
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			start := time.Now()
			cl, err := model.ClassifyDetail(ctx, c.Text)
			d = time.Since(start)
			cancel()
			lat = append(lat, d)
			switch {
			case err != nil:
				got = "ERROR " + err.Error()
			case cl.Intent == llm.IntentFind:
				got = "find"
			case cl.Intent == llm.IntentNewTask:
				got = "errand"
			default:
				got = string(cl.Intent)
			}
			if cl.Playbook != "" {
				how += " pb=" + cl.Playbook
			}
			time.Sleep(delay)
		}
		if how == "rule" {
			rule++
		}
		mark := "PASS"
		if got == c.Want {
			pass++
		} else {
			mark = "FAIL"
			wrong++
		}
		ms := ""
		if d > 0 {
			ms = fmt.Sprintf(" %5dms", d.Milliseconds())
		}
		fmt.Printf("%s %-6s %-6s %-16s%s  %q\n", mark, c.Want, got, how, ms, c.Text)
	}
	fmt.Printf("\nsearch vs errand: %d/%d pass, %d by rule (no model call), %d to the model; %s\n", pass, len(cases), rule, len(cases)-rule, latSummary(lat))

	fmt.Printf("\nasked back (reply to \"What should your supporter order there?\"): rule, else %s\n", spec)
	askQ := "What should your supporter order there? Or reply SKIP."
	apass := 0
	var alat []time.Duration
	for _, c := range askBackCases {
		got, how := channel.OrderQuestion(c.Reply), "rule"
		var d time.Duration
		if !got {
			how = "model"
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			start := time.Now()
			yes, err := model.ReadsAsQuestion(ctx, askQ, c.Reply)
			d = time.Since(start)
			cancel()
			alat = append(alat, d)
			if err != nil {
				how = "ERROR " + err.Error()
			}
			got = yes
			time.Sleep(delay)
		}
		mark := "PASS"
		if got == c.Back {
			apass++
		} else {
			mark = "FAIL"
			wrong++
		}
		fmt.Printf("%s back=%-5t got=%-5t %-6s %5dms  %q\n", mark, c.Back, got, how, d.Milliseconds(), c.Reply)
	}
	fmt.Printf("\nasked back: %d/%d pass; %s\n", apass, len(askBackCases), latSummary(alat))
	if wrong > 0 {
		return 1
	}
	return 0
}

func latSummary(ds []time.Duration) string {
	if len(ds) == 0 {
		return "no model calls"
	}
	s := append([]time.Duration{}, ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	pct := func(q float64) time.Duration { return s[int(q*float64(len(s)-1)+0.5)] }
	return fmt.Sprintf("model latency p50 %dms p90 %dms max %dms (n=%d)", pct(0.5).Milliseconds(), pct(0.9).Milliseconds(), s[len(s)-1].Milliseconds(), len(s))
}
