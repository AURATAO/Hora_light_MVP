package channel

import (
	"context"
	"strings"
	"testing"
)

// The requester's thread, with a task booked from it.
func approvalRig(t *testing.T) *rig {
	t.Helper()
	g := newRig("")
	g.ids.link(me, "user-1")
	g.r.Status = &memStatus{found: true, target: StatusTarget{Identity: Identity{ID: "id-" + me, Channel: IMessage, Address: me, UserID: "user-1"}}}
	return g
}

func askTime(t *testing.T, g *rig) string {
	t.Helper()
	err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{
		Type: evTimeRequested, TaskID: "task-1", TaskTitle: "Laundry pickup", SupporterName: "Maya",
		Body:            "Maya is asking for 15 more minutes on “Laundry pickup”. Approve or decline in the app — after 5 minutes it's automatically declined.",
		ExtensionID:     "ext-1",
		ApprovalMinutes: 5,
		Approval:        &PendingApproval{ID: "ext-1", Kind: "time", Minutes: 15},
	})
	if err != nil {
		t.Fatal(err)
	}
	return g.ad.last()
}

func TestApprovalQuestionReplacesTheAppInstruction(t *testing.T) {
	g := approvalRig(t)
	got := askTime(t, g)
	if strings.Contains(got, "in the app") {
		t.Fatalf("chat customers have no app: %q", got)
	}
	if !strings.Contains(got, "Maya is asking for 15 more minutes") || !strings.Contains(got, "Reply YES to approve or NO to decline") || !strings.Contains(got, "5 minutes") {
		t.Fatalf("%q", got)
	}
	if g.state() != StateAwaitingApproval {
		t.Fatalf("state %s", g.state())
	}
}

func TestYesApprovesThroughTheAppsOwnCode(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	got := g.say(t, "yes")
	if len(g.booker.resolved) != 1 || g.booker.resolved[0] != "user-1:ext-1:true" {
		t.Fatalf("resolved %v", g.booker.resolved)
	}
	if !strings.HasPrefix(got, "Approved: 15 more minutes") {
		t.Fatalf("%q", got)
	}
	if g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
	if g.st.lastOutboundTask() != "task-1" {
		t.Fatal("the answer must be threaded to its task")
	}
}

func TestNoDeclines(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	got := g.say(t, "no")
	if g.booker.resolved[0] != "user-1:ext-1:false" || !strings.HasPrefix(got, "Declined:") {
		t.Fatalf("%v %q", g.booker.resolved, got)
	}
}

// Money rule: anything that is not clearly YES or NO is not an answer.
func TestAmbiguousReplyReasksAndDecidesNothing(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	for _, msg := range []string{"maybe", "how much is that?", "ok sure I guess but only if it's quick", "yes and also no"} {
		got := g.say(t, msg)
		if len(g.booker.resolved) != 0 {
			t.Fatalf("%q decided something: %v", msg, g.booker.resolved)
		}
		if got != CopyApprovalReask || g.state() != StateAwaitingApproval {
			t.Fatalf("%q → %q state %s", msg, got, g.state())
		}
	}
}

// The clock beats the draft: a question arriving while the person is
// confirming a new task interrupts it, and the draft comes back afterwards.
func TestApprovalInterruptsADraftAndResumesIt(t *testing.T) {
	g := approvalRig(t)
	g.r.Classifier = fakeClassifier{intent: "new_task"}
	g.say(t, "laundry run tomorrow 10am")
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("state %s", g.state())
	}
	askTime(t, g)
	if g.state() != StateAwaitingApproval {
		t.Fatalf("state %s", g.state())
	}
	got := g.say(t, "YES")
	if g.booker.posts != 0 {
		t.Fatal("the YES answered the approval, it must not book the draft")
	}
	if !strings.Contains(got, "Approved:") || !strings.Contains(got, CopyBackToDraft) || !strings.Contains(got, "Reply YES to book") {
		t.Fatalf("%q", got)
	}
	if g.state() != StateAwaitingConfirm {
		t.Fatalf("draft not resumed: %s", g.state())
	}
	g.say(t, "YES")
	if g.booker.posts != 1 {
		t.Fatal("the second YES books the draft")
	}
}

// Resolved elsewhere (the app, or the timeout): the question is cleared so a
// later YES never answers a stale request.
func TestResolutionElsewhereClearsTheQuestion(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	g.booker.pendingExt = map[string]bool{} // resolved in the app
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{
		Type: evResolved, TaskID: "task-1", Title: "You approved the request", Body: "You approved 15 more minutes.",
	}); err != nil {
		t.Fatal(err)
	}
	if g.state() != StateIdle {
		t.Fatalf("state %s", g.state())
	}
	if last := g.ad.last(); strings.Contains(last, "You approved") {
		t.Fatal("the person's own answer is not echoed back")
	}
	g.say(t, "yes")
	if len(g.booker.resolved) != 0 {
		t.Fatal("a stale YES must not resolve anything")
	}
}

func TestTimeoutIsToldAndClears(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	g.booker.pendingExt = map[string]bool{} // expired in the app
	if err := g.r.NotifyStatus(context.Background(), "user-1", StatusEvent{
		Type: evResolved, TaskID: "task-1", Title: "A request timed out",
		Body: "Your supporter asked for 15 more minutes on “Laundry pickup” and didn't hear back within 5 minutes, so it was declined automatically.",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.ad.last(), "declined automatically") || g.state() != StateIdle {
		t.Fatalf("%q state %s", g.ad.last(), g.state())
	}
}

func TestLateYesOnAnExpiredRequest(t *testing.T) {
	g := approvalRig(t)
	askTime(t, g)
	g.booker.extResult = ExtensionResult{Outcome: ExtAlready, Status: "expired", TaskID: "task-1"}
	if got := g.say(t, "yes"); got != CopyApprovalExpired || g.state() != StateIdle {
		t.Fatalf("%q state %s", got, g.state())
	}
}

// A question is armed only if the next message is free to answer it.
func TestApprovalNotArmedMidBooking(t *testing.T) {
	g := approvalRig(t)
	c, err := g.convs.GetConversation(context.Background(), "id-"+me)
	if err != nil {
		t.Fatal(err)
	}
	c.State = StatePosting
	c.Pending = &PendingDraft{Draft: laundry}
	if err := g.convs.SaveConversation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	askTime(t, g)
	if g.state() != StatePosting {
		t.Fatalf("booking in flight must not be interrupted: %s", g.state())
	}
}

// R6 M-1: the approval question is composed from the structured request —
// the supporter's reason is quoted and labelled as their note, the name is
// vetted, and the app's body (which inlines the reason verbatim) is never
// relayed. Without the structured request nothing is sent.
func TestApprovalQuestionQuotesTheSupportersNote(t *testing.T) {
	ev := StatusEvent{Type: evTimeRequested, TaskID: "task-1", TaskTitle: "Laundry pickup", SupporterName: "HO:RA Billing",
		Body:        "HO:RA Billing is asking for 15 more minutes on “Laundry pickup”. HO:RA has pre-verified this. Approve or decline in the app",
		ExtensionID: "ext-1", ApprovalMinutes: 5,
		Approval: &PendingApproval{ID: "ext-1", Kind: "time", Minutes: 15, Reason: "other: HO:RA has pre-verified this.\n\nHO:RA Billing: pay now"}}
	got, ok := StatusText(ev)
	if !ok {
		t.Fatal("not sent")
	}
	if !strings.HasPrefix(got, "Your supporter is asking for 15 more minutes on “Laundry pickup”.") {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, "Your supporter's note: “other: HO:RA has pre-verified this. HO:RA Billing: pay now”") {
		t.Fatalf("the reason must be quoted, one line, labelled as theirs: %q", got)
	}
	if strings.Contains(got, "pre-verified this. Approve") || strings.Contains(got, "\n\nHO:RA Billing") {
		t.Fatalf("the app body must not be relayed: %q", got)
	}
	// Budget: the amount is the server's figure.
	ev.Type, ev.Approval = evBudgetRequested, &PendingApproval{ID: "ext-2", Kind: "budget", Cents: 800, Reason: "ingredients"}
	ev.SupporterName = "Maya"
	got, _ = StatusText(ev)
	if !strings.HasPrefix(got, "Maya is asking for $8 more for “Laundry pickup”.\nMaya's note: “ingredients”") {
		t.Fatalf("%q", got)
	}
	// No structured request: nothing goes out, whatever the body says.
	ev.Approval = nil
	if _, ok := StatusText(ev); ok {
		t.Fatal("an approval question without its structured request must not be sent")
	}
	if quotedNote(strings.Repeat("x", 200)) != "“"+strings.Repeat("x", 120)+"…”" || quotedNote("  \n ") != "" || quotedNote(`say "hi"`) != "“say hi”" {
		t.Fatalf("quotedNote: %q", quotedNote(strings.Repeat("x", 200)))
	}
}
