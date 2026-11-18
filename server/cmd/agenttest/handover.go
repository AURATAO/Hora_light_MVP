package main

// -handover: replies to a supporter drop-off notice (2026-11-18,
// internal/channel/handover.go). Every reply in
// internal/channel/testdata/handover_replies.json goes the production way:
// the rule first (channel.ReadHandOverByRule), and only what it leaves goes to
// the reader on the small step's model (LLM_SMALL_MODEL, default
// anthropic:claude-sonnet-5-5; -model overrides), no fallback. A case
// passes when the label is one of its "want".
//
//	go run ./cmd/agenttest -handover [-runs 3]

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

func runHandOver(spec llm.Spec, providers llm.Providers, runs int, delay time.Duration) int {
	b, err := os.ReadFile("internal/channel/testdata/handover_replies.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var f struct {
		Notices map[string]string
		Cases   []struct {
			Notice, Text string
			Want         []string
		}
	}
	if err := json.Unmarshal(b, &f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	model := &llm.Model{Primary: providers.Provider(spec)}
	if runs < 1 {
		runs = 1
	}
	fmt.Printf("hand-over replies: %d cases × %d run(s), reader %s (no fallback)\n\n", len(f.Cases), runs, spec)
	pass, total, rule := 0, 0, 0
	var lat []time.Duration
	for _, c := range f.Cases {
		kind := channel.HandOverCancelled
		if strings.HasPrefix(c.Notice, "reassigned") {
			kind = channel.HandOverReassigned
		}
		for run := 0; run < runs; run++ {
			got, by := llm.HandOverReading(""), "rule"
			if r, ok := channel.ReadHandOverByRule(kind, c.Text); ok {
				got = r
				if run == 0 {
					rule++
				}
			} else {
				by = "model"
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				start := time.Now()
				r, err := model.ReadHandOver(ctx, llm.HandOverQuery{Kind: kind, Notice: f.Notices[c.Notice], Text: c.Text})
				cancel()
				lat = append(lat, time.Since(start))
				if err != nil {
					by = "ERROR " + err.Error()
				}
				got = r
				time.Sleep(delay)
			}
			ok := false
			for _, w := range c.Want {
				if string(got) == w {
					ok = true
				}
			}
			total++
			mark := "FAIL"
			if ok {
				pass++
				mark = "ok  "
			}
			if !ok || run == 0 {
				fmt.Printf("%s %-17s %-45q → %-8s (%s) want %v\n", mark, c.Notice, c.Text, got, by, c.Want)
			}
			if by == "rule" {
				break // a rule answers the same every run
			}
		}
	}
	fmt.Printf("\n%d/%d passed; %d decided by rule; model p50 %s\n", pass, total, rule, p50(lat))
	if pass != total {
		return 2
	}
	return 0
}

func p50(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s[len(s)/2].Round(time.Millisecond)
}
