package channel

import (
	"fmt"
	"strings"
	"testing"

	"hora-auth/internal/channel/llm"
)

// The production messages of 2026-11-03 (15:56–16:46 NY), the number
// replaced by a fictional one.
const (
	prodExtraTimeAck = "15 mins is fine"
	prodNote         = "This is my number (555) 010-0199 if supporter arrive can call my friend Lily"
	prodCakeErrand   = "Hi I need someone to 100 Washington street pick up a cake from my friend and deliver to my office 250 Mercer street."
)

var cakeDraft = llm.TaskDraft{Title: "Pick up cake and deliver to office", Category: "delivery",
	Location1: "100 Washington St, New York, NY 10006", Location2: "250 Mercer St, New York, NY 10012"}

func TestIsExtraTimeAck(t *testing.T) {
	for _, s := range []string{
		"15 mins is fine", "15 min is ok", "15 minutes are fine 👍", "15 is fine", "fifteen minutes is fine",
		"extra time is fine", "the extra 15 is ok", "yes the 15 extra minutes are fine", "15 extra mins ok thanks",
		"Extra time OK!", "15 mins is fine.", "up to 15 extra minutes is good",
	} {
		if !IsExtraTimeAck(s, 15) {
			t.Errorf("%q: not read as agreeing to the extra-time line", s)
		}
	}
	for _, s := range []string{
		"15 mins", "make it 15 minutes", "it only takes 15 min", "it takes 15 minutes, that's fine",
		"20 mins is fine", "30 min is fine", "fine", "ok", "15 mins is fine but bring a bag",
		"about 15 mins is fine", "just 15 mins", "15 mins max", "no extra time", "extra time is fine at 4pm",
		"15 mins is fine, deliver to 250 Mercer",
	} {
		if IsExtraTimeAck(s, 15) {
			t.Errorf("%q: read as agreeing to the extra-time line", s)
		}
	}
	if IsExtraTimeAck("15 mins is fine", 0) {
		t.Error("no extra-time line, no acknowledgement of it")
	}
}

// 5a: "15 mins is fine" with the summary open keeps the draft — the same
// length and price — and asks for the plain YES. A clear new length still
// changes it.
func TestExtraTimeAckKeepsTheDraft(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionCorrection, Task: -1})
	g.ids.link(me, "user-1")
	g.parser.draft = cakeDraft
	before := g.say(t, prodCakeErrand)
	if !strings.Contains(before, "about 30 min") {
		t.Fatalf("summary: %q", before)
	}
	parses, quotes := len(g.parser.sources), g.booker.quotes
	got := g.say(t, prodExtraTimeAck)
	if got != fmt.Sprintf(CopyExtraTimeAck, 15) || g.state() != StateAwaitingConfirm {
		t.Fatalf("reply %q state %s", got, g.state())
	}
	if len(g.parser.sources) != parses || g.booker.quotes != quotes || len(fa.calls) != 0 {
		t.Fatalf("re-parsed %d re-quoted %d agent %d", len(g.parser.sources)-parses, g.booker.quotes-quotes, len(fa.calls))
	}
	if got := g.say(t, "YES"); g.booker.posts != 1 || g.booker.opts[0].QuotedCents == 0 || !strings.HasPrefix(got, "Booked") {
		t.Fatalf("YES: %q posts %d", got, g.booker.posts)
	}

	// "make it 15 minutes" is a new length: re-drafted (the fake booker
	// quotes 30 whatever the draft; DurationOnly sets the 15).
	h, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionCorrection, Task: -1})
	h.ids.link(me, "user-1")
	h.parser.draft = cakeDraft
	h.say(t, prodCakeErrand)
	parses = len(h.parser.sources)
	if got := h.say(t, "make it 15 minutes"); len(h.parser.sources) != parses+1 || !strings.Contains(got, "Reply YES") {
		t.Fatalf("new length not re-drafted: %q", got)
	}
	if m, ok := DurationOnly("make it 15 minutes"); !ok || m != 15 {
		t.Fatalf("DurationOnly = %d, %v", m, ok)
	}
}

// 5b: words for the supporter while the summary is open are kept as a note:
// shown in the summary, time / price / addresses untouched, sent with the
// booking.
func TestNoteForTheSupporterIsKept(t *testing.T) {
	g, _, _, fa := agentRig(llm.Proposal{Action: llm.ActionNote, Task: -1})
	g.ids.link(me, "user-1")
	g.parser.draft = cakeDraft
	before := g.say(t, prodCakeErrand)
	parses := len(g.parser.sources)
	got := g.say(t, prodNote)
	if len(fa.calls) != 1 {
		t.Fatalf("agent asked %d times", len(fa.calls))
	}
	if len(g.parser.sources) != parses || g.state() != StateAwaitingConfirm {
		t.Fatalf("re-parsed %d state %s", len(g.parser.sources)-parses, g.state())
	}
	if !strings.Contains(got, "Note for your supporter: "+prodNote) || !strings.Contains(got, "Reply YES") {
		t.Fatalf("summary: %q", got)
	}
	// Everything else as shown before.
	if strings.Replace(got, "\n"+fmt.Sprintf(CopyNoteForSupporter, prodNote), "", 1) != before {
		t.Fatalf("summary changed beyond the note:\n%s\n---\n%s", before, got)
	}
	// It survives a correction, and goes with the booking.
	g.r.Agent = &fakeAgent{p: llm.Proposal{Action: llm.ActionCorrection, Task: -1}}
	if got := g.say(t, "make it 4pm"); !strings.Contains(got, "Note for your supporter: "+prodNote) {
		t.Fatalf("after a re-draft: %q", got)
	}
	g.say(t, "YES")
	if g.booker.posts != 1 || len(g.booker.opts[0].Notes) != 1 || g.booker.opts[0].Notes[0] != prodNote {
		t.Fatalf("posted %d with %+v", g.booker.posts, g.booker.opts)
	}
}

// With a supporter on another job the agent is asked through the context
// routing: a note is the pending booking's, never relayed to that supporter.
func TestNoteWithASupporterOnAnotherJob(t *testing.T) {
	g, chat, _, _ := agentRig(llm.Proposal{Action: llm.ActionNote, Task: -1}, bagelT)
	g.parser.draft = cakeDraft
	g.say(t, "new task: "+prodCakeErrand)
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	got := g.say(t, prodNote)
	if !strings.Contains(got, "Note for your supporter: "+prodNote) || len(chat.posted) != 0 {
		t.Fatalf("reply %q posted %v", got, chat.posted)
	}
}

// A note that also changes the booking is a correction (re-drafted), so a
// time is never kept only as words.
func TestNoteWithAChangeIsACorrection(t *testing.T) {
	g, _, _, _ := agentRig(llm.Proposal{Action: llm.ActionNote, Task: -1})
	g.ids.link(me, "user-1")
	g.parser.draft = cakeDraft
	g.say(t, prodCakeErrand)
	parses := len(g.parser.sources)
	got := g.say(t, "call my friend Lily when you arrive, and make it 5pm")
	if len(g.parser.sources) != parses+1 || strings.Contains(got, "Note for your supporter") {
		t.Fatalf("re-parsed %d reply %q", len(g.parser.sources)-parses, got)
	}
}

func TestNotesDescription(t *testing.T) {
	if got := NotesDescription("Fragile.", nil); got != "Fragile." {
		t.Fatal(got)
	}
	got := NotesDescription("Fragile.", []string{"call Lily", "door code 4521"})
	if got != "Fragile.\n\nNote from the requester: call Lily\n\nNote from the requester: door code 4521" {
		t.Fatalf("%q", got)
	}
	if got := NotesDescription("", []string{"call Lily"}); got != "Note from the requester: call Lily" {
		t.Fatalf("%q", got)
	}
}
