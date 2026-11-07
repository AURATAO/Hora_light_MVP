//go:build liveeval

package channel

// Live latency of the 2026-11-07 messages through the real router: the
// classifier (LLM_SMALL_MODEL), the splitter and parser (LLM_PARSE_MODEL)
// and discovery (LLM_AGENT_MODEL) on real models, with in-memory stores.
// Google Places is a fixture with a simulated delay (PLACES_DELAY, default
// 600ms per search) and the address check answers inside at once, so the
// numbers are the models' plus the router's. It costs money; it only builds
// with the tag:
//
//	set -a; source .env; set +a
//	CHANNEL_PARSE_PROMPT_FILE=/path/to/prompt.txt LLM_SMALL_MODEL=anthropic:claude-sonnet-5-5 \
//	  go test -tags liveeval -run TestLiveLatency -v ./internal/channel
//
// CHANNEL_PARSE_PROMPT_FILE holds aiTaskParserSystemPrompt's text (package
// main, ai_task_parser.go); without it a short prompt is used and said so.
// Targets: a search under 10 s to its answer, an errand under 6 s to its
// draft's first question or summary.

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

type delayedPlaces struct {
	d time.Duration
}

func (p delayedPlaces) SearchPlaces(ctx context.Context, q string, openNow bool) ([]llm.Place, error) {
	time.Sleep(p.d)
	kind := "Place"
	for _, k := range []string{"pizz", "clean", "laundr", "pharmac", "bagel", "tailor", "bakery", "hardware"} {
		if strings.Contains(strings.ToLower(q), k) {
			kind = k
		}
	}
	t := true
	return []llm.Place{
		{ID: "L1-" + kind, Name: "First " + kind, Address: "68 Kenmare St, Manhattan", FullAddress: "68 Kenmare St, New York, NY 10012", Rating: 4.6, RatingCount: 1200, OpenNow: &t, ClosesAt: "10 PM", InNYC: true},
		{ID: "L2-" + kind, Name: "Second " + kind, Address: "27 Prince St, Manhattan", FullAddress: "27 Prince St, New York, NY 10012", Rating: 4.4, RatingCount: 800, OpenNow: &t, ClosesAt: "9 PM", InNYC: true},
		{ID: "L3-" + kind, Name: "Third " + kind, Address: "150 Spring St, Manhattan", FullAddress: "150 Spring St, New York, NY 10012", Rating: 4.2, RatingCount: 300, OpenNow: &t, ClosesAt: "11 PM", InNYC: true},
	}, nil
}
func (p delayedPlaces) PlaceDetails(_ context.Context, id string) (llm.Place, error) {
	return llm.Place{ID: id, Name: id, FullAddress: "68 Kenmare St, New York, NY 10012", InNYC: true}, nil
}
func (p delayedPlaces) AboutDetails(ctx context.Context, id string) (llm.Place, error) {
	return p.PlaceDetails(ctx, id)
}
func (p delayedPlaces) PlaceName(_ context.Context, id string) (string, error) { return id, nil }

func TestLiveLatency(t *testing.T) {
	prov := llm.ProvidersFromEnv()
	if prov.AnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	steps := llm.StepsFromEnv(prov, func(s string) { t.Log("warn:", s) })
	t.Logf("small=%s parse=%s agent=%s", steps.Small.Name(), steps.Parse.Name(), steps.Agent.Name())
	prompt := "Extract the errand as JSON for the task form."
	if f := os.Getenv("CHANNEL_PARSE_PROMPT_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		prompt = string(b)
	} else {
		t.Log("CHANNEL_PARSE_PROMPT_FILE not set: a short parse prompt (parse latency is low)")
	}
	pd := time.Duration(600) * time.Millisecond
	if v := os.Getenv("PLACES_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			pd = d
		}
	}
	agentSpec, _ := llm.ParseSpec(firstNonEmpty(os.Getenv("LLM_AGENT_MODEL"), "anthropic:"+llm.DefaultAnthropicModel))
	disc, err := prov.Discovery(agentSpec, delayedPlaces{d: pd})
	if err != nil {
		t.Fatal(err)
	}
	pbs, _ := llm.Playbooks()

	type run struct {
		kind, msg string
		last      time.Duration
		outcome   string
	}
	var runs []run
	one := func(kind, msg string) {
		g := newDiscoRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.r.Go = nil
		g.r.Classifier, g.r.Parser, g.r.Splitter, g.r.QuestionReader = steps.Small, steps.Parse, steps.Parse, steps.Small
		g.r.Prompt = func(time.Time) string { return prompt }
		g.r.Discovery, g.r.Places, g.r.Playbooks = disc, delayedPlaces{d: pd}, pbs
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		in := Inbound{Channel: IMessage, ProviderMessageID: msg + start.String(), From: me, Text: msg, ReceivedAt: start}
		if err := g.r.HandleInbound(ctx, in); err != nil {
			t.Errorf("%q: %v", msg, err)
			return
		}
		last := time.Since(start)
		outcome := "search"
		if c := g.conv(t); c.Pending != nil {
			outcome = "draft (" + string(c.State) + ")"
		}
		runs = append(runs, run{kind, msg, last, outcome})
		t.Logf("%-6s %6dms  %-24s %q", kind, last.Milliseconds(), outcome, msg)
	}
	searches := []string{prodPizza, prodSoho, prodDryCleaner, "pizza places in soho?", "need a hardware store close to Prospect Park", "know any good bagel places in the East Village?"}
	errands := []string{"pick up my dry cleaning at 12 Bedford Ave at 5pm", "get me two bagels from Ess-a-Bagel at 831 3rd Ave",
		"can someone sit with my grandmother for two hours tomorrow at 10, 300 E 10th St", "take my suit to the dry cleaners at 120 E 23rd St",
		"I need you to mail a package at the post office on 3rd Ave"}
	for _, m := range searches {
		one("search", m)
	}
	for _, m := range errands {
		one("errand", m)
	}
	report := func(kind string, budget time.Duration) {
		var ds []time.Duration
		bad := 0
		for _, r := range runs {
			if r.kind != kind {
				continue
			}
			ds = append(ds, r.last)
			if (kind == "search") != (r.outcome == "search") {
				t.Errorf("%q: %s, want a %s", r.msg, r.outcome, kind)
			}
			if r.last > budget {
				bad++
			}
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		if len(ds) == 0 {
			return
		}
		t.Logf("%s: median %dms max %dms; %d of %d over %s", kind, ds[len(ds)/2].Milliseconds(), ds[len(ds)-1].Milliseconds(), bad, len(ds), budget)
	}
	report("search", 10*time.Second)
	report("errand", 6*time.Second)
}
