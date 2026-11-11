package main

// -voice: the voice layer (D-28, internal/channel/voice.go). Each case is a
// reply the router really sends, built from the channel's own copy
// (Copy* constants, StatusText) with realistic values, plus the customer's
// message it answers. Each goes through Router.VoiceRewrite with the small
// step's model (LLM_SMALL_MODEL, default anthropic:claude-sonnet-5-5; no
// fallback) and a generous deadline, so the latency measured is the
// model's own; the fallback rate is then given for the production timeout.
//
//	go run ./cmd/agenttest -voice [-runs 2] [-v]
//
// Reported: how often the rule check passed a model answer, the fallback
// rate (rule check, errors, and at the production timeout), median and
// p95 latency, and before/after pairs.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"hora-auth/internal/channel"
	"hora-auth/internal/channel/llm"
)

type voiceCase struct {
	Kind     string
	Customer string
	Text     string
}

func voiceCases() []voiceCase {
	refusal := ""
	if s, err := llm.Playbooks(); err == nil && len(s.ScopeRules.Refusals) > 0 {
		refusal = s.ScopeRules.Refusals[0].Reply
	}
	status := func(typ, title, who, logged, cost string) string {
		t, _ := channel.StatusText(channel.StatusEvent{Type: typ, TaskTitle: title, SupporterName: who, TotalLogged: logged, FinalCost: cost})
		return t
	}
	summary := "Laundry pickup\nTomorrow, 3:00 PM · about 45 min\n12 Bedford Ave, Brooklyn\nEstimate: $38 (you pay for the time actually used)\n" +
		fmt.Sprintf(channel.CopyAutoExtendOn, 15) + "\n\nReply YES to book, or tell me what to change.\nBy booking you agree to our Terms: " + channel.TermsURL + "\n" + channel.CopyCancelHint
	return []voiceCase{
		// Greetings and drafts.
		{"greeting", "hi", channel.CopyWelcome},
		{"greeting", "hey again", channel.CopyHelloAgain},
		{"draft", "can someone pick up my dry cleaning from Hudson Cleaners tomorrow at 10am", fmt.Sprintf(channel.CopyAskName, "Pick up dry cleaning from Hudson Cleaners")},
		{"draft", "grab me some groceries from Trader Joe's on 14th st", channel.CopyAskBudget},
		{"draft", "$30 i guess? not sure", channel.CopyAskBudgetAgain},
		{"draft", "I need help with something", channel.CopyAskWhatWhere},
		{"draft", "pick up my package asap", fmt.Sprintf(channel.CopyAskWhere, "Package pickup")},
		{"draft", "allow 15 extra min", fmt.Sprintf(channel.CopyExtraTimeAck, 15)},
		{"draft", "YES", channel.CopyBooked},
		{"draft", "no actually forget it", channel.CopyDropped},
		{"draft", "Maria", channel.CopyAskNameAgain},
		{"draft", "laundry tomorrow 3pm 12 bedford ave", summary},
		// Follow-up questions.
		{"follow_up", "pick up from 350 broadway", fmt.Sprintf(channel.CopyWhichBorough, "350 Broadway")},
		{"follow_up", "deliver to 55 water", fmt.Sprintf(channel.CopyAskClearerAddress, "55 water")},
		{"follow_up", "find me a pharmacy that's open", channel.CopyAskLocation},
		{"follow_up", "the laundry one", channel.CopyWhichTask},
		// Discovery answers.
		{"discovery", "any good bakeries near me that do gluten free?", channel.CopyDiscoveryNothing},
		{"discovery", "what's the best thing at Levain?", fmt.Sprintf(channel.CopyAboutMenuItem, "Levain Bakery", "cookie") + " " + fmt.Sprintf(channel.CopyAboutAsk, "Levain Bakery")},
		{"discovery", "do they have a line right now?", channel.CopyLiveUnknown + "\n\n" + fmt.Sprintf(channel.CopyOfferCheck, "Joe's Pizza")},
		{"discovery", "what do people order at Katz's", "Reviews mention their pastrami on rye.\n\n" + fmt.Sprintf(channel.CopyOfferItems, "a pastrami on rye", "Katz's Delicatessen") + "\n" + channel.CopyFromGoogle},
		// Status updates.
		{"status", "", status("ORDER_ACCEPTED", "Laundry pickup", "Alex Rivera", "", "")},
		{"status", "", status("ON_MY_WAY", "Grocery run", "Bea", "", "")},
		{"status", "", status("SUPPORTER_ARRIVED", "Grocery run", "Bea", "", "")},
		{"status", "", status("CLOCK_OUT", "Laundry pickup", "Alex", "42 min", "")},
		{"status", "", status("COMPLETED", "Laundry pickup", "Alex", "42 min", "$35.50")},
		// Refusals and handoffs.
		{"refusal", "can someone pick me up some beer", refusal},
		{"refusal", "pick up a package in Jersey City", channel.CopyOutOfArea},
		{"refusal", "[voice memo]", channel.CopyTextOnly},
		{"handoff", "I want to talk to a real person", channel.CopyHandoffStart},
		{"handoff", "hello??", channel.CopyHandoffTimedOut},
		// Errors.
		{"error", "YES", channel.CopyPostFailed},
		{"error", "tell her the code is 4521", channel.CopyRelayFailed},
		{"error", "deliver to 200 e 10th st", channel.CopyAreaCheckDown},
		{"error", "I need a person NOW this is ridiculous", channel.CopyHandoffFailed},
		{"error", "cancel task 1 please", fmt.Sprintf(channel.CopyCancelFailed, "Grocery run")},
		{"cancel", "2", fmt.Sprintf(channel.CopyCancelledFree, "Grocery run")},
		// Stressed customer, and another language.
		{"stressed", "where is my supporter?? I've been waiting forever, I'm going to be late", fmt.Sprintf(channel.CopyWhereUnavailableLine, "Bea", "") + channel.CopyWhereAsked},
		{"stressed", "I was charged twice?? what is going on", channel.CopyHandoffStart},
		{"spanish", "hola, necesito que alguien recoja mi ropa de la lavandería mañana", fmt.Sprintf(channel.CopyAskName, "Recoger ropa de la lavandería")},
		{"spanish", "recoger un paquete en 350 broadway", fmt.Sprintf(channel.CopyWhichBorough, "350 Broadway")},
		{"chinese", "你好，有人能帮我取快递吗？", channel.CopyAskWhatWhere},
	}
}

func runVoice(spec llm.Spec, providers llm.Providers, runs int, verbose bool, delay time.Duration) int {
	model := &llm.Model{Primary: providers.Provider(spec)}
	r := &channel.Router{Voice: model, VoiceTimeout: 15 * time.Second}
	cases := voiceCases()
	if runs < 1 {
		runs = 1
	}
	fmt.Printf("voice: %d replies × %d run(s), %s (no fallback), prompt %s; production timeout %s\n\n", len(cases), runs, spec, llm.VoicePromptVersion, channel.DefaultVoiceTimeout)
	type row struct {
		c   voiceCase
		res channel.VoiceResult
	}
	var rows []row
	var lat []time.Duration
	outcomes := map[string]int{}
	asked, passed, errs := 0, 0, 0
	for run := 0; run < runs; run++ {
		for _, c := range cases {
			ctx := channel.WithCustomerWordsForEval(context.Background(), c.Customer)
			res := r.VoiceRewrite(ctx, channel.Outbound{Channel: channel.IMessage, To: "+15550000000", Text: c.Text})
			rows = append(rows, row{c, res})
			outcomes[res.Outcome+"/"+res.Reason]++
			if res.Outcome != channel.VoiceFixed {
				asked++
				lat = append(lat, res.Latency)
				switch {
				case res.Outcome == channel.VoiceRewritten:
					passed++
				case res.Reason == "error" || res.Reason == "timeout":
					errs++
				}
			}
			mark := map[string]string{channel.VoiceRewritten: "REWRITE ", channel.VoiceFallback: "FALLBACK", channel.VoiceFixed: "FIXED   "}[res.Outcome]
			fmt.Printf("%s %-10s %-16s %5dms  %q\n", mark, c.Kind, res.Reason, res.Latency.Milliseconds(), short(c.Text, 70))
			if verbose && res.Outcome != channel.VoiceFixed {
				fmt.Printf("    before: %s\n    after:  %s\n", indent(c.Text), indent(res.Text))
				if res.Rejected != "" {
					fmt.Printf("    rejected: %s\n", indent(res.Rejected))
				}
			}
			time.Sleep(delay)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(float64(len(lat))*p))]
	}
	overTimeout := 0
	for _, d := range lat {
		if d > channel.DefaultVoiceTimeout {
			overTimeout++
		}
	}
	answered := asked - errs
	fmt.Printf("\nreplies: %d, sent to the model: %d, fixed (never sent): %d\n", len(rows), asked, len(rows)-asked)
	fmt.Printf("rule check: %d/%d model answers passed (%.0f%%)\n", passed, answered, 100*float64(passed)/float64(max(1, answered)))
	fallbackRule := answered - passed
	fmt.Printf("fallback: %d/%d of the model-eligible replies (%.0f%%) — rule check %d, model error %d\n", fallbackRule+errs, asked, 100*float64(fallbackRule+errs)/float64(max(1, asked)), fallbackRule, errs)
	fmt.Printf("  at the production timeout (%s) another %d would fall back: %.0f%% in all\n", channel.DefaultVoiceTimeout, overTimeout, 100*float64(fallbackRule+errs+overTimeout)/float64(max(1, asked)))
	fmt.Printf("added latency (model-eligible replies): median %dms, p90 %dms, p95 %dms, max %dms\n", pct(0.5).Milliseconds(), pct(0.9).Milliseconds(), pct(0.95).Milliseconds(), pct(1).Milliseconds())
	var keys []string
	for k := range outcomes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("\noutcomes:")
	for _, k := range keys {
		fmt.Printf("  %-32s %d\n", k, outcomes[k])
	}
	if os.Getenv("VOICE_PAIRS") != "" {
		fmt.Println("\nbefore/after (first run, rewritten):")
		for _, rw := range rows[:len(cases)] {
			if rw.res.Outcome == channel.VoiceRewritten {
				fmt.Printf("\n[%s] customer: %q\n  before: %s\n  after:  %s\n", rw.c.Kind, rw.c.Customer, indent(rw.c.Text), indent(rw.res.Text))
			}
		}
	}
	return 0
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n            ") }
