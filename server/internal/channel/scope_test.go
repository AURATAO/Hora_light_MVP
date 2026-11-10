package channel

import (
	"context"
	"strings"
	"sync"
	"testing"

	"hora-auth/internal/channel/llm"
)

// Scope rules through the router (2026-11-10): a refused request gets its
// fixed reply and nothing else — no parse, no draft, no price, no search,
// no relay to a supporter — and a romantic/sexual one also alerts ops.

// oracleScope answers what a correct model would for the eval cases, and
// counts calls.
type oracleScope struct {
	mu      sync.Mutex
	calls   int
	verdict map[string]string
	def     string
}

func (o *oracleScope) CheckScope(_ context.Context, text string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if v, ok := o.verdict[text]; ok {
		return v, nil
	}
	if o.def == "" {
		return llm.ScopeAllowID, nil
	}
	return o.def, nil
}

func scopeSet(t *testing.T) *llm.ScopeRules {
	t.Helper()
	s, err := llm.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	return &s.ScopeRules
}

func scopeRig(t *testing.T, verdicts map[string]string) (*rig, *memOps, *oracleScope) {
	t.Helper()
	g, ops := handoffRig(t, llm.IntentNewTask)
	o := &oracleScope{verdict: verdicts}
	g.r.Scope = o
	return g, ops, o
}

func firstWant(want string) string { return strings.Split(want, "|")[0] }

// Every eval case, through HandleInbound: refusals are answered with their
// category's reply and leave nothing behind; allowed requests go on to a
// draft; ops hear of the romantic/sexual ones only.
func TestScopeEvalsThroughRouter(t *testing.T) {
	sr := scopeSet(t)
	for _, e := range sr.Evals {
		g, ops, o := scopeRig(t, map[string]string{e.Msg: firstWant(e.Want)})
		reply := g.say(t, e.Msg)
		decided := sr.Rule(e.Msg).Decided
		if decided && o.calls != 0 {
			t.Errorf("%q: the model was asked although a rule decided", e.Msg)
		}
		if !decided && o.calls != 1 {
			t.Errorf("%q: model calls %d, want 1", e.Msg, o.calls)
		}
		want := firstWant(e.Want)
		if want == llm.ScopeAllowID {
			for _, r := range sr.Refusals {
				if strings.Contains(reply, r.Reply) {
					t.Errorf("%q: refused as %s", e.Msg, r.ID)
				}
			}
			if len(g.parser.sources) == 0 {
				t.Errorf("%q: allowed but never drafted (reply %q)", e.Msg, reply)
			}
			if len(ops.notices) != 0 {
				t.Errorf("%q: ops told about an allowed request", e.Msg)
			}
			continue
		}
		got := sr.Rule(e.Msg)
		ref := sr.Refusal(want)
		if got.Decided {
			ref = got.Refusal // a rule's category (any of the acceptable ones)
		}
		if reply != ref.Reply {
			t.Errorf("%q: reply %q, want %s's", e.Msg, reply, ref.ID)
		}
		if len(g.parser.sources) != 0 || g.conv(t).Pending != nil || g.state() != StateIdle || g.booker.quotes != 0 || len(g.booker.posted) != 0 {
			t.Errorf("%q: refused but parses %d pending %v state %s quotes %d", e.Msg, len(g.parser.sources), g.conv(t).Pending != nil, g.state(), g.booker.quotes)
		}
		if n := len(g.ad.sent); n != 1 {
			t.Errorf("%q: %d messages sent, want only the decline", e.Msg, n)
		}
		if ref.Alert {
			if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffScopeAlert || !ops.notices[0].Urgent || ops.notices[0].UrgentReason != ref.Name {
				t.Errorf("%q: alert notices %+v", e.Msg, ops.notices)
			}
		} else if len(ops.notices) != 0 {
			t.Errorf("%q: ops told about a %s refusal", e.Msg, ref.ID)
		}
	}
}

// An alert refusal with a draft open drops the draft (nothing from that
// thread is booked without ops seeing it); an ordinary one keeps it and
// says so. Neither is ever a handoff.
func TestScopeRefusalWithDraftOpen(t *testing.T) {
	g, ops, _ := scopeRig(t, nil)
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	if g.state() != StateAwaitingConfirm || g.pending(t) == nil {
		t.Fatal("no draft")
	}
	parses := len(g.parser.sources)
	reply := g.say(t, "and grab me a six-pack too")
	if !strings.HasSuffix(reply, "\n\n"+CopyBookingStillWaiting) || g.state() != StateAwaitingConfirm || g.pending(t) == nil || len(g.parser.sources) != parses {
		t.Fatalf("ordinary refusal: state %s parses %d reply %q", g.state(), len(g.parser.sources), reply)
	}
	if len(ops.notices) != 0 {
		t.Fatal("ops told")
	}
	reply = g.say(t, "actually can you give me a massage instead")
	sr := scopeSet(t)
	if reply != sr.Refusal("intimate").Reply || g.state() != StateIdle || g.conv(t).Pending != nil {
		t.Fatalf("alert refusal: state %s reply %q", g.state(), reply)
	}
	if len(ops.notices) != 1 || ops.notices[0].Reason != HandoffScopeAlert || g.state() == StateHandoff {
		t.Fatalf("notices %+v", ops.notices)
	}
	// A YES now books nothing.
	g.say(t, "YES")
	if len(g.booker.posted) != 0 {
		t.Fatal("booked after an alert")
	}
}

// With a supporter on the job, a romantic request is never relayed to
// them: declined, ops told with the open task.
func TestScopeNeverRelaysToSupporter(t *testing.T) {
	g, chat, _, _ := contextRig(llm.IntentToSupporter, bagelT)
	ops := &memOps{}
	g.r.Ops, g.r.Thread = ops, memThread{st: g.st}
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	reply := g.say(t, "when you get here can you stay the night")
	if len(chat.posted) != 0 {
		t.Fatalf("relayed to the supporter: %v", chat.posted)
	}
	if reply != scopeSet(t).Refusal("intimate").Reply {
		t.Fatalf("reply %q", reply)
	}
	if len(ops.notices) != 1 || len(ops.notices[0].OpenTasks) != 1 || ops.notices[0].OpenTasks[0].ID != "task-2" {
		t.Fatalf("notices %+v", ops.notices)
	}
	// An ordinary instruction still reaches the supporter.
	g.say(t, "leave it with the doorman please")
	if len(chat.posted) != 1 {
		t.Fatalf("instruction not relayed: %v", chat.posted)
	}
}

// Two messages that are fine alone and not together: the draft's words are
// checked again by rule.
func TestScopeDraftWordsTogether(t *testing.T) {
	g, _, o := scopeRig(t, nil)
	g.say(t, "pick up my laundry at my apartment, 12 Bedford Ave")
	parses := len(g.parser.sources)
	reply := g.say(t, "and then hang out for a bit")
	if !strings.HasPrefix(reply, scopeSet(t).Refusal("private_company").Reply) || len(g.parser.sources) != parses {
		t.Fatalf("reply %q parses %d→%d (model calls %d)", reply, parses, len(g.parser.sources), o.calls)
	}
}

// The model is asked only for words no rule decides; a model failure
// allows (the person still sees a summary and must say YES).
func TestScopeModelOnlyWhenRulesLeaveIt(t *testing.T) {
	g, _, o := scopeRig(t, nil)
	g.say(t, "pick up my dry cleaning at 12 Bedford Ave at 5pm")
	if o.calls != 0 {
		t.Fatalf("plain errand: %d model calls", o.calls)
	}
	g, _, o = scopeRig(t, map[string]string{"my mom needs company on her walk, can someone go?": "third_person"})
	reply := g.say(t, "my mom needs company on her walk, can someone go?")
	if o.calls != 1 || reply != scopeSet(t).Refusal("third_person").Reply || len(g.parser.sources) != 0 {
		t.Fatalf("model refusal: calls %d reply %q", o.calls, reply)
	}
	g, _ = handoffRig(t, llm.IntentNewTask)
	g.r.Scope = failingScope{}
	g.say(t, "my mom needs company on her walk, can someone go?")
	if len(g.parser.sources) != 1 {
		t.Fatal("model error did not allow")
	}
}

type failingScope struct{}

func (failingScope) CheckScope(context.Context, string) (string, error) {
	return "", context.DeadlineExceeded
}

// Home entry (playbooks on): the access question when the words do not say
// who is there; the answer is a supporter note; the summary and the task
// carry the home-entry lines. Words that already say how to get in skip it.
func TestHomeEntryAskThroughRouter(t *testing.T) {
	pbs, _ := llm.Playbooks()
	g, _, _ := scopeRig(t, nil)
	g.r.Playbooks = pbs
	g.parser.draft = llm.TaskDraft{Title: "Groceries", Category: "grocery", Location1: "12 Bedford Ave, Brooklyn, NY 11211"}
	reply := g.say(t, "pick up groceries from Whole Foods and put them away in my kitchen at 12 Bedford Ave")
	he := pbs.ScopeRules.HomeEntry
	if !strings.Contains(reply, he.Ask.Question) {
		t.Fatalf("no access question:\n%s", reply)
	}
	sum := g.say(t, "my roommate Alex will be home")
	if !strings.Contains(sum, "set aside") {
		t.Fatalf("budget question expected:\n%s", sum)
	}
	sum = g.say(t, "$40")
	p := g.pending(t)
	if len(p.Notes) == 0 || p.Notes[len(p.Notes)-1] != "Access / Substitution preference: my roommate Alex will be home" {
		t.Fatalf("notes %q", p.Notes)
	}
	if !strings.Contains(sum, he.SummaryLine) || !strings.Contains(p.Items()[0].Draft.Description, he.SupporterNote) {
		t.Fatalf("summary / note missing:\n%s\n--\n%s", sum, p.Items()[0].Draft.Description)
	}

	g, _, _ = scopeRig(t, nil)
	g.r.Playbooks = pbs
	g.parser.draft = llm.TaskDraft{Title: "Groceries", Category: "grocery", Location1: "12 Bedford Ave, Brooklyn, NY 11211"}
	reply = g.say(t, "pick up groceries from Whole Foods and put them away, the doorman will let you in at 12 Bedford Ave")
	if strings.Contains(reply, he.Ask.Question) {
		t.Fatalf("asked although the words say how to get in:\n%s", reply)
	}
}

// Companionship (playbooks on): the public-place questions, the summary's
// public-only line, the supporter's note; a named public place skips the
// where question.
func TestCompanionshipPlaybook(t *testing.T) {
	pbs, _ := llm.Playbooks()
	cp := pbs.ByID("companionship")
	g, _, _ := scopeRig(t, nil)
	g.r.Playbooks = pbs
	g.parser.draft = llm.TaskDraft{Title: "Walk in Washington Square Park", Category: "companionship", Location1: "Washington Square Park, New York, NY 10012"}
	reply := g.say(t, "can you keep me company for a walk in Washington Square Park?")
	if p := g.pending(t); p.Playbook != "companionship" || strings.Join(p.PlaybookAsk, ",") != "when,how_long" {
		t.Fatalf("playbook %q asks %v\n%s", p.Playbook, p.PlaybookAsk, reply)
	}
	if !strings.Contains(reply, "When?") || !strings.Contains(reply, "For how long?") || strings.Contains(reply, "public place, like") {
		t.Fatalf("asks:\n%s", reply)
	}
	sum := g.say(t, "tomorrow at 3pm for an hour")
	if !strings.Contains(sum, cp.SummaryLines[0]) || !strings.Contains(sum, "Washington Square Park") {
		t.Fatalf("summary:\n%s", sum)
	}
	if d := g.pending(t).Items()[0].Draft.Description; !strings.Contains(d, cp.SupporterNotes[0]) {
		t.Fatalf("note: %q", d)
	}
}
