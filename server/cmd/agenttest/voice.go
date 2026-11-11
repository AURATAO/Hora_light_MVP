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
	// Thread: Customer is the thread's last message, not one this reply
	// answers (a status update).
	Thread bool
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
		{"greeting", "hi", channel.CopyWelcome, false},
		{"greeting", "hey again", channel.CopyHelloAgain, false},
		{"draft", "can someone pick up my dry cleaning from Hudson Cleaners tomorrow at 10am", fmt.Sprintf(channel.CopyAskName, "Pick up dry cleaning from Hudson Cleaners"), false},
		{"draft", "grab me some groceries from Trader Joe's on 14th st", channel.CopyAskBudget, false},
		{"draft", "$30 i guess? not sure", channel.CopyAskBudgetAgain, false},
		{"draft", "I need help with something", channel.CopyAskWhatWhere, false},
		{"draft", "pick up my package asap", fmt.Sprintf(channel.CopyAskWhere, "Package pickup"), false},
		{"draft", "allow 15 extra min", fmt.Sprintf(channel.CopyExtraTimeAck, 15), false},
		{"draft", "YES", channel.CopyBooked, false},
		{"draft", "no actually forget it", channel.CopyDropped, false},
		{"draft", "Maria", channel.CopyAskNameAgain, false},
		{"draft", "laundry tomorrow 3pm 12 bedford ave", summary, false},
		// Follow-up questions.
		{"follow_up", "pick up from 350 broadway", fmt.Sprintf(channel.CopyWhichBorough, "350 Broadway"), false},
		{"follow_up", "deliver to 55 water", fmt.Sprintf(channel.CopyAskClearerAddress, "55 water"), false},
		{"follow_up", "find me a pharmacy that's open", channel.CopyAskLocation, false},
		{"follow_up", "the laundry one", channel.CopyWhichTask, false},
		// Discovery answers.
		{"discovery", "any good bakeries near me that do gluten free?", channel.CopyDiscoveryNothing, false},
		{"discovery", "what's the best thing at Levain?", fmt.Sprintf(channel.CopyAboutMenuItem, "Levain Bakery", "cookie") + " " + fmt.Sprintf(channel.CopyAboutAsk, "Levain Bakery"), false},
		{"discovery", "do they have a line right now?", channel.CopyLiveUnknown + "\n\n" + fmt.Sprintf(channel.CopyOfferCheck, "Joe's Pizza"), false},
		{"discovery", "what do people order at Katz's", "Reviews mention their pastrami on rye.\n\n" + fmt.Sprintf(channel.CopyOfferItems, "a pastrami on rye", "Katz's Delicatessen") + "\n" + channel.CopyFromGoogle, false},
		// Status updates.
		{"status", "", status("ORDER_ACCEPTED", "Laundry pickup", "Alex Rivera", "", ""), false},
		{"status", "", status("ON_MY_WAY", "Grocery run", "Bea", "", ""), false},
		{"status", "", status("SUPPORTER_ARRIVED", "Grocery run", "Bea", "", ""), false},
		{"status", "", status("CLOCK_OUT", "Laundry pickup", "Alex", "42 min", ""), false},
		{"status", "", status("COMPLETED", "Laundry pickup", "Alex", "42 min", "$35.50"), false},
		// Refusals and handoffs.
		{"refusal", "can someone pick me up some beer", refusal, false},
		{"refusal", "pick up a package in Jersey City", channel.CopyOutOfArea, false},
		{"refusal", "[voice memo]", channel.CopyTextOnly, false},
		{"handoff", "I want to talk to a real person", channel.CopyHandoffStart, false},
		{"handoff", "hello??", channel.CopyHandoffTimedOut, false},
		// Errors.
		{"error", "YES", channel.CopyPostFailed, false},
		{"error", "tell her the code is 4521", channel.CopyRelayFailed, false},
		{"error", "deliver to 200 e 10th st", channel.CopyAreaCheckDown, false},
		{"error", "I need a person NOW this is ridiculous", channel.CopyHandoffFailed, false},
		{"error", "cancel task 1 please", fmt.Sprintf(channel.CopyCancelFailed, "Grocery run"), false},
		{"cancel", "2", fmt.Sprintf(channel.CopyCancelledFree, "Grocery run"), false},
		// Stressed customer, and another language.
		{"stressed", "where is my supporter?? I've been waiting forever, I'm going to be late", fmt.Sprintf(channel.CopyWhereUnavailableLine, "Bea", "") + channel.CopyWhereAsked, false},
		{"stressed", "I was charged twice?? what is going on", channel.CopyHandoffStart, false},
		{"stressed", "hello?? anyone there", channel.CopyHandoffHolding, false},
		{"stressed", "this is ridiculous, my supporter still isn't here", fmt.Sprintf(channel.CopyWhereOnTheWay, "Bea", " — about 4 blocks away", ""), false},
		{"stressed", "I've been waiting forever, can someone help", channel.CopyAskWhatWhere, false},
		// Spanish.
		{"spanish", "hola, necesito que alguien recoja mi ropa de la lavandería mañana", fmt.Sprintf(channel.CopyAskName, "Recoger ropa de la lavandería"), false},
		{"spanish", "recoger un paquete en 350 broadway", fmt.Sprintf(channel.CopyWhichBorough, "350 Broadway"), false},
		{"spanish", "necesito que alguien me compre comida en el supermercado", channel.CopyAskBudget, false},
		{"spanish", "no, gracias, ya no lo necesito", channel.CopyDropped, false},
		{"spanish", "¿dónde está? todavía no llega", fmt.Sprintf(channel.CopyWhereUnavailableLine, "Bea", "") + channel.CopyWhereAsked, false},
		{Kind: "spanish", Customer: "gracias, ¿cuándo llega mi ropa?", Text: status("ON_MY_WAY", "Recoger ropa", "Bea", "", ""), Thread: true},
		// Traditional Chinese.
		{"zh-hant", "你好，可以幫我去洗衣店拿衣服嗎？", fmt.Sprintf(channel.CopyAskName, "洗衣店取衣服"), false},
		{"zh-hant", "請幫我買一些菜", channel.CopyAskBudget, false},
		{"zh-hant", "不用了，謝謝", channel.CopyDropped, false},
		{"zh-hant", "我的跑腿員到哪了？怎麼還沒到？？", fmt.Sprintf(channel.CopyWhereUnavailableLine, "Bea", "") + channel.CopyWhereAsked, false},
		{Kind: "zh-hant", Customer: "請問什麼時候會到？", Text: status("ORDER_ACCEPTED", "洗衣店取衣服", "Alex", "", ""), Thread: true},
		{"zh-hant", "您好，請問可以幫我取包裹嗎", channel.CopyAskWhatWhere, false},
		// Simplified Chinese.
		{"zh-hans", "你好，有人能帮我取快递吗？", channel.CopyAskWhatWhere, false},
		{"zh-hans", "帮我在350 broadway取个包裹", fmt.Sprintf(channel.CopyWhichBorough, "350 Broadway"), false},
		{"zh-hans", "不用了，谢谢", channel.CopyDropped, false},
		{Kind: "zh-hans", Customer: "我的东西什么时候到？", Text: status("SUPPORTER_ARRIVED", "取快递", "Bea", "", ""), Thread: true},
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
	triggered := map[string]int{}
	asked, passed, errs := 0, 0, 0
	for run := 0; run < runs; run++ {
		for _, c := range cases {
			ctx := channel.WithCustomerWordsForEval(context.Background(), c.Customer)
			if c.Thread {
				ctx = channel.WithThreadWordsForEval(context.Background(), c.Customer)
			}
			res := r.VoiceRewrite(ctx, channel.Outbound{Channel: channel.IMessage, To: "+15550000000", Text: c.Text})
			rows = append(rows, row{c, res})
			outcomes[res.Outcome+"/"+res.Reason]++
			if res.Trigger != "" {
				triggered[res.Trigger]++
			}
			if res.Outcome != channel.VoiceFixed && res.Outcome != channel.VoiceSkipped {
				asked++
				lat = append(lat, res.Latency)
				switch {
				case res.Outcome == channel.VoiceRewritten:
					passed++
				case res.Reason == "error" || res.Reason == "timeout":
					errs++
				}
			}
			mark := map[string]string{channel.VoiceRewritten: "REWRITE ", channel.VoiceFallback: "FALLBACK", channel.VoiceFixed: "FIXED   ", channel.VoiceSkipped: "SKIP    "}[res.Outcome]
			fmt.Printf("%s %-10s %-18s %-18s %5dms  %q\n", mark, c.Kind, res.Trigger, res.Reason, res.Latency.Milliseconds(), short(c.Text, 60))
			if verbose && res.Outcome != channel.VoiceFixed && res.Outcome != channel.VoiceSkipped {
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
	skipped, fixed := 0, 0
	for _, rw := range rows {
		switch rw.res.Outcome {
		case channel.VoiceSkipped:
			skipped++
		case channel.VoiceFixed:
			fixed++
		}
	}
	fmt.Printf("\nreplies: %d — through the layer (sent to the model): %d; sent as written: %d skipped (English, calm, no latency) + %d fixed (triggered, but never reworded)\n", len(rows), asked, skipped, fixed)
	fmt.Printf("triggers: %v\n", triggered)
	fmt.Printf("rule check: %d/%d model answers passed (%.0f%%)\n", passed, answered, 100*float64(passed)/float64(max(1, answered)))
	fallbackRule := answered - passed
	fmt.Printf("fallback: %d/%d of the replies sent to the model (%.0f%%) — rule check %d, model error/timeout %d\n", fallbackRule+errs, asked, 100*float64(fallbackRule+errs)/float64(max(1, asked)), fallbackRule, errs)
	fmt.Printf("  at the production timeout (%s) another %d would fall back\n", channel.DefaultVoiceTimeout, overTimeout)
	fmt.Printf("added latency (replies sent to the model): median %dms, p90 %dms, p95 %dms, max %dms; every other reply: 0\n", pct(0.5).Milliseconds(), pct(0.9).Milliseconds(), pct(0.95).Milliseconds(), pct(1).Milliseconds())
	// The glossary (voice.md), checked on what would be sent: "supporter"
	// and "HO:RA" never translated. CheckVoice refuses a rewrite that
	// breaks it ("glossary"); this counts both, independently.
	glossSent, glossRefused, glossApplies := 0, 0, 0
	for _, rw := range rows {
		if rw.res.Outcome != channel.VoiceRewritten && rw.res.Outcome != channel.VoiceFallback {
			continue
		}
		if strings.Contains(strings.ToLower(rw.c.Text), "supporter") || strings.Contains(rw.c.Text, "HO:RA") {
			glossApplies++
		}
		if !channel.GlossaryKept(rw.c.Text, rw.res.Text) {
			glossSent++
		}
		if rw.res.Reason == "glossary" {
			glossRefused++
		}
	}
	fmt.Printf("glossary (supporter, HO:RA): %d of the replies sent to the model contain a term; broken in sent text: %d; rewrites refused for it: %d\n", glossApplies, glossSent, glossRefused)
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
		fmt.Println("\nbefore/after (every run, sent to the model):")
		for _, rw := range rows {
			if rw.res.Outcome == channel.VoiceRewritten || rw.res.Outcome == channel.VoiceFallback {
				fmt.Printf("\n[%s | %s | %s %s] customer: %q\n  before: %s\n  after:  %s\n", rw.c.Kind, rw.res.Trigger, rw.res.Outcome, rw.res.Reason, rw.c.Customer, indent(rw.c.Text), indent(rw.res.Text))
				if rw.res.Rejected != "" {
					fmt.Printf("  rejected: %s\n", indent(rw.res.Rejected))
				}
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
