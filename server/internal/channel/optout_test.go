package channel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"hora-auth/internal/channel/llm"
)

// memOptOuts mirrors PGStore's opt-out semantics.
type memOptOuts struct {
	mu     sync.Mutex
	ids    *memIdentities
	marked map[string]bool // channel|address
	err    error
}

func (m *memOptOuts) MarkOptedOut(ctx context.Context, ch Channel, addr string) (Identity, bool, error) {
	id, err := m.ids.UpsertIdentity(ctx, ch, addr)
	if err != nil {
		return Identity{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(ch) + "|" + addr
	was := m.marked[k]
	m.marked[k] = true
	return id, !was, nil
}
func (m *memOptOuts) ClearOptedOut(_ context.Context, ch Channel, addr string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(ch) + "|" + addr
	was := m.marked[k]
	delete(m.marked, k)
	return was, nil
}
func (m *memOptOuts) OptedOut(_ context.Context, ch Channel, addr string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	return m.marked[string(ch)+"|"+addr], nil
}
func (m *memOptOuts) is(addr string) bool {
	ok, _ := m.OptedOut(context.Background(), IMessage, addr)
	return ok
}

func optOutRig(t *testing.T) (*rig, *memOptOuts, *memOps) {
	t.Helper()
	g, ops := handoffRig(t, llm.IntentNewTask)
	oo := &memOptOuts{ids: g.ids, marked: map[string]bool{}}
	g.r.OptOuts = oo
	return g, oo, ops
}

func TestOptOutKeywords(t *testing.T) {
	for _, kw := range []string{"STOP", "stop", "Stop.", " Cancel ", "CANCEL!", "cancel", "END", "quit", "Revoke", "OPT OUT", "opt-out", "optout", "Opt  out", "UNSUBSCRIBE", "unsubscribe."} {
		if !IsOptOutKeyword(kw) {
			t.Errorf("%q should be an opt-out keyword", kw)
		}
	}
	for _, not := range []string{"cancel task", "cancel my errand", "please cancel", "stop by the store", "don't stop", "the end", "start", "no", "cancelled", "取消", ""} {
		if IsOptOutKeyword(not) {
			t.Errorf("%q must not be an opt-out keyword", not)
		}
	}
	for _, in := range []string{"START", "start", "Start.", " start "} {
		if !IsOptInKeyword(in) {
			t.Errorf("%q should be START", in)
		}
	}
	if IsOptInKeyword("start the laundry") || IsOptInKeyword("restart") {
		t.Error("only START alone opts in")
	}
}

// The phrases our copy now teaches, and the ways people say them, are still
// cancel requests by rule.
func TestCancelPhrasesStillCancel(t *testing.T) {
	for _, s := range []string{"cancel task", "Cancel task", "cancel my errand", "cancel the booking", "please cancel", "can you cancel it", "I need to cancel the laundry"} {
		if !IsCancelRequest(s) {
			t.Errorf("%q should be a cancel request", s)
		}
		if IsOptOutKeyword(s) {
			t.Errorf("%q must not be an opt-out keyword", s)
		}
	}
}

// No customer copy tells the person to send an opt-out keyword on its own:
// a quoted keyword (“cancel”, "STOP") or a bare capitalised one ("reply
// CANCEL") in copy.go is a regression.
func TestCopyNeverTeachesAKeyword(t *testing.T) {
	src, err := os.ReadFile("copy.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range bareKeywordLines(string(src)) {
		t.Errorf("copy.go teaches a bare opt-out keyword: %s", hit)
	}
	// The scanner itself catches the strings this branch replaced.
	old := "\tCopyCancelNotDone = \"(I haven't cancelled “%s”. Say “cancel” again if you still want to.)\"\n" +
		"\tCopySetupAlreadySent = \"Lost it? Text me again, or reply CANCEL.\"\n" +
		"\t// a comment saying \"cancel\" is fine\n" +
		"\tCopyCancelHint = `To cancel, reply \"cancel task\".`\n"
	if got := bareKeywordLines(old); len(got) != 2 {
		t.Fatalf("scanner found %d of 2: %q", len(got), got)
	}
}

// bareKeywordLines: code lines that quote an opt-out keyword on its own
// (“cancel”, "STOP") or shout one ("reply CANCEL").
func bareKeywordLines(src string) []string {
	kw := `(?:stop|quit|end|revoke|opt[ -]?out|cancel|unsubscribe)`
	quoted := regexp.MustCompile(`(?i)[“"'‘]` + kw + `[.!]?[”"'’]`)
	shouted := regexp.MustCompile(`\b(?:STOP|QUIT|END|REVOKE|OPT OUT|CANCEL|UNSUBSCRIBE)\b`)
	var hits []string
	for i, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if quoted.MatchString(line) || shouted.MatchString(line) {
			hits = append(hits, fmt.Sprintf("line %d: %s", i+1, strings.TrimSpace(line)))
		}
	}
	return hits
}

func TestSummaryAndBookedTeachCancelTaskOnce(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.ids.link(me, "user-1")
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	summary := g.ad.last()
	if strings.Count(summary, CopyCancelHint) != 1 {
		t.Fatalf("summary must say %q once:\n%s", CopyCancelHint, summary)
	}
	if got := g.say(t, "YES"); got != CopyBooked || strings.Count(got, CopyCancelHint) != 1 {
		t.Fatalf("booked reply %q", got)
	}
	if !strings.Contains(CopyCancelHint, `"cancel task"`) {
		t.Fatalf("hint %q", CopyCancelHint)
	}
	if strings.Count(CopyBookedAll, CopyCancelHint) != 1 {
		t.Fatalf("booked-all %q", CopyBookedAll)
	}
}

// A bare STOP while a supporter's request waits for a YES/NO: an opt-out,
// not a NO. Nothing is declined, nothing is sent, ops hear it with the open
// task, and later sends are blocked before the provider.
func TestStopIsAnOptOutNotAnAnswer(t *testing.T) {
	g, oo, ops := optOutRig(t)
	g.booker.open = []TaskRef{{ID: "task-2", Title: "Bagel run"}}
	g.arm(t, "ext-t1")
	callsBefore := g.ad.calls

	in := Inbound{Channel: IMessage, ProviderMessageID: "stop-1", From: me, Text: "STOP", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 0 {
		t.Fatalf("STOP answered the request: %v", g.booker.resolved)
	}
	if g.ad.calls != callsBefore {
		t.Fatalf("something was sent to a number that opted out (%d calls)", g.ad.calls-callsBefore)
	}
	if !oo.is(me) {
		t.Fatal("not marked opted out")
	}
	if len(ops.notices) != 1 {
		t.Fatalf("ops notices %d", len(ops.notices))
	}
	n := ops.notices[0]
	if n.Reason != HandoffOptedOut || !n.OptedOut || len(n.OpenTasks) != 1 || n.OpenTasks[0].ID != "task-2" {
		t.Fatalf("notice %+v", n)
	}
	if n.PhoneHint != "•••• 4567" || strings.Contains(n.PhoneHint, "555123") {
		t.Fatalf("phone hint %q", n.PhoneHint)
	}

	// A status update afterwards is refused before the provider.
	err := g.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "Sam accepted"}, "task-2")
	if !errors.Is(err, ErrRecipientOptedOut) || g.ad.calls != callsBefore {
		t.Fatalf("send to opted-out number: err=%v calls=%d", err, g.ad.calls-callsBefore)
	}
	// Anything else they write is not acted on, and not answered.
	if err := g.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: "yes-1", From: me, Text: "YES", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if len(g.booker.resolved) != 0 || g.ad.calls != callsBefore {
		t.Fatalf("message from opted-out number was acted on: resolved=%v calls=%d", g.booker.resolved, g.ad.calls-callsBefore)
	}
	// A second keyword does not alert ops again.
	if err := g.r.HandleInbound(context.Background(), Inbound{Channel: IMessage, ProviderMessageID: "stop-2", From: me, Text: "stop", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if len(ops.notices) != 1 {
		t.Fatalf("ops alerted twice: %d", len(ops.notices))
	}
}

// A bare CANCEL no longer starts a cancel whose questions could never
// arrive; "cancel task" still does.
func TestBareCancelOptsOutCancelTaskCancels(t *testing.T) {
	g, oo, _ := optOutRig(t)
	g.booker.open = []TaskRef{{ID: "task-1", Title: "Laundry pickup"}}
	if got := g.say(t, "Cancel"); got != "" || !oo.is(me) || g.state() == StateAwaitingCancel {
		t.Fatalf("bare Cancel: reply %q opted=%t state=%s", got, oo.is(me), g.state())
	}

	h, oo2, _ := optOutRig(t)
	h.booker.open = []TaskRef{{ID: "task-1", Title: "Laundry pickup"}}
	if got := h.say(t, "cancel task"); oo2.is(me) || !strings.Contains(got, "Laundry pickup") {
		t.Fatalf("cancel task: reply %q opted=%t", got, oo2.is(me))
	}
}

// On WhatsApp there are no carrier keywords: "cancel" stays a cancel.
func TestWhatsAppCancelIsNotAnOptOut(t *testing.T) {
	g, oo, _ := optOutRig(t)
	g.r.Adapters[WhatsApp] = g.ad
	g.booker.open = []TaskRef{{ID: "task-1", Title: "Laundry pickup"}}
	wa := "+15559876543"
	g.ids.mu.Lock()
	g.ids.ids["whatsapp|"+wa] = Identity{ID: "id-wa", Channel: WhatsApp, Address: wa, UserID: "user-1"}
	g.ids.mu.Unlock()
	in := Inbound{Channel: WhatsApp, ProviderMessageID: "wa-1", From: wa, Text: "cancel", ReceivedAt: time.Now()}
	if err := g.r.HandleInbound(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if ok, _ := oo.OptedOut(context.Background(), WhatsApp, wa); ok {
		t.Fatal("WhatsApp cancel marked an opt-out")
	}
	if !strings.Contains(g.ad.last(), "Laundry pickup") {
		t.Fatalf("reply %q", g.ad.last())
	}
}

func TestStartClearsAndWelcomesBack(t *testing.T) {
	g, oo, _ := optOutRig(t)
	g.say(t, "STOP")
	if got := g.say(t, "START"); got != CopyOptInWelcome {
		t.Fatalf("START reply %q", got)
	}
	if oo.is(me) {
		t.Fatal("still opted out after START")
	}
	// Texts flow again.
	if err := g.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "hi"}, ""); err != nil {
		t.Fatal(err)
	}
	// START from a number that never stopped is an ordinary message.
	h, _, _ := optOutRig(t)
	h.r.Classifier = fakeClassifier{intent: llm.IntentGreeting}
	if got := h.say(t, "start"); got == CopyOptInWelcome {
		t.Fatal("welcome-back sent to a number that never stopped")
	}
}

// Sendblue refusing a send with OPTED_OUT (a keyword we never saw): marked,
// ops told once, the route counts as handled, and the next send is not
// attempted.
func TestProviderOptedOutMarks(t *testing.T) {
	g, oo, ops := optOutRig(t)
	g.ad.err = &ProviderStatusError{Provider: "sendblue", Status: 400, Text: `{"error_message":"OPTED_OUT"}`, Cause: ErrRecipientOptedOut}
	err := g.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "Sam accepted"}, "task-1")
	if !errors.Is(err, ErrRecipientOptedOut) {
		t.Fatalf("err %v", err)
	}
	if !oo.is(me) || len(ops.notices) != 1 || !ops.notices[0].OptedOut {
		t.Fatalf("opted=%t notices=%d", oo.is(me), len(ops.notices))
	}
	calls := g.ad.calls
	if err := g.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "again"}, ""); !errors.Is(err, ErrRecipientOptedOut) || g.ad.calls != calls {
		t.Fatalf("second send: err=%v provider calls +%d", err, g.ad.calls-calls)
	}
	if len(ops.notices) != 1 {
		t.Fatalf("ops alerted %d times", len(ops.notices))
	}
	// Other provider errors mark nothing.
	h, oo2, _ := optOutRig(t)
	h.ad.err = &ProviderStatusError{Provider: "sendblue", Status: 400, Text: "plan limit"}
	if err := h.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "x"}, ""); err == nil || oo2.is(me) {
		t.Fatalf("plain 400 marked: err=%v", err)
	}
}

// The webhook marks a message whose reply was refused for opt-out as
// processed: re-routing it would be refused again.
func TestWebhookTreatsOptedOutReplyAsHandled(t *testing.T) {
	st := &fakeStore{}
	h := &WebhookHandler{Adapter: &fakeAdapter{}, Store: st, Router: routerFunc(func(context.Context, Inbound) error {
		return ErrRecipientOptedOut
	})}
	h.route(Inbound{Channel: IMessage, ProviderMessageID: "m1"})
	if len(st.processed) != 1 {
		t.Fatalf("marked %v", st.processed)
	}
	h.Router = routerFunc(func(context.Context, Inbound) error { return errors.New("db down") })
	h.route(Inbound{Channel: IMessage, ProviderMessageID: "m2"})
	if len(st.processed) != 1 {
		t.Fatal("an ordinary failure was marked processed")
	}
}

type routerFunc func(context.Context, Inbound) error

func (f routerFunc) HandleInbound(ctx context.Context, in Inbound) error { return f(ctx, in) }

// Fail closed: when the mark cannot be read, nothing is sent.
func TestOptOutCheckFailureSendsNothing(t *testing.T) {
	g, oo, _ := optOutRig(t)
	oo.err = errors.New("db down")
	if err := g.r.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "x"}, ""); err == nil || g.ad.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, g.ad.calls)
	}
}

// "cancel task" is never taken as a first name, nor a reason to resend the
// setup link: the draft is dropped, as everywhere else.
func TestCancelTaskWhileDrafting(t *testing.T) {
	g := newRig(llm.IntentNewTask)
	g.say(t, "pick up my laundry at 12 Bedford Ave")
	if g.state() != StateAwaitingName {
		t.Fatalf("state %s", g.state())
	}
	if got := g.say(t, "cancel task"); got != CopyDroppedCancelHint || g.state() != StateIdle {
		t.Fatalf("name step: %q state %s", got, g.state())
	}

	h := newRig(llm.IntentNewTask)
	h.say(t, "pick up my laundry at 12 Bedford Ave")
	h.say(t, "Aura")
	h.say(t, "YES") // not linked: setup link
	if h.state() != StateAwaitingSetup {
		t.Fatalf("state %s", h.state())
	}
	if got := h.say(t, "cancel task"); got != CopyDroppedCancelHint || h.state() != StateIdle {
		t.Fatalf("setup step: %q state %s", got, h.state())
	}
}

func TestSendblueOptedOutError(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"status":"ERROR","error_message":"OPTED_OUT","number":"+15551234567"}`, true},
		{400, `{"error_message":"Recipient has opted out"}`, true},
		{400, `OPTED_OUT`, true},
		{400, `{"status":"ERROR","error_message":"Plan limit reached"}`, false},
		{500, `{"error_message":"OPTED_OUT"}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		a := &SendblueAdapter{APIKey: "k", APISecret: "s", FromNumber: "+12125550100", BaseURL: srv.URL}
		_, err := a.Send(context.Background(), Outbound{Channel: IMessage, To: me, Text: "hi"})
		srv.Close()
		if err == nil {
			t.Fatalf("%d %s: no error", tc.status, tc.body)
		}
		if got := errors.Is(err, ErrRecipientOptedOut); got != tc.want {
			t.Errorf("%d %s: opted out = %t, want %t", tc.status, tc.body, got, tc.want)
		}
		if strings.Contains(err.Error(), "5551234567") {
			t.Errorf("error leaks the number: %v", err)
		}
	}
}

func TestPGOptOut(t *testing.T) {
	db := testDB(t)
	s := &PGStore{DB: db}
	ctx := context.Background()
	if ok, err := s.OptedOut(ctx, IMessage, me); err != nil || ok {
		t.Fatalf("unknown number: %t %v", ok, err)
	}
	// A number never seen: created, marked, newly.
	id, newly, err := s.MarkOptedOut(ctx, IMessage, me)
	if err != nil || !newly || id.ID == "" {
		t.Fatalf("first mark: %+v %t %v", id, newly, err)
	}
	if _, newly, err := s.MarkOptedOut(ctx, IMessage, me); err != nil || newly {
		t.Fatalf("second mark newly=%t err=%v", newly, err)
	}
	if ok, err := s.OptedOut(ctx, IMessage, me); err != nil || !ok {
		t.Fatalf("marked: %t %v", ok, err)
	}
	// Same number on WhatsApp is a different identity.
	if ok, _ := s.OptedOut(ctx, WhatsApp, me); ok {
		t.Fatal("whatsapp identity marked")
	}
	// Linked: the app sees it.
	var uid string
	if err := db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE channel_identities SET user_id = $1 WHERE id = $2`, uid, id.ID); err != nil {
		t.Fatal(err)
	}
	if stopped, at, err := s.UserTextsStopped(ctx, uid); err != nil || !stopped || at.IsZero() {
		t.Fatalf("user texts stopped: %t %v %v", stopped, at, err)
	}
	// Existing identity, unmarked → marked again is newly, and carries the user.
	if cleared, err := s.ClearOptedOut(ctx, IMessage, me); err != nil || !cleared {
		t.Fatalf("clear: %t %v", cleared, err)
	}
	if cleared, _ := s.ClearOptedOut(ctx, IMessage, me); cleared {
		t.Fatal("second clear reported cleared")
	}
	if stopped, _, _ := s.UserTextsStopped(ctx, uid); stopped {
		t.Fatal("still stopped after clear")
	}
	got, newly, err := s.MarkOptedOut(ctx, IMessage, me)
	if err != nil || !newly || got.UserID != uid || got.ID != id.ID {
		t.Fatalf("re-mark: %+v %t %v", got, newly, err)
	}
	// UpsertIdentity still works with the column present.
	if up, err := s.UpsertIdentity(ctx, IMessage, me); err != nil || up.ID != id.ID {
		t.Fatalf("upsert: %+v %v", up, err)
	}
	// Racing marks: exactly one is newly (it decides the ops alert).
	if _, err := s.ClearOptedOut(ctx, IMessage, me); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	firsts := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, n, err := s.MarkOptedOut(ctx, IMessage, me)
			if err != nil {
				t.Error(err)
			}
			if n {
				mu.Lock()
				firsts++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firsts != 1 {
		t.Fatalf("%d racers saw newly", firsts)
	}
	// The migration keeps the table locked to clients (Rule 3).
	var rls bool
	if err := db.QueryRow(`SELECT relrowsecurity FROM pg_class WHERE relname = 'channel_identities'`).Scan(&rls); err != nil || !rls {
		t.Fatalf("rls %t %v", rls, err)
	}
	var grants int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.role_table_grants
		WHERE table_name = 'channel_identities' AND grantee IN ('anon','authenticated','PUBLIC')`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("client grants %d %v", grants, err)
	}
	var policies int
	if err := db.QueryRow(`SELECT count(*) FROM pg_policies WHERE tablename = 'channel_identities'`).Scan(&policies); err != nil || policies != 0 {
		t.Fatalf("policies %d %v", policies, err)
	}
}
