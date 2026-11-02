package channel

import (
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Money rule: only a plain YES books. Each phrase is sent on an open summary
// with no supporter writing; a re-ask leaves the draft, and a YES then books
// it once.
func TestBookingNeedsAPlainYes(t *testing.T) {
	for _, c := range []struct {
		text string
		book bool
	}{
		{"YES", true}, {"yes", true}, {"yes!", true}, {"y", true}, {"Yes.", true},
		{"yes please", true}, {"Yes, please", true}, {"yes thanks", true}, {"yes, thank you!", true},
		{"ok", true}, // D-17 I-P version B, TestCasualAffirmativeGoesToTheSupporterWhoJustWrote

		{"book it please", false}, {"ok book it", false}, {"sure go ahead", false},
		{"👍", false}, {"✅", false}, {"book it", false}, {"sure", false}, {"go ahead", false},
		{"do it", false}, {"yep", false}, {"yeah", false}, {"ok thx", false}, {"ok thanks", false},
		{"yes book it", false}, {"yes please, thank you", false}, {"yes go ahead please", false},
	} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.say(t, "laundry pickup")
		got := g.say(t, c.text)
		if c.book {
			if got != CopyBooked || g.booker.posts != 1 {
				t.Errorf("%q: want a booking, got %q (posts %d)", c.text, got, g.booker.posts)
			}
			continue
		}
		if got != CopyConfirmReask || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
			t.Errorf("%q: want a re-ask, got %q (posts %d, state %s)", c.text, got, g.booker.posts, g.state())
			continue
		}
		if got := g.say(t, "YES"); got != CopyBooked || g.booker.posts != 1 {
			t.Errorf("%q then YES: %q (posts %d)", c.text, got, g.booker.posts)
		}
	}
}

// The same bar before the setup link: a first-timer's YES is the consent.
func TestSetupLinkNeedsAPlainYes(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	g.say(t, "I'm Aura")
	if got := g.say(t, "👍"); got != CopyConfirmReask || len(g.ids.tokens) != 0 {
		t.Fatalf("got %q, tokens %d", got, len(g.ids.tokens))
	}
}

// "yes but…" carries a change: never booked, re-drafted, and the new summary
// asks for the YES again.
func TestYesWithAChangeDoesNotBook(t *testing.T) {
	for _, text := range []string{"yes but make it 4pm", "ok but 4pm", "yes if it's under $20"} {
		g := newRig(llm.IntentNewTask)
		g.ids.link(me, "user-1")
		g.say(t, "laundry pickup")
		if got := g.say(t, text); !strings.Contains(got, "Reply YES") || g.booker.posts != 0 {
			t.Errorf("%q: got %q (posts %d)", text, got, g.booker.posts)
		}
	}
}

// With a supporter on the job and nothing of ours open, a yes with words in
// it reaches them.
func TestPoliteYesReachesTheActiveSupporter(t *testing.T) {
	for _, text := range []string{"yes please, thank you", "yes thanks", "ok thank you"} {
		g, chat, _ := relayRig(llm.IntentOther, laundryT)
		g.say(t, text)
		if len(chat.posted) != 1 || !strings.HasSuffix(chat.posted[0], ":"+text) || g.booker.posts != 0 || g.state() != StateIdle {
			t.Errorf("%q: posted %v, posts %d, state %s", text, chat.posted, g.booker.posts, g.state())
		}
	}
}

// With no supporter on the job it is an acknowledgement, not an errand.
func TestPoliteYesWithNobodyOnTheJob(t *testing.T) {
	g, chat, _ := relayRig(llm.IntentNewTask)
	if got := g.say(t, "yes thanks"); got != CopyAck || len(chat.posted) != 0 || g.state() != StateIdle {
		t.Fatalf("got %q, posted %v, state %s", got, chat.posted, g.state())
	}
}

// A summary is open and the supporter has just written: a yes with courtesy
// is theirs, and the YES the summary needs is still needed. This is what
// keeps "yes thanks" from booking on words meant for someone else.
func TestPoliteYesOnASummaryGoesToTheSupporterWhoJustWrote(t *testing.T) {
	for _, text := range []string{"yes thanks", "yes please, thank you", "yes please", "book it please"} {
		g, chat, tasks, _ := contextRig(llm.IntentNewTask, bagelT)
		g.say(t, "new task: pick up my laundry tomorrow at 3")
		tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: toasted?", "task-2", time.Now().Add(-time.Minute)
		got := g.say(t, text)
		if len(chat.posted) != 1 || chat.posted[0] != "task-2:"+text || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
			t.Errorf("%q: posted %v, posts %d, state %s", text, chat.posted, g.booker.posts, g.state())
		}
		if !strings.Contains(got, "Still waiting on your YES or NO") {
			t.Errorf("%q: got %q", text, got)
		}
	}
}

// A yes with a question or a message for the supporter is not a booking.
func TestYesWithAMessageForTheSupporterDoesNotBook(t *testing.T) {
	g, chat, tasks, _ := contextRig(llm.IntentToSupporter, bagelT)
	g.say(t, "new task: pick up my laundry tomorrow at 3")
	tasks.lastText, tasks.lastTask, tasks.lastAt = "Sam · Bagel run: toasted?", "task-2", time.Now().Add(-time.Minute)
	const text = "yes please, can you get two?"
	g.say(t, text)
	if len(chat.posted) != 1 || chat.posted[0] != "task-2:"+text || g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posted %v, posts %d, state %s", chat.posted, g.booker.posts, g.state())
	}
}

func TestYesWordMatchers(t *testing.T) {
	for _, s := range []string{"yes thanks", "YES please", "ok thx", "好謝謝"} {
		if !IsYes(s) {
			t.Errorf("IsYes(%q)", s)
		}
	}
	for _, s := range []string{"book it please", "sure thanks", "yes please, thank you", "okthanks", "surety", "yesterday"} {
		if IsYes(s) {
			t.Errorf("IsYes(%q)", s)
		}
	}
	for _, s := range []string{"okthanks", "yesterday", "yes but 4pm", "thanks", "please", "ok but 4pm", "ok, tell him thanks", "no thanks"} {
		if IsAffirmation(s) || IsBookingYes(s) {
			t.Errorf("%q: affirmation %v, booking %v", s, IsAffirmation(s), IsBookingYes(s))
		}
	}
}
