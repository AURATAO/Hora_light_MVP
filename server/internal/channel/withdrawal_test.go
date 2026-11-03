package channel

import (
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// The exact production messages (2026-11-02 and 2026-11-03), straight and
// curly apostrophes (iMessage sends ’).
var productionWithdrawals = []string{
	"Oh sorry I don't need to task anymore thanks", // 2026-11-03 21:58 Rome
	"Oh sorry I don’t need to task anymore thanks",
	"never mind I don't need that anymore", // 2026-11-02
	"never mind I don’t need that anymore",
}

var withdrawalPhrases = []string{
	"never mind", "Never mind.", "nevermind", "nvm", "oh never mind thanks",
	"don't need it", "I don't need it anymore", "I don’t need the task anymore", "don't need that anymore",
	"sorry, I don't need it", "oh sorry we don't need that any more, thank you",
	"no longer need it", "I no longer need the errand", "not needed anymore",
	"forget it", "forget about it", "ok forget it thanks",
	"not anymore", "no not anymore",
	"I changed my mind", "sorry I've changed my mind", "changed my mind, thanks",
	"cancel task", "cancel the booking", "please cancel it", "oh sorry cancel that",
	"I don't want it anymore", "actually I don't want it", "scratch that",
	"算了", "不用了", "不要了", "算了謝謝", "不用了，謝謝", "不要了啦", "哦 算了",
}

// Messages that are NOT withdrawals: a change, a plain no (its own rules),
// an answer to the budget question, D-17's bare 不要 / 不用.
var notWithdrawals = []string{
	"make it 4pm", "no, make it 4pm", "never mind the milk, just eggs", "I don't need it today, make it tomorrow",
	"I don't need a car", "don't need the extra time", "no", "nope", "ok", "yes", "thanks",
	"不要啦", "不用喔", "不要", "pick up my laundry at 12 Bedford Ave", "the cake is for my mom",
	"I don't need to buy anything", "",
}

func TestIsWithdrawal(t *testing.T) {
	for _, s := range append(append([]string{}, productionWithdrawals...), withdrawalPhrases...) {
		if !IsWithdrawal(s) {
			t.Errorf("%q should be a withdrawal", s)
		}
	}
	for _, s := range notWithdrawals {
		if IsWithdrawal(s) {
			t.Errorf("%q must not be a withdrawal", s)
		}
	}
}

// With the booking summary open: dropped with the cancel reply, before any
// model — the agent says "correction" (the worst case) and is never asked,
// and nothing is re-parsed.
func TestWithdrawalAtTheSummaryDropsBeforeAnyModel(t *testing.T) {
	for _, s := range append(append([]string{}, productionWithdrawals...), withdrawalPhrases...) {
		g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionCorrection, Task: -1})
		g.say(t, "pick up cake and deliver to office")
		if g.state() != StateAwaitingConfirm {
			t.Fatalf("state %s", g.state())
		}
		calls, parses := len(fa.calls), len(g.parser.sources)
		got := g.say(t, s)
		if got != CopyDroppedCancelHint || g.state() != StateIdle {
			t.Errorf("%q: reply %q state %s", s, got, g.state())
		}
		if len(fa.calls) != calls || len(g.parser.sources) != parses {
			t.Errorf("%q: a model was asked (agent %d, parser %d)", s, len(fa.calls)-calls, len(g.parser.sources)-parses)
		}
		if strings.Contains(got, "Reply YES") || g.booker.posts != 0 {
			t.Errorf("%q: booking still offered", s)
		}
	}
}

// The same at the budget and name questions of a new booking, and with a
// setup link out.
func TestWithdrawalAtOtherNewBookingQuestions(t *testing.T) {
	for _, s := range productionWithdrawals {
		// Budget.
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.parser.draft = bagelD
		g.say(t, "bring me a bagel from Russ & Daughters")
		if g.state() != StateAwaitingBudget {
			t.Fatalf("state %s", g.state())
		}
		if got := g.say(t, s); got != CopyDroppedCancelHint || g.state() != StateIdle {
			t.Errorf("budget %q: %q state %s", s, got, g.state())
		}
		// Name.
		h := newRig(llm.IntentNewTask)
		h.say(t, "pick up my laundry at 12 Bedford Ave")
		if h.state() != StateAwaitingName {
			t.Fatalf("state %s", h.state())
		}
		if got := h.say(t, s); got != CopyDroppedCancelHint || h.state() != StateIdle {
			t.Errorf("name %q: %q state %s", s, got, h.state())
		}
		// Setup link out.
		k := newRig(llm.IntentNewTask)
		k.say(t, "pick up my laundry at 12 Bedford Ave")
		k.say(t, "Aura")
		k.say(t, "YES")
		if k.state() != StateAwaitingSetup {
			t.Fatalf("state %s", k.state())
		}
		if got := k.say(t, s); got != CopyDroppedCancelHint || k.state() != StateIdle {
			t.Errorf("setup %q: %q state %s", s, got, k.state())
		}
	}
	// "not needed" / "no thanks" still answer the budget question.
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = bagelD
	g.say(t, "bring me a bagel from Russ & Daughters")
	if got := g.say(t, "not needed"); g.state() != StateAwaitingConfirm || !strings.Contains(got, "Reply YES") {
		t.Fatalf("budget 'not needed': %q state %s", got, g.state())
	}
}

// Cause 1 (production 2026-11-03): the agent said "decline" but the old
// change check matched "to" and re-drafted. A decline with no real change
// in it drops the draft.
func TestAgentDeclineWithAPrepositionDrops(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionDecline, Task: -1})
	g.say(t, "pick up cake and deliver to office")
	parses := len(g.parser.sources)
	// Not a withdrawal by rule (leftover words): the agent is asked.
	got := g.say(t, "honestly my sister is going to do it for me")
	if len(fa.calls) == 0 {
		t.Fatal("agent not asked")
	}
	if got != CopyDroppedCancelHint || g.state() != StateIdle || len(g.parser.sources) != parses {
		t.Fatalf("reply %q state %s re-parsed %d", got, g.state(), len(g.parser.sources)-parses)
	}
	// A decline that carries a change is still re-drafted.
	h, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionDecline, Task: -1})
	h.say(t, "pick up cake and deliver to office")
	if got := h.say(t, "no, deliver it tomorrow instead"); h.state() != StateAwaitingConfirm || !strings.Contains(got, "Reply YES") {
		t.Fatalf("decline with a change: %q state %s", got, h.state())
	}
}

// Cause 2: with a supporter on another job the agent is asked through the
// context routing, which turned "decline" into an "answer" — the summary's
// "Reply YES" was put again. Now dropped.
func TestAgentDeclineWithASupporterOnAnotherJobDrops(t *testing.T) {
	g, chat, tasks, fa := agentRig(llm.Proposal{Action: llm.ActionDecline, Task: -1}, bagelT)
	g.say(t, "new task: pick up cake and deliver to office")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: on my way", "task-2", time.Now().Add(-20*time.Minute)
	got := g.say(t, "honestly my sister is going to do it for me")
	if len(fa.calls) == 0 {
		t.Fatal("agent not asked")
	}
	if got != CopyDroppedCancelHint || g.state() != StateIdle || len(chat.posted) != 0 {
		t.Fatalf("reply %q state %s posted %v", got, g.state(), chat.posted)
	}
}

// Title guard: a re-draft whose title gains a status word is never shown —
// the message was a withdrawal.
func TestStatusWordTitleIsAWithdrawal(t *testing.T) {
	for _, title := range []string{"Pick up cake and deliver to office (CANCELLED)", "Cake pickup - canceled", "Cake run (cancel)", "Cake pickup booked", "Cake pickup DONE", "Cake pickup completed", "VOID cake pickup"} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.parser.draft = llm.TaskDraft{Title: "Pick up cake and deliver to office", Category: "delivery", Location1: "12 Bedford Ave", DurationMinutes: 30}
		g.say(t, "pick up cake and deliver to office")
		g.parser.draft.Title = title
		// Not a withdrawal by rule: it reaches the parser.
		got := g.say(t, "the cake thing is off sorry")
		if got != CopyDroppedCancelHint || g.state() != StateIdle || strings.Contains(got, "Reply YES") {
			t.Errorf("%q: reply %q state %s", title, got, g.state())
		}
	}
	// A status word the errand already had is not one: "Get nails done",
	// "make it 4pm" re-drafts as usual.
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.parser.draft = llm.TaskDraft{Title: "Get nails done", Category: "other", Location1: "12 Bedford Ave", DurationMinutes: 30}
	g.say(t, "get my nails done at 12 Bedford Ave")
	if got := g.say(t, "make it 4pm"); g.state() != StateAwaitingConfirm || !strings.Contains(got, "Get nails done") {
		t.Fatalf("existing status word: %q state %s", got, g.state())
	}
}

// Unchanged: "never mind" with nothing open, and a correction re-drafts.
func TestWithdrawalGuardLeavesTheRestAlone(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	if got := g.say(t, "never mind"); got != CopyAck || g.state() != StateIdle || len(g.parser.sources) != 0 {
		t.Fatalf("idle never mind: %q state %s parses %d", got, g.state(), len(g.parser.sources))
	}
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	parses := len(g.parser.sources)
	got := g.say(t, "make it 4pm")
	if g.state() != StateAwaitingConfirm || len(g.parser.sources) != parses+1 || !strings.Contains(got, "Reply YES") {
		t.Fatalf("make it 4pm: %q state %s parses +%d", got, g.state(), len(g.parser.sources)-parses)
	}
	if !strings.HasSuffix(g.parser.sources[len(g.parser.sources)-1], "\nmake it 4pm") {
		t.Fatalf("re-draft source %q", g.parser.sources[len(g.parser.sources)-1])
	}
}
