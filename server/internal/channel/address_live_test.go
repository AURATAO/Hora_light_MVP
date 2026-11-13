//go:build liveeval

package channel

// The 2026-11-12 transcripts through the whole router on the live models
// (parse, split, classify, scope, purchase, address guess — the production
// steps from env), with the map stubbed: a neighbourhood is a neighbourhood,
// a full "number street, New York, NY 100xx" address is inside, anything
// else can't be placed. Prints every reply.
//
//	CHANNEL_PARSE_PROMPT_FILE=<aiTaskParserSystemPrompt dumped> go test -tags liveeval -run TestLiveAddressFollowup -v ./internal/channel

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

var stubFullAddressRE = regexp.MustCompile(`(?i)^\d+[a-z]? (?:[EW] )?\d+(?:st|nd|rd|th) (?:St|Street), New York, NY 100\d\d$`)

func stubMap(d llm.TaskDraft) AreaCheck {
	var resolved []string
	for _, s := range []string{d.Location1, d.Location2} {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			continue
		case strings.Contains(strings.ToLower(s), "east village") && !regexp.MustCompile(`\d`).MatchString(s):
			// Google: a neighbourhood, however it is written.
			return AreaCheck{Outcome: AreaUnclear, Stop: s, Area: "East Village"}
		case stubFullAddressRE.MatchString(s):
			resolved = append(resolved, s)
		default:
			return AreaCheck{Outcome: AreaUnclear, Stop: s}
		}
	}
	return AreaCheck{Outcome: AreaInside, Resolved: resolved}
}

func TestLiveAddressFollowup(t *testing.T) {
	prov := llm.ProvidersFromEnv()
	if prov.AnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	pf := os.Getenv("CHANNEL_PARSE_PROMPT_FILE")
	if pf == "" {
		t.Skip("CHANNEL_PARSE_PROMPT_FILE not set (the production parse prompt)")
	}
	prompt, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	steps := llm.StepsFromEnv(prov, func(s string) { t.Log("warn:", s) })
	pbs, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/address_followups.json")
	if err != nil {
		t.Fatal(err)
	}
	var f addressFollowups
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, tr := range f.Transcripts {
		g := newRig(llm.IntentNewTask)
		g.r.Parser, g.r.Splitter = steps.Parse, steps.Parse
		g.r.Classifier, g.r.InContext, g.r.QuestionReader = steps.Small, steps.Small, steps.Small
		g.r.Scope, g.r.Purchases, g.r.Addresses = steps.Small, steps.Small, steps.Small
		g.r.Prompt = func(time.Time) string { return string(prompt) }
		g.r.Playbooks = pbs
		g.r.Ops = &memOps{}
		g.booker.area = stubMap
		t.Logf("── %s (parse %s, small %s)", tr.ID, steps.Parse.Name(), steps.Small.Name())
		for i, m := range tr.Messages {
			r := g.say(t, m)
			t.Logf("%d. customer: %s\n   HO:RA:    %s", i+1, m, strings.ReplaceAll(r, "\n", "\n             "))
			switch {
			case r == CopyParseFailed:
				t.Errorf("%d: the request was lost (%q)", i+1, r)
			case strings.Contains(r, "“x”") || strings.Contains(r, "“”"):
				t.Errorf("%d: a placeholder title was shown: %q", i+1, r)
			case g.state() == StateHandoff:
				t.Errorf("%d: handed off", i+1)
			}
			if p := g.pending(t); p == nil {
				t.Errorf("%d: no open request", i+1)
			} else {
				t.Logf("   draft: title=%q where=%q → %q area=%q unclear=%q guess=%q tries=%d", p.Draft.Title, p.Draft.Location1, p.Draft.Location2, p.Area, p.UnclearStop, p.AddressGuess, p.DetailsTries)
			}
		}
		if p := g.pending(t); p != nil && p.AddressGuess != "" {
			// Not part of the transcript: what a YES to the guess leads to.
			r := g.say(t, "YES")
			t.Logf("(then) customer: YES\n   HO:RA:    %s", strings.ReplaceAll(r, "\n", "\n             "))
			if p := g.pending(t); p != nil {
				t.Logf("   draft: title=%q where=%q → %q area=%q details=%v\n   description=%q", p.Draft.Title, p.Draft.Location1, p.Draft.Location2, p.Area, p.DetailsAsked, p.Draft.Description)
			}
		}
	}
}
