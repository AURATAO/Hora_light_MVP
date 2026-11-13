//go:build liveeval

package channel

// D-30 on the live model (LLM_SMALL_MODEL, default Sonnet): the ops notes
// of the request, read as edits; then the 2026-11-12 transcript, handed off,
// fixed with a note and handed back — every reply printed. The map is the
// stub of address_live_test.go.
//
//	CHANNEL_PARSE_PROMPT_FILE=<aiTaskParserSystemPrompt dumped> go test -tags liveeval -run TestLiveOps -v ./internal/channel

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

func TestLiveOpsEditNotes(t *testing.T) {
	prov := llm.ProvidersFromEnv()
	if prov.AnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}
	steps := llm.StepsFromEnv(prov, func(s string) { t.Log("warn:", s) })
	nyc, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 11, 12, 16, 58, 0, 0, nyc)
	draft := llm.TaskDraft{Title: "Pick up and deliver medicine", Category: "delivery", Location2: "335 6 st 3 ave",
		Description: "Requester needs medicine picked up. No specific medication, pharmacy, or prescription details were provided, so confirm with the requester before starting. The requester is in the East Village near 6th St and 3rd Ave."}
	for _, c := range []struct {
		note  string
		check func(e llm.DraftEdit) string
	}{
		{"address: 335 E 6th St, New York, NY 10003", func(e llm.DraftEdit) string {
			if e.DropOff == nil || !strings.Contains(*e.DropOff, "335 E 6th St") || e.AsksToBook {
				return "drop-off not set"
			}
			return ""
		}},
		{"it's Advil, 1 box, from any pharmacy", func(e llm.DraftEdit) string {
			if e.Title == nil || !strings.Contains(strings.ToLower(*e.Title), "advil") || e.Pickup != nil || e.DropOff != nil {
				return "title / no fixed pickup"
			}
			return ""
		}},
		{"time: today 6pm", func(e llm.DraftEdit) string {
			if e.When == nil || !strings.HasPrefix(*e.When, "2026-11-12T18:00") {
				return "time"
			}
			return ""
		}},
		{"budget $20", func(e llm.DraftEdit) string {
			if e.BudgetCents == nil || *e.BudgetCents != 2000 {
				return "budget"
			}
			return ""
		}},
		{"customer said yes on the phone, just confirm the booking for her", func(e llm.DraftEdit) string {
			if !e.AsksToBook {
				return "asks_to_book not set"
			}
			return ""
		}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		start := time.Now()
		e, err := steps.Small.EditDraft(ctx, llm.DraftEditInput{Draft: draft, Note: c.note, Now: now})
		cancel()
		if err != nil {
			t.Fatalf("%q: %v", c.note, err)
		}
		t.Logf("%5dms %q → %s", time.Since(start).Milliseconds(), c.note, editString(e))
		if why := c.check(e); why != "" {
			t.Errorf("%q: %s", c.note, why)
		}
	}
}

func editString(e llm.DraftEdit) string {
	var b []string
	add := func(k string, v *string) {
		if v != nil {
			b = append(b, k+"="+*v)
		}
	}
	add("title", e.Title)
	add("pickup", e.Pickup)
	add("drop_off", e.DropOff)
	add("when", e.When)
	add("note", e.SupportNote)
	if e.BudgetCents != nil {
		b = append(b, "budget_cents="+strings.TrimSpace(Dollars(*e.BudgetCents)))
	}
	if e.Description != nil {
		b = append(b, "description="+*e.Description)
	}
	if e.AsksToBook {
		b = append(b, "ASKS_TO_BOOK")
	}
	return strings.Join(b, " | ")
}

func TestLiveOpsTranscript(t *testing.T) {
	prov := llm.ProvidersFromEnv()
	pf := os.Getenv("CHANNEL_PARSE_PROMPT_FILE")
	if prov.AnthropicKey == "" || pf == "" {
		t.Skip("ANTHROPIC_API_KEY / CHANNEL_PARSE_PROMPT_FILE not set")
	}
	prompt, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	steps := llm.StepsFromEnv(prov, func(s string) { t.Log("warn:", s) })
	pbs, _ := llm.Playbooks()
	g := newRig(llm.IntentNewTask)
	g.r.Parser, g.r.Splitter = steps.Parse, steps.Parse
	g.r.Classifier, g.r.InContext, g.r.QuestionReader = steps.Small, steps.Small, steps.Small
	g.r.Scope, g.r.Purchases, g.r.OpsEditor = steps.Small, steps.Small, steps.Small
	g.r.Prompt = func(time.Time) string { return string(prompt) }
	g.r.Playbooks, g.r.Ops = pbs, &memOps{}
	g.booker.area = stubMap
	g.ids.link(me, "user-1")
	for _, m := range []string{efMsg1, efMsg2, efMsg3, efMsg4} {
		r := g.say(t, m)
		t.Logf("customer: %s\n   HO:RA: %s", m, r)
	}
	if g.state() != StateHandoff {
		// With the address model off, the old limit hands off; force one
		// the way the customer would ("real person").
		t.Logf("customer: real person\n   HO:RA: %s", g.say(t, "real person"))
	}
	ctx := context.Background()
	for _, note := range []string{"address: 335 E 6th St, New York, NY 10003", "it's Advil, 1 box, from any pharmacy, budget $20"} {
		res, err := g.r.OpsEditDraft(ctx, g.ident(), note)
		if err != nil {
			t.Fatalf("%q: %v", note, err)
		}
		for _, c := range res.Changes {
			t.Logf("ops note %q: %s: %q → %q", note, c.Field, c.Before, c.After)
		}
		t.Logf("ready=%v missing=%q", res.Ready, res.Missing)
	}
	what, err := g.r.OpsHandBack(ctx, g.ident())
	if err != nil {
		t.Fatal(err)
	}
	n := len(g.ad.sent)
	t.Logf("hand back (%s):\n   HO:RA: %s\n   HO:RA: %s", what, g.ad.sent[n-2].Text, g.ad.sent[n-1].Text)
	if g.booker.posts != 0 {
		t.Fatal("booked without YES")
	}
	if what == "summary" && !strings.Contains(g.ad.sent[n-1].Text, "Reply YES to book") {
		t.Fatal("no YES line")
	}
}
