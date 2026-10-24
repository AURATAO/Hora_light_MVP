package channel

import (
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// Arbiter 2026-10-24: cancel a posted task by text — confirmed once at the
// charge the app's policy quotes. Staging: "Can you cancel the task?" got
// "Got it" and nothing happened.
func TestCancelByChat(t *testing.T) {
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	g.booker.open = []TaskRef{{ID: "task-9", Title: "Book another task"}}
	g.booker.quote = CancelQuote{Title: "Book another task", ChargeCents: 0}
	g.booker.cancelRes = CancelResult{OK: true}
	got := g.say(t, "Can you cancel the task?")
	if !strings.Contains(got, "free to cancel") || g.state() != StateAwaitingCancel {
		t.Fatalf("%q state %s", got, g.state())
	}
	if got := g.say(t, "ok"); !strings.Contains(got, "reply YES on its own") || len(g.booker.cancelled) != 0 {
		t.Fatalf("casual ok must not cancel: %q", got)
	}
	got = g.say(t, "YES")
	if len(g.booker.cancelled) != 1 || g.booker.cancelled[0] != "task-9" || !strings.Contains(got, "No charge") || g.state() != StateIdle {
		t.Fatalf("cancelled %v reply %q state %s", g.booker.cancelled, got, g.state())
	}
}

// The charge is stated; a NO keeps the task; a charge that rose before the
// YES is asked again, never charged silently.
func TestCancelChargeConsent(t *testing.T) {
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	g.booker.open = []TaskRef{{ID: "task-9", Title: "Laundry pickup"}}
	g.booker.quote = CancelQuote{Title: "Laundry pickup", ChargeCents: 1200, BaseFeeCents: 1200, Committed: true}
	got := g.say(t, "cancel my laundry pickup")
	if !strings.Contains(got, "$12") {
		t.Fatalf("fee not stated: %q", got)
	}
	if got := g.say(t, "no"); !strings.Contains(got, "stays booked") || len(g.booker.cancelled) != 0 {
		t.Fatalf("no: %q", got)
	}
	g.booker.quotes2 = []CancelQuote{
		{Title: "Laundry pickup", ChargeCents: 1200, BaseFeeCents: 1200, Committed: true}, // the question
		{Title: "Laundry pickup", ChargeCents: 1550, BaseFeeCents: 1200, Committed: true}, // at the YES: rose
		{Title: "Laundry pickup", ChargeCents: 1550, BaseFeeCents: 1200, Committed: true}, // re-asked
	}
	g.say(t, "cancel it")
	got = g.say(t, "YES")
	if len(g.booker.cancelled) != 0 || !strings.Contains(got, "$15.50") {
		t.Fatalf("a higher charge was not asked again: cancelled %v reply %q", g.booker.cancelled, got)
	}
	g.booker.quote = CancelQuote{Title: "Laundry pickup", ChargeCents: 1550, BaseFeeCents: 1200, Committed: true}
	g.booker.cancelRes = CancelResult{OK: true, BillCents: 1550}
	got = g.say(t, "YES")
	if len(g.booker.cancelled) != 1 || !strings.Contains(got, "$15.50") {
		t.Fatalf("cancelled %v reply %q", g.booker.cancelled, got)
	}
}

// Several open tasks: a numbered list, a number picks one. "don't cancel"
// is not a cancel; with no open task, it says so.
func TestCancelChoosingAndNegation(t *testing.T) {
	g := newRig(llm.IntentOther)
	g.ids.link(me, "user-1")
	g.booker.open = []TaskRef{{ID: "a", Title: "Bagel run"}, {ID: "b", Title: "Laundry pickup"}}
	g.booker.quote = CancelQuote{Title: "Laundry pickup"}
	got := g.say(t, "please cancel")
	if !strings.Contains(got, "1. Bagel run") || !strings.Contains(got, "2. Laundry pickup") {
		t.Fatalf("list: %q", got)
	}
	got = g.say(t, "2")
	if !strings.Contains(got, "Laundry pickup") || g.state() != StateAwaitingCancel {
		t.Fatalf("pick: %q", got)
	}
	if IsCancelRequest("don't cancel it") || !IsCancelRequest("取消任務") {
		t.Fatal("negation / Chinese")
	}
	g2 := newRig(llm.IntentOther)
	g2.ids.link(me, "user-1")
	if got := g2.say(t, "cancel the task"); got != CopyCancelNone {
		t.Fatalf("none: %q", got)
	}
}

// While a draft waits, "cancel" still drops the draft (the existing NO):
// the cancel-a-posted-task flow is for an idle thread only.
func TestCancelDuringADraftDropsTheDraft(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.booker.open = []TaskRef{{ID: "a", Title: "Bagel run"}}
	g.say(t, "pick up my laundry tomorrow at 3")
	if got := g.say(t, "cancel"); got != CopyDropped || len(g.booker.cancelled) != 0 {
		t.Fatalf("%q cancelled %v", got, g.booker.cancelled)
	}
}
