package channel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// Replies to a supporter drop-off notice (handover.go).

type fakeHandOvers struct {
	notices []HandOverNotice
	since   time.Time
	err     error
}

func (f *fakeHandOvers) HandOverNotices(_ context.Context, _ string, _ Channel, _ string, since time.Time) ([]HandOverNotice, error) {
	f.since = since
	return f.notices, f.err
}

type fakeHandOverReader struct {
	reading llm.HandOverReading
	err     error
	calls   []llm.HandOverQuery
}

func (f *fakeHandOverReader) ReadHandOver(_ context.Context, q llm.HandOverQuery) (llm.HandOverReading, error) {
	f.calls = append(f.calls, q)
	return f.reading, f.err
}

const dryTitle = "Pick up dry cleaning and deliver to 200 E 70th St"

func cancelledNotice(sentAgo time.Duration, sched *time.Time) HandOverNotice {
	return HandOverNotice{Kind: HandOverCancelled, TaskID: "task-9", Title: dryTitle,
		Text:   "Your supporter couldn't finish \"" + dryTitle + "\", so we've cancelled it. You haven't been charged. Want it done by someone else? Just tell us.",
		SentAt: time.Now().Add(-sentAgo), Category: "laundry", Description: "Ticket 4471, already paid.",
		Location1: "1232 2nd Ave, New York, NY 10065", Location2: "200 E 70th St, New York, NY 10021", Minutes: 30, ScheduledAt: sched}
}

func reassignedNotice(started bool) HandOverNotice {
	return HandOverNotice{Kind: HandOverReassigned, TaskID: "task-2", Title: "Bagel run", Supporter: "Sam Chen", Started: started,
		Text: "Your supporter couldn't finish \"Bagel run\", so Sam Chen is taking over. You only pay for Sam Chen's time.", SentAt: time.Now().Add(-2 * time.Minute)}
}

func handOverRig(t *testing.T, n ...HandOverNotice) (*rig, *fakeHandOvers, *fakeHandOverReader, *memOps) {
	t.Helper()
	g, ops := handoffRig(t, llm.IntentNewTask)
	h := &fakeHandOvers{notices: n}
	rd := &fakeHandOverReader{reading: llm.ReadOther}
	g.r.HandOvers, g.r.HandOverReader = h, rd
	return g, h, rd, ops
}

func tomorrowAt3() *time.Time {
	nyc, _ := time.LoadLocation("America/New_York")
	d := time.Now().In(nyc).AddDate(0, 0, 1)
	t := time.Date(d.Year(), d.Month(), d.Day(), 15, 0, 0, 0, nyc)
	return &t
}

func TestHandOverRules(t *testing.T) {
	cases := []struct {
		kind, text string
		want       llm.HandOverReading
	}{
		{HandOverCancelled, "yes", llm.ReadRedo},
		{HandOverCancelled, "Yes please!", llm.ReadRedo},
		{HandOverCancelled, "find someone else", llm.ReadRedo},
		{HandOverCancelled, "redo it", llm.ReadRedo},
		{HandOverCancelled, "好的", llm.ReadRedo},
		{HandOverCancelled, "再找一个人吧", llm.ReadRedo},
		{HandOverCancelled, "sí, por favor", llm.ReadRedo},
		{HandOverCancelled, "otra persona", llm.ReadRedo},
		{HandOverCancelled, "no thanks", llm.ReadDecline},
		{HandOverCancelled, "never mind", llm.ReadDecline},
		{HandOverCancelled, "不用了", llm.ReadDecline},
		{HandOverCancelled, "算了", llm.ReadDecline},
		{HandOverCancelled, "no gracias", llm.ReadDecline},
		{HandOverCancelled, "why?", llm.ReadWhy},
		{HandOverCancelled, "What happened?", llm.ReadWhy},
		{HandOverCancelled, "为什么", llm.ReadWhy},
		{HandOverCancelled, "¿qué pasó?", llm.ReadWhy},
		{HandOverCancelled, "ok", llm.ReadAck},
		{HandOverCancelled, "谢谢", llm.ReadAck},
		{HandOverReassigned, "ok thanks", llm.ReadAck},
		{HandOverReassigned, "yes", llm.ReadAck},
		{HandOverReassigned, "好的", llm.ReadAck},
		{HandOverReassigned, "vale, gracias", llm.ReadAck},
		{HandOverReassigned, "why", llm.ReadWhy},
	}
	for _, c := range cases {
		got, ok := ReadHandOverByRule(c.kind, c.text)
		if !ok || got != c.want {
			t.Errorf("%s %q: %q %v, want %q", c.kind, c.text, got, ok, c.want)
		}
	}
	// Left to the model: anything longer, and words a rule must not take
	// ("不好" is "not good", never a yes; "no" to a new supporter is not
	// a plain decline; "find someone else" to a reassign is no ack).
	for _, c := range []struct{ kind, text string }{
		{HandOverCancelled, "不好"}, {HandOverCancelled, "不是"}, {HandOverCancelled, "was I charged?"},
		{HandOverCancelled, "yes but tomorrow at 5"}, {HandOverReassigned, "no"}, {HandOverReassigned, "find someone else"},
		{HandOverReassigned, "who is it?"}, {HandOverReassigned, "I want the original one"}, {HandOverReassigned, "cancel it"},
	} {
		if got, ok := ReadHandOverByRule(c.kind, c.text); ok {
			t.Errorf("%s %q: a rule took it as %q", c.kind, c.text, got)
		}
	}
}

// "yes" to "Want it done by someone else?": the errand drafted again from
// the cancelled one, with its places and time — a summary, never a booking.
// The YES that follows books it.
func TestHandOverRedoDraftsTheErrandAgain(t *testing.T) {
	g, h, rd, _ := handOverRig(t, cancelledNotice(5*time.Minute, tomorrowAt3()))
	got := g.say(t, "yes")
	if g.booker.posts != 0 || g.state() != StateAwaitingConfirm {
		t.Fatalf("posts %d state %s", g.booker.posts, g.state())
	}
	if !strings.HasPrefix(got, "Sure, here's “"+dryTitle+"” again for another supporter:") || !strings.Contains(got, "Reply YES to book") || !strings.Contains(got, "3:00 PM") {
		t.Fatalf("summary: %q", got)
	}
	p := g.pending(t)
	if p.Draft.Location1 != "1232 2nd Ave, New York, NY 10065" || p.Draft.Location2 != "200 E 70th St, New York, NY 10021" || !p.Draft.Scheduled || !strings.Contains(p.Draft.Description, "4471") {
		t.Fatalf("draft %+v", p.Draft)
	}
	if len(rd.calls) != 0 {
		t.Fatal("a plain yes needs no model")
	}
	if time.Since(h.since) < HandOverWindow-time.Minute || time.Since(h.since) > HandOverWindow+time.Minute {
		t.Fatalf("notices read since %s", h.since)
	}
	h.notices = nil // as the store would: a newer task closes it
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatalf("the YES did not book: posts %d", g.booker.posts)
	}
}

// A time that has passed (or "as soon as possible", which meant then) is
// asked again before any summary; the answer re-drafts the errand.
func TestHandOverRedoAsksTheTimeWhenItPassed(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	for _, sched := range []*time.Time{nil, &past} {
		g, _, rd, _ := handOverRig(t, cancelledNotice(5*time.Minute, sched))
		rd.reading = llm.ReadRedo // were it asked, it would redo again
		got := g.say(t, "redo it")
		if !strings.HasSuffix(got, CopyHandOverAskTime) || g.state() != StateAwaitingConfirm || !g.pending(t).TimeAsked || g.booker.posts != 0 {
			t.Fatalf("sched %v: %q state %s", sched, got, g.state())
		}
		if got := g.say(t, "yes"); got != CopyHandOverAskTime {
			t.Fatalf("a yes to when: %q", got)
		}
		g.parser.draft = llm.TaskDraft{Title: dryTitle, Category: "laundry", Location1: "1232 2nd Ave", Location2: "200 E 70th St", Scheduled: true, ScheduledTime: tomorrowAt3().Format(time.RFC3339)}
		got = g.say(t, "tomorrow at 3pm")
		if !strings.Contains(got, "Reply YES to book") || g.booker.posts != 0 {
			t.Fatalf("after the time: %q", got)
		}
		if src := g.parser.lastText(); !strings.Contains(src, "At: 1232 2nd Ave") || !strings.Contains(src, "tomorrow at 3pm") {
			t.Fatalf("re-drafted from %q", src)
		}
		if len(rd.calls) != 0 || strings.Count(got, "again for another supporter") != 0 {
			t.Fatalf("the redo's own answers went to the notice reader: %d calls, %q", len(rd.calls), got)
		}
	}
}

// Words beyond the yes go with the errand ("yes, tomorrow at 5").
func TestHandOverRedoWithWordsReDrafts(t *testing.T) {
	g, _, rd, _ := handOverRig(t, cancelledNotice(5*time.Minute, nil))
	rd.reading = llm.ReadRedo
	g.parser.draft = llm.TaskDraft{Title: dryTitle, Category: "laundry", Location1: "1232 2nd Ave", Location2: "200 E 70th St", Scheduled: true, ScheduledTime: tomorrowAt3().Format(time.RFC3339)}
	got := g.say(t, "can someone else do it tomorrow at 3pm?")
	if !strings.Contains(got, "Reply YES to book") || g.booker.posts != 0 {
		t.Fatalf("%q", got)
	}
	if src := g.parser.lastText(); !strings.Contains(src, "Ticket 4471") || !strings.Contains(src, "tomorrow at 3pm") {
		t.Fatalf("parsed %q", src)
	}
}

// A cancelled errand's shopping budget is the customer's own: it carries.
func TestHandOverRedoKeepsTheBudget(t *testing.T) {
	n := cancelledNotice(5*time.Minute, tomorrowAt3())
	n.Title, n.Category, n.Description, n.BudgetCents = "Buy milk, eggs and bread at Whole Foods", "grocery", "Milk, eggs and bread.", 2500
	g, _, _, _ := handOverRig(t, n)
	got := g.say(t, "yes please")
	if !strings.Contains(got, "$25") || g.state() != StateAwaitingConfirm || g.booker.posts != 0 {
		t.Fatalf("%q state %s", got, g.state())
	}
	if p := g.pending(t); p.BudgetCents != 2500 || !p.BudgetAsked {
		t.Fatalf("budget %d asked %v", p.BudgetCents, p.BudgetAsked)
	}
}

// The redo is checked for scope like any request: companionship, paused,
// is declined.
func TestHandOverRedoIsScopeChecked(t *testing.T) {
	t.Setenv("COMPANIONSHIP_ENABLED", "")
	n := cancelledNotice(5*time.Minute, tomorrowAt3())
	n.Title, n.Category, n.Description = "Walk with me in Central Park", "companionship", "Keep me company on a walk."
	g, _, _, _ := handOverRig(t, n)
	got := g.say(t, "yes")
	if g.state() != StateIdle || g.booker.posts != 0 || strings.Contains(got, "Reply YES") {
		t.Fatalf("%q state %s", got, g.state())
	}
	pbs, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	if ref := pbs.PausedRefusal("", "companionship"); ref == nil || got != ref.Reply {
		t.Fatalf("not the paused reply: %q", got)
	}
}

func TestHandOverDeclineWhyCharged(t *testing.T) {
	g, _, rd, _ := handOverRig(t, cancelledNotice(5*time.Minute, tomorrowAt3()))
	if got := g.say(t, "no thanks"); got != CopyHandOverDeclined || g.st.lastOutboundTask() != "task-9" {
		t.Fatalf("decline %q tagged %q", got, g.st.lastOutboundTask())
	}
	if got := g.say(t, "why?"); !strings.HasPrefix(got, "Your supporter couldn't finish “"+dryTitle+"”, so we cancelled it") {
		t.Fatalf("why %q", got)
	}
	rd.reading = llm.ReadCharged
	got := g.say(t, "was I charged?")
	if got != "No, you weren't charged for “"+dryTitle+"”. Any hold for it on your card was released." {
		t.Fatalf("charged %q", got)
	}
	if g.state() != StateIdle || g.booker.posts != 0 {
		t.Fatalf("state %s posts %d", g.state(), g.booker.posts)
	}
}

// Past the window, closed, or not about the notice: routed as before.
func TestHandOverOnlyWhileTheNoticeCounts(t *testing.T) {
	g, h, rd, _ := handOverRig(t, cancelledNotice(25*time.Hour, tomorrowAt3()))
	h.notices = nil // the store reads since now-24h: nothing
	if got := g.say(t, "why?"); got == CopyHandOverWhyCancelled || strings.Contains(got, "couldn't finish") {
		t.Fatalf("outside the window: %q", got)
	}
	n := cancelledNotice(time.Minute, tomorrowAt3())
	n.Closed = true
	h.notices = []HandOverNotice{n}
	if got := g.say(t, "yes"); g.state() != StateIdle || strings.Contains(got, "again for another supporter") {
		t.Fatalf("closed: %q", got)
	}
	h.notices = []HandOverNotice{cancelledNotice(time.Minute, tomorrowAt3())}
	rd.reading = llm.ReadOther
	g.say(t, "can you pick up my laundry at 12 Bedford Ave")
	if g.state() != StateAwaitingConfirm || g.pending(t).Draft.Title != laundry.Title {
		t.Fatalf("a new request is a new request: state %s", g.state())
	}
	// The model is down: routed as before, no hand-off.
	g2, _, rd2, ops := handOverRig(t, cancelledNotice(time.Minute, tomorrowAt3()))
	rd2.err = errors.New("timeout")
	g2.say(t, "hmm what")
	if len(ops.notices) != 0 || g2.state() == StateHandoff {
		t.Fatalf("a model error handed off")
	}
}

// A draft open from before the notice and a bare "yes": which one? The
// answer REDO redoes; a YES books the draft.
func TestHandOverAsksWhichOneWhenBothCouldApply(t *testing.T) {
	g, h, _, _ := handOverRig(t)
	g.say(t, "laundry pickup at 12 Bedford Ave")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	g.edit(func(c *Conversation) {
		past := time.Now().Add(-10 * time.Minute)
		c.QuestionShownAt = &past
		c.Pending.SummaryShownAtUnix = past.Unix()
	})
	h.notices = []HandOverNotice{cancelledNotice(time.Minute, tomorrowAt3())}
	got := g.say(t, "yes")
	if !strings.HasPrefix(got, CopyHandOverWhichLead) || !strings.Contains(got, "Reply REDO") || !strings.Contains(got, laundry.Title) || g.booker.posts != 0 {
		t.Fatalf("which: %q posts %d", got, g.booker.posts)
	}
	got = g.say(t, "REDO")
	if !strings.Contains(got, "again for another supporter") || g.pending(t).Draft.Title != dryTitle || g.booker.posts != 0 {
		t.Fatalf("redo after which: %q", got)
	}
	// The same, answered YES: the draft that was open books.
	g, h, _, _ = handOverRig(t)
	g.say(t, "laundry pickup at 12 Bedford Ave")
	g.edit(func(c *Conversation) {
		past := time.Now().Add(-10 * time.Minute)
		c.QuestionShownAt = &past
		c.Pending.SummaryShownAtUnix = time.Now().Unix()
	})
	h.notices = []HandOverNotice{cancelledNotice(time.Minute, tomorrowAt3())}
	g.say(t, "yes")
	g.say(t, "YES")
	if g.booker.posts != 1 || g.booker.posted[0] != laundry.Title {
		t.Fatalf("posts %d %v", g.booker.posts, g.booker.posted)
	}
}

// A summary shown after the notice is the latest thing said: its YES books
// it, no question.
func TestHandOverNewerDraftWins(t *testing.T) {
	g, _, _, _ := handOverRig(t, cancelledNotice(10*time.Minute, tomorrowAt3()))
	g.say(t, "laundry pickup at 12 Bedford Ave")
	g.say(t, "YES")
	if g.booker.posts != 1 || g.booker.posted[0] != laundry.Title {
		t.Fatalf("posts %d %v", g.booker.posts, g.booker.posted)
	}
}

func TestHandOverReassignedReplies(t *testing.T) {
	g, _, rd, ops := handOverRig(t, reassignedNotice(true))
	if got := g.say(t, "ok thanks"); got != CopyAckShort {
		t.Fatalf("ack %q", got)
	}
	rd.reading = llm.ReadStatus
	if got := g.say(t, "who is it?"); got != "Sam is your supporter for “Bagel run” now, and it's still on." {
		t.Fatalf("who %q", got)
	}
	rd.reading = llm.ReadCharged
	if got := g.say(t, "will I be charged twice?"); got != "No, you won't be charged twice. You only pay for Sam's time on “Bagel run”." {
		t.Fatalf("twice %q", got)
	}
	if got := g.say(t, "why"); got != "Your first supporter couldn't finish “Bagel run”, so Sam is taking over." {
		t.Fatalf("why %q", got)
	}
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	rd.reading = llm.ReadCancel
	got := g.say(t, "I don't want it anymore")
	if g.state() != StateAwaitingCancel || len(g.booker.cancelled) != 0 {
		t.Fatalf("cancel: %q state %s cancelled %v", got, g.state(), g.booker.cancelled)
	}
	g2, _, rd2, ops2 := handOverRig(t, reassignedNotice(false))
	rd2.reading = llm.ReadOriginal
	got = g2.say(t, "I want the original one")
	if got != CopyHandoffStart || g2.state() != StateHandoff || len(ops2.notices) != 1 || ops2.notices[0].Reason != HandoffSupporterChange {
		t.Fatalf("original: %q state %s notices %+v", got, g2.state(), ops2.notices)
	}
	if len(ops.notices) != 0 {
		t.Fatalf("ops told for answered replies: %+v", ops.notices)
	}
	// Before start, the why names no failure.
	g3, _, _, _ := handOverRig(t, reassignedNotice(false))
	if got := g3.say(t, "why?"); got != "We changed the supporter for “Bagel run”: Sam is doing it now." {
		t.Fatalf("why before start %q", got)
	}
}

// Every reply copy stays inside the rules: no refund, compensation or time
// promised; no reason beyond "couldn't finish".
func TestHandOverCopyPromisesNothing(t *testing.T) {
	for _, s := range []string{CopyHandOverDeclined, CopyHandOverAckCancelled, CopyHandOverWhyCancelled, CopyHandOverWhyTakenOver, CopyHandOverWhyChanged,
		CopyHandOverNotCharged, CopyHandOverNotTwice, CopyHandOverWho, CopyHandOverRedoIntro, CopyHandOverAskTime} {
		l := strings.ToLower(s)
		for _, bad := range []string{"refund", "compensat", "credit", "minutes", "sorry for", "sick", "emergency", "family"} {
			if strings.Contains(l, bad) {
				t.Errorf("%q says %q", s, bad)
			}
		}
	}
}

func (f *fakeParser) lastText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sources) == 0 {
		return ""
	}
	return f.sources[len(f.sources)-1]
}

// A draft open from before a reassign notice, and "I don't want it
// anymore": the errand or the draft? "cancel task" then starts the usual
// cancel of the errand; the draft is left as it was.
func TestHandOverWhichOneOnACancel(t *testing.T) {
	g, h, rd, _ := handOverRig(t)
	g.say(t, "laundry pickup at 12 Bedford Ave")
	g.edit(func(c *Conversation) {
		past := time.Now().Add(-10 * time.Minute)
		c.QuestionShownAt = &past
		c.Pending.SummaryShownAtUnix = past.Unix()
	})
	h.notices = []HandOverNotice{reassignedNotice(true)}
	rd.reading = llm.ReadCancel
	got := g.say(t, "I don't want it anymore")
	if !strings.HasPrefix(got, CopyHandOverWhichLead) || !strings.Contains(got, `Reply "cancel task"`) || g.state() != StateAwaitingConfirm || g.pending(t) == nil {
		t.Fatalf("which: %q state %s", got, g.state())
	}
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	g.say(t, "cancel task")
	if g.state() != StateAwaitingCancel || len(g.booker.cancelled) != 0 {
		t.Fatalf("state %s cancelled %v", g.state(), g.booker.cancelled)
	}
}
