package main

// -address: answers to HO:RA's address question (2026-11-12). Reads
// internal/channel/testdata/address_followups.json:
//
//   - guesses: the address model (LLM_SMALL_MODEL, default Sonnet; -model
//     overrides) on what was said — the guess must contain the expected
//     words, or be null where no single address is likely;
//   - transcripts: with CHANNEL_PARSE_PROMPT_FILE set (the production parse
//     prompt, aiTaskParserSystemPrompt, dumped to a file), the parse model
//     (LLM_PARSE_MODEL) on everything said after each message, -runs times:
//     a placeholder or empty title is counted (production 2026-11-12 had
//     "" then "x"); the router keeps the pending title on those, so they
//     are reported, not failed.
//
//	go run ./cmd/agenttest -address -runs 3

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

type addressFile struct {
	Transcripts []struct {
		ID       string   `json:"id"`
		Messages []string `json:"messages"`
	} `json:"transcripts"`
	Guesses []struct {
		Said    []string `json:"said"`
		Unclear string   `json:"unclear"`
		Area    string   `json:"area"`
		Want    []string `json:"want"`
		Null    bool     `json:"null"`
		OrNull  bool     `json:"or_null"`
	} `json:"guesses"`
}

func runAddress(spec llm.Spec, providers llm.Providers, runs int, delay time.Duration) int {
	b, err := os.ReadFile("internal/channel/testdata/address_followups.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var f addressFile
	if err := json.Unmarshal(b, &f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if runs < 1 {
		runs = 1
	}
	model := &llm.Model{Primary: providers.Provider(spec)}
	fmt.Printf("address guesses: %d cases × %d runs, %s (no fallback)\n\n", len(f.Guesses), runs, spec)
	pass, total := 0, 0
	var lat []time.Duration
	for _, c := range f.Guesses {
		for run := 0; run < runs; run++ {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			start := time.Now()
			got, err := model.GuessAddress(ctx, c.Said, c.Unclear, c.Area)
			d := time.Since(start)
			cancel()
			lat = append(lat, d)
			ok := err == nil
			switch {
			case !ok:
			case c.Null || (c.OrNull && got == ""):
				ok = got == ""
			default:
				for _, w := range c.Want {
					ok = ok && strings.Contains(strings.ToLower(got), strings.ToLower(w))
				}
			}
			total++
			mark := "FAIL"
			if ok {
				pass++
				mark = "PASS"
			}
			shown := got
			if err != nil {
				shown = "ERROR " + err.Error()
			} else if got == "" {
				shown = "null"
			}
			fmt.Printf("%s %5dms  %-22q → %s\n", mark, d.Milliseconds(), c.Unclear, shown)
			time.Sleep(delay)
		}
	}
	fmt.Printf("\nguesses: %d/%d pass; %s\n", pass, total, latSummary(lat))

	code := 0
	if pass != total {
		code = 2
	}
	pf := os.Getenv("CHANNEL_PARSE_PROMPT_FILE")
	if pf == "" {
		fmt.Println("\ntranscript parses: skipped (set CHANNEL_PARSE_PROMPT_FILE to the production parse prompt)")
		return code
	}
	prompt, err := os.ReadFile(pf)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	pspec, err := llm.ParseSpec(orDefault(os.Getenv("LLM_PARSE_MODEL"), "anthropic:"+llm.DefaultAnthropicModel))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	parser := &llm.Model{Primary: providers.Provider(pspec)}
	fmt.Printf("\ntranscript parses: everything said after each message, %d runs, %s\n\n", runs, pspec)
	bad, n := 0, 0
	for _, tr := range f.Transcripts {
		for i := range tr.Messages {
			source := strings.Join(tr.Messages[:i+1], "\n")
			for run := 0; run < runs; run++ {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				d, _, err := parser.ParseTask(ctx, string(prompt), source)
				cancel()
				n++
				title := d.Title
				mark := "ok  "
				if err != nil || channel.PlaceholderTitle(title) {
					bad++
					mark = "HELD" // the router keeps the pending title / fills the answer by rule
					if err != nil {
						title = "ERROR " + err.Error()
					}
				}
				fmt.Printf("%s %s #%d  title=%q  where=%q → %q\n", mark, tr.ID, i+1, title, d.Location1, d.Location2)
				time.Sleep(delay)
			}
		}
	}
	fmt.Printf("\ntranscript parses: %d/%d with a placeholder or no title (kept by the router's rule, never shown)\n", bad, n)
	return code
}
